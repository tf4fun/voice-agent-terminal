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

// The I2S DMA ring holds only 90 ms, so the network reader queues PCM here and
// a writer task feeds the codec. Both buffers and the writer are reserved once
// at boot: allocating ~11 KiB per reply fragmented the heap (the largest free
// block fell from 59 KiB to 35 KiB after one spoken reply).
enum {
    PLAY_BUFFER = 8192, // ~256 ms of 16 kHz PCM16
    PLAY_START = 6400,  // Prebuffer 200 ms before the first sound...
    PLAY_RESUME = 3200, // ...and 100 ms after running dry.
    WRITER_STACK = 2560,
};

static SemaphoreHandle_t s_lock;
static TaskHandle_t s_task, s_writer;
static agent_speech_view_t s_view;
static char s_request_session[37], s_request_message[37];
static atomic_uint s_request;   // Incremented by play/stop; the task follows the latest.
static atomic_bool s_running;   // The task holds a stream (and the speaker).

static StreamBufferHandle_t s_pcm;
static StaticStreamBuffer_t s_pcm_control;
static uint8_t s_pcm_storage[PLAY_BUFFER + 1];
static StaticTask_t s_writer_tcb;
static StackType_t s_writer_stack[WRITER_STACK / sizeof(StackType_t)];
static SemaphoreHandle_t s_writer_done;
static StaticSemaphore_t s_writer_done_control;
static atomic_bool s_ended, s_stop;
static uint8_t s_chunk[1024];
static agent_speech_parser_t s_parser;

static void lock(void) { xSemaphoreTake(s_lock, portMAX_DELAY); }
static void unlock(void) { xSemaphoreGive(s_lock); }

static void publish(bool active, bool audible, uint16_t index, const char *sentence, const char *message)
{
    lock();
    if (s_view.active != active) s_view.serial++;
    s_view.active = active;
    s_view.audible = audible;
    s_view.index = index;
    if (!active) s_view.level = 0;
    if (sentence != NULL) snprintf(s_view.sentence, sizeof(s_view.sentence), "%s", sentence);
    if (message != NULL) snprintf(s_view.message_id, sizeof(s_view.message_id), "%s", message);
    unlock();
}

static void publish_level(uint8_t level)
{
    lock();
    s_view.level = level;
    unlock();
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

// Drains the playback buffer into the codec for one stream at a time.
static void writer_task(void *arg)
{
    (void)arg;
    for (;;) {
        ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
        bool primed = false;
        size_t carry = 0; // A received odd byte waits for the rest of its sample.
        for (;;) {
            if (atomic_load(&s_stop)) break;
            const size_t queued = xStreamBufferBytesAvailable(s_pcm);
            const bool ended = atomic_load(&s_ended);
            if (!primed) {
                // Wait for enough audio to ride out network and UI jitter.
                if (queued >= (size_t)PLAY_START || (ended && queued > 0)) primed = true;
                else if (ended) break;
                else { vTaskDelay(pdMS_TO_TICKS(10)); continue; }
            }
            const size_t got = xStreamBufferReceive(s_pcm, s_chunk + carry, sizeof(s_chunk) - carry, pdMS_TO_TICKS(20));
            if (got == 0) {
                if (atomic_load(&s_ended)) break;
                // Ran dry: refill a little before speaking again rather than stutter.
                if (xStreamBufferBytesAvailable(s_pcm) < (size_t)PLAY_RESUME) { primed = false; publish_level(0); }
                continue;
            }
            const size_t total = carry + got, even = total & ~(size_t)1;
            publish_level(loudness(s_chunk, even));
            if (even > 0 && bsp_audio_write(s_chunk, even) != ESP_OK) break;
            carry = total - even;
            if (carry) s_chunk[0] = s_chunk[even];
        }
        publish_level(0);
        xSemaphoreGive(s_writer_done);
    }
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

// Plays one stream until it ends, fails or a newer request supersedes it.
static void play(unsigned request, const char *session, const char *message)
{
    agent_speech_parser_reset(&s_parser);
    publish(true, false, 0, "", message);
    esp_http_client_handle_t client = open_stream(session, message);
    uint8_t status = 1;
    uint16_t index = 0;
    bool writing = false;
    if (client != NULL && bsp_audio_set_format(16000, 16, 1) == ESP_OK) {
        // The writer is idle here, so the buffer can be emptied safely.
        xStreamBufferReset(s_pcm);
        atomic_store(&s_ended, false);
        atomic_store(&s_stop, false);
        xTaskNotifyGive(s_writer);
        writing = true;
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
                agent_speech_event_t event;
                offset += agent_speech_parse(&s_parser, buffer + offset, (size_t)got - offset, &event);
                if (s_parser.invalid) { done = true; break; }
                if (event.kind == AGENT_SPEECH_AUDIO) {
                    // Queue for the writer; wait in short slices so a stop stays prompt.
                    size_t sent = 0;
                    while (sent < event.size && atomic_load(&s_request) == request)
                        sent += xStreamBufferSend(s_pcm, event.data + sent, event.size - sent, pdMS_TO_TICKS(50));
                } else if (event.kind == AGENT_SPEECH_SENTENCE && event.size >= 2) {
                    index = (uint16_t)(event.data[0] | (event.data[1] << 8));
                    char sentence[AGENT_SPEECH_SENTENCE_BYTES];
                    size_t text = event.size - 2;
                    if (text >= sizeof(sentence)) text = sizeof(sentence) - 1;
                    // Cut before a UTF-8 continuation byte so the text stays valid.
                    while (text > 0 && text < event.size - 2 && (event.data[2 + text] & 0xC0) == 0x80) text--;
                    memcpy(sentence, event.data + 2, text);
                    sentence[text] = '\0';
                    publish(true, true, index, sentence, NULL);
                } else if (event.kind == AGENT_SPEECH_END) {
                    status = event.size ? event.data[0] : 1;
                    done = true;
                }
            }
        }
        if (atomic_load(&s_request) != request) status = 2;
    }
    if (writing) {
        // Let the queued audio finish unless playback was stopped.
        atomic_store(&s_ended, true);
        if (status == 2) atomic_store(&s_stop, true);
        while (xSemaphoreTake(s_writer_done, pdMS_TO_TICKS(50)) != pdTRUE)
            if (atomic_load(&s_request) != request) atomic_store(&s_stop, true);
    }
    if (client != NULL) {
        esp_http_client_close(client);
        esp_http_client_cleanup(client);
    }
    agent_trace(TRACE_SPEECH, status, index, NULL);
    publish(false, false, index, NULL, NULL);
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
    s_pcm = xStreamBufferCreateStatic(PLAY_BUFFER, 1, s_pcm_storage, &s_pcm_control);
    s_writer_done = xSemaphoreCreateBinaryStatic(&s_writer_done_control);
    if (s_lock == NULL || s_pcm == NULL || s_writer_done == NULL) return ESP_ERR_NO_MEM;
    // At the capture task's priority, above LVGL; the two never run together.
    s_writer = xTaskCreateStatic(writer_task, "agent_play", WRITER_STACK / sizeof(StackType_t), NULL, 6,
                                 s_writer_stack, &s_writer_tcb);
    if (s_writer == NULL) return ESP_ERR_NO_MEM;
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
    // The writer finishes its current 32 ms chunk; the read loop then exits.
    for (int i = 0; wait && i < 50 && atomic_load(&s_running); i++) vTaskDelay(pdMS_TO_TICKS(10));
}

void agent_speech_copy(agent_speech_view_t *out)
{
    if (s_lock == NULL) { memset(out, 0, sizeof(*out)); return; }
    lock();
    *out = s_view;
    unlock();
}
