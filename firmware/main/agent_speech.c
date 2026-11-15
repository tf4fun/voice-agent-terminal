#include "agent_speech.h"

#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "agent_speech_frames.h"
#include "agent_trace.h"
#include "bsp_audio.h"
#include "esp_http_client.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/stream_buffer.h"
#include "freertos/task.h"
#include "memo_discovery.h"
#include "memo_net.h"
#include "sdkconfig.h"

static const char *TAG = "agent_speech";

static SemaphoreHandle_t s_lock;
static TaskHandle_t s_task;
static agent_speech_view_t s_view;
static char s_request_session[37], s_request_message[37];
static atomic_uint s_request;   // Incremented by play/stop; the task follows the latest.
static atomic_bool s_running;   // The task holds a stream (and the speaker).

// Playback buffer: the network reader fills it while a separate writer feeds
// the 90 ms I2S DMA ring, so neither a slow read nor a busy UI starves the
// speaker. Allocated only while a stream plays.
enum {
    PLAY_BUFFER = 12288, // ~384 ms of 16 kHz PCM16
    PLAY_START = 6400,   // Prebuffer 200 ms before the first sound...
    PLAY_RESUME = 3200,  // ...and 100 ms after running dry.
};
typedef struct {
    StreamBufferHandle_t pcm;
    atomic_bool ended, stop;
    SemaphoreHandle_t done;
    uint8_t chunk[1024];
} player_t;

static void lock(void) { xSemaphoreTake(s_lock, portMAX_DELAY); }
static void unlock(void) { xSemaphoreGive(s_lock); }

static void publish(bool active, bool audible, uint16_t index, const char *sentence, const char *message, uint8_t level)
{
    lock();
    if (s_view.active != active) s_view.serial++;
    s_view.active = active;
    s_view.audible = audible;
    s_view.index = index;
    s_view.level = level;
    if (sentence != NULL) snprintf(s_view.sentence, sizeof(s_view.sentence), "%s", sentence);
    if (message != NULL) snprintf(s_view.message_id, sizeof(s_view.message_id), "%s", message);
    unlock();
}

static esp_http_client_handle_t open_stream(const char *session, const char *message)
{
    memo_endpoint_t endpoint;
    if (!memo_net_has_ip() || !memo_discovery_endpoint(&endpoint)) return NULL;
    char url[256];
    const int length = snprintf(url, sizeof(url), "http://%s:%u/api/v1/sessions/%s/messages/%s/speech",
                                endpoint.host, endpoint.port, session, message);
    if (length < 0 || length >= (int)sizeof(url)) return NULL;
    // Keepalive frames arrive every 5 s while the reply is still being thought about.
    const esp_http_client_config_t config = {
        .url = url, .method = HTTP_METHOD_GET, .timeout_ms = 12000, .buffer_size = 1024,
        .buffer_size_tx = 512, .disable_auto_redirect = true, .keep_alive_enable = false,
    };
    esp_http_client_handle_t client = esp_http_client_init(&config);
    if (client == NULL) return NULL;
    if (CONFIG_AGENT_API_KEY[0] != '\0') {
        char authorization[sizeof(CONFIG_AGENT_API_KEY) + 8];
        snprintf(authorization, sizeof(authorization), "Bearer %s", CONFIG_AGENT_API_KEY);
        esp_http_client_set_header(client, "Authorization", authorization);
    }
    if (esp_http_client_open(client, 0) != ESP_OK || esp_http_client_fetch_headers(client) < 0 ||
        esp_http_client_get_status_code(client) != 200) {
        ESP_LOGW(TAG, "speech stream unavailable status=%d", esp_http_client_get_status_code(client));
        esp_http_client_close(client);
        esp_http_client_cleanup(client);
        return NULL;
    }
    return client;
}

static uint8_t loudness(const uint8_t *pcm, size_t bytes)
{
    uint32_t sum = 0;
    const size_t samples = bytes / 2;
    for (size_t i = 0; i < samples; i += 4) {
        const int16_t value = (int16_t)(pcm[2 * i] | (pcm[2 * i + 1] << 8));
        sum += value < 0 ? -(int32_t)value : value;
    }
    const uint32_t level = samples ? sum / ((samples + 3) / 4) / 96 : 0;
    return level > 100 ? 100 : (uint8_t)level;
}

static void publish_level(uint8_t level)
{
    lock();
    s_view.level = level;
    unlock();
}

static void writer_task(void *arg)
{
    player_t *player = arg;
    bool primed = false;
    for (;;) {
        if (atomic_load(&player->stop)) break;
        const size_t queued = xStreamBufferBytesAvailable(player->pcm);
        const bool ended = atomic_load(&player->ended);
        if (!primed) {
            // Wait for enough audio to ride out network and UI jitter.
            if (queued >= (size_t)PLAY_START || (ended && queued > 0)) primed = true;
            else if (ended) break;
            else { vTaskDelay(pdMS_TO_TICKS(10)); continue; }
        }
        const size_t got = xStreamBufferReceive(player->pcm, player->chunk, sizeof(player->chunk), pdMS_TO_TICKS(20));
        if (got == 0) {
            if (atomic_load(&player->ended)) break;
            // Ran dry: refill a little before speaking again rather than stutter.
            if (xStreamBufferBytesAvailable(player->pcm) < (size_t)PLAY_RESUME) { primed = false; publish_level(0); }
            continue;
        }
        publish_level(loudness(player->chunk, got));
        if (bsp_audio_write(player->chunk, got) != ESP_OK) break;
    }
    publish_level(0);
    xSemaphoreGive(player->done);
    vTaskDelete(NULL);
}

// Plays one stream until it ends, fails or a newer request supersedes it.
static void play(unsigned request, const char *session, const char *message)
{
    agent_speech_parser_t *parser = malloc(sizeof(*parser));
    if (parser == NULL) { ESP_LOGW(TAG, "no memory for speech"); return; }
    agent_speech_parser_reset(parser);
    publish(true, false, 0, "", message, 0);
    esp_http_client_handle_t client = open_stream(session, message);
    uint8_t status = 1;
    char sentence[AGENT_SPEECH_SENTENCE_BYTES];
    uint16_t index = 0;
    player_t *player = calloc(1, sizeof(*player));
    if (player != NULL) {
        player->pcm = xStreamBufferCreate(PLAY_BUFFER, 1);
        player->done = xSemaphoreCreateBinary();
    }
    const bool ready = player != NULL && player->pcm != NULL && player->done != NULL &&
        xTaskCreate(writer_task, "agent_play", 2560, player, 6, NULL) == pdPASS;
    if (!ready) {
        ESP_LOGW(TAG, "no memory for speech playback");
        if (player != NULL) {
            if (player->pcm != NULL) vStreamBufferDelete(player->pcm);
            if (player->done != NULL) vSemaphoreDelete(player->done);
            free(player);
            player = NULL;
        }
    }
    if (client != NULL && player != NULL && bsp_audio_set_format(16000, 16, 1) == ESP_OK) {
        uint8_t buffer[512];
        bool done = false;
        // IDF reads try to fill the buffer; a short timeout keeps stop requests
        // responsive. Keepalives every 5 s mean 15 s of silence is a dead stream.
        esp_http_client_set_timeout_ms(client, 100);
        int64_t heard = esp_timer_get_time();
        while (!done && atomic_load(&s_request) == request) {
            const int got = esp_http_client_read(client, (char *)buffer, sizeof(buffer));
            if (got == -ESP_ERR_HTTP_EAGAIN) {
                if (esp_timer_get_time() - heard > 15000000) break;
                continue;
            }
            if (got <= 0) break;
            heard = esp_timer_get_time();
            size_t offset = 0;
            while (offset < (size_t)got && !done && atomic_load(&s_request) == request) {
                bool complete = false;
                offset += agent_speech_parse(parser, buffer + offset, (size_t)got - offset, &complete);
                if (parser->invalid) { done = true; break; }
                if (!complete) continue;
                if (parser->kind == AGENT_SPEECH_SENTENCE && parser->length >= 2) {
                    index = (uint16_t)(parser->payload[0] | (parser->payload[1] << 8));
                    size_t text = parser->length - 2;
                    if (text >= sizeof(sentence)) {
                        // Cut before a UTF-8 continuation byte so the text stays valid.
                        text = sizeof(sentence) - 1;
                        while (text > 0 && (parser->payload[2 + text] & 0xC0) == 0x80) text--;
                    }
                    memcpy(sentence, parser->payload + 2, text);
                    sentence[text] = '\0';
                    publish(true, true, index, sentence, NULL, 0);
                } else if (parser->kind == AGENT_SPEECH_AUDIO) {
                    // Queue for the writer; wait in short slices so a stop stays prompt.
                    size_t sent = 0;
                    while (sent < parser->length && atomic_load(&s_request) == request)
                        sent += xStreamBufferSend(player->pcm, parser->payload + sent, parser->length - sent, pdMS_TO_TICKS(50));
                } else if (parser->kind == AGENT_SPEECH_END) {
                    status = parser->length ? parser->payload[0] : 1;
                    done = true;
                }
            }
        }
        if (atomic_load(&s_request) != request) status = 2;
    }
    if (player != NULL) {
        // Let the queued audio finish unless playback was stopped.
        atomic_store(&player->ended, true);
        if (status == 2 || client == NULL) atomic_store(&player->stop, true);
        while (xSemaphoreTake(player->done, pdMS_TO_TICKS(50)) != pdTRUE)
            if (atomic_load(&s_request) != request) atomic_store(&player->stop, true);
        vStreamBufferDelete(player->pcm);
        vSemaphoreDelete(player->done);
        free(player);
    }
    if (client != NULL) {
        esp_http_client_close(client);
        esp_http_client_cleanup(client);
    }
    free(parser);
    agent_trace(TRACE_SPEECH, status, index, NULL);
    publish(false, false, index, NULL, NULL, 0);
}

static void speech_task(void *arg)
{
    (void)arg;
    unsigned handled = 0;
    for (;;) {
        ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
        for (;;) {
            const unsigned request = atomic_load(&s_request);
            if (request == handled) break;
            handled = request;
            char session[37], message[37];
            lock();
            snprintf(session, sizeof(session), "%s", s_request_session);
            snprintf(message, sizeof(message), "%s", s_request_message);
            unlock();
            if (message[0] == '\0') continue; // A stop request.
            atomic_store(&s_running, true);
            play(request, session, message);
            atomic_store(&s_running, false);
        }
    }
}

esp_err_t agent_speech_init(void)
{
    s_lock = xSemaphoreCreateMutex();
    if (s_lock == NULL) return ESP_ERR_NO_MEM;
    return xTaskCreate(speech_task, "agent_speech", 4096, NULL, 5, &s_task) == pdPASS ? ESP_OK : ESP_ERR_NO_MEM;
}

void agent_speech_play(const char *session_id, const char *message_id)
{
    if (s_task == NULL) return;
    lock();
    snprintf(s_request_session, sizeof(s_request_session), "%s", session_id);
    snprintf(s_request_message, sizeof(s_request_message), "%s", message_id);
    unlock();
    atomic_fetch_add(&s_request, 1);
    xTaskNotifyGive(s_task);
}

void agent_speech_stop(bool wait)
{
    if (s_task == NULL) return;
    lock();
    s_request_message[0] = '\0';
    unlock();
    atomic_fetch_add(&s_request, 1);
    xTaskNotifyGive(s_task);
    // The current audio frame (at most 128 ms) finishes; the read loop then exits.
    for (int i = 0; wait && i < 50 && atomic_load(&s_running); i++) vTaskDelay(pdMS_TO_TICKS(10));
}

void agent_speech_copy(agent_speech_view_t *out)
{
    if (s_lock == NULL) { memset(out, 0, sizeof(*out)); return; }
    lock();
    *out = s_view;
    unlock();
}
