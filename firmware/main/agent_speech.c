#include "agent_speech.h"

#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#include "agent_pcm_pool.h"
#include "agent_speech_frames.h"
#include "agent_trace.h"
#include "bsp_audio.h"
#include "esp_http_client.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/stream_buffer.h"
#include "freertos/task.h"
#include "memo_discovery.h"
#include "memo_net.h"
#include "sdkconfig.h"

static const char *TAG = "agent_speech";

// The I2S DMA ring holds only 90 ms, so the network reader queues PCM in the
// 16 KiB buffer shared with recording (agent_pcm_pool) and a writer task feeds
// the codec. The buffer and the writer are reserved once at boot: allocating
// them per reply fragmented the heap.
enum {
    PLAY_START = 12800, // Prebuffer 400 ms before the first sound...
    PLAY_RESUME = 6400, // ...and 200 ms after running dry.
    WRITER_STACK = 2560,
};

static SemaphoreHandle_t s_lock;
static TaskHandle_t s_task, s_writer;
static agent_speech_view_t s_view;
static char s_request_session[37], s_request_message[37];
static atomic_uint s_request;   // Incremented by play/stop; the task follows the latest.
static atomic_bool s_running;   // The task holds a stream (and the speaker).

static StreamBufferHandle_t s_pcm; // Borrowed from the pool while a reply plays.
static StaticTask_t s_writer_tcb;
static StackType_t s_writer_stack[WRITER_STACK / sizeof(StackType_t)];
static SemaphoreHandle_t s_writer_done;
static StaticSemaphore_t s_writer_done_control;
static atomic_bool s_ended, s_stop;
static atomic_uint s_underruns; // Times the speaker ran dry mid-stream (diagnostics).
static atomic_uint s_played;    // PCM bytes sent to the speaker for this stream.
static _Atomic int64_t s_heard_us; // Last time stream bytes arrived.
static atomic_uint s_longest_gap_ms; // Longest wait for stream bytes in this reply.
static uint8_t s_chunk[1024];
static agent_speech_parser_t s_parser;

static void lock(void) { xSemaphoreTake(s_lock, portMAX_DELAY); }
static void unlock(void) { xSemaphoreGive(s_lock); }

static void publish(bool active, bool audible, const char *message)
{
    lock();
    s_view.active = active;
    s_view.audible = audible;
    if (!active) s_view.level = 0;
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

// Ramps PCM16LE samples linearly from silence up (or down to silence). The
// I2S ring outputs zeros when it runs dry, so an unramped stop or restart
// mid-waveform is a step the speaker plays as a click.
static void ramp(uint8_t *pcm, size_t bytes, bool up)
{
    const size_t samples = bytes / 2;
    if (samples == 0) return;
    for (size_t i = 0; i < samples; i++) {
        int16_t value = (int16_t)(pcm[2 * i] | (pcm[2 * i + 1] << 8));
        const int32_t weight = (int32_t)(up ? i : samples - 1 - i);
        value = (int16_t)((int32_t)value * weight / (int32_t)samples);
        pcm[2 * i] = (uint8_t)value;
        pcm[2 * i + 1] = (uint8_t)((uint16_t)value >> 8);
    }
}

// Drains the playback buffer into the codec for one stream at a time.
static void writer_task(void *arg)
{
    (void)arg;
    for (;;) {
        ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
        bool primed = false, fade_in = true;
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
                if (xStreamBufferBytesAvailable(s_pcm) < (size_t)PLAY_RESUME) {
                    primed = false;
                    fade_in = true;
                    publish_level(0);
                    // Record whether the network went quiet, to tell a stalled
                    // radio from synthesis that could not keep up.
                    wifi_ap_record_t ap = {0};
                    const int rssi = esp_wifi_sta_get_ap_info(&ap) == ESP_OK ? ap.rssi : 0;
                    ESP_LOGI(TAG, "speech underrun %u: no stream data for %lld ms, rssi %d",
                             atomic_fetch_add(&s_underruns, 1) + 1,
                             (long long)((esp_timer_get_time() - atomic_load(&s_heard_us)) / 1000), rssi);
                }
                continue;
            }
            const size_t total = carry + got, even = total & ~(size_t)1;
            // Fade into sound after silence, and out of it when this chunk
            // empties the buffer before the stream has ended.
            if (fade_in) { ramp(s_chunk, even, true); fade_in = false; }
            else if (!atomic_load(&s_ended) && xStreamBufferBytesAvailable(s_pcm) == 0) {
                ramp(s_chunk, even, false);
                fade_in = true;
            }
            publish_level(loudness(s_chunk, even));
            if (even > 0 && bsp_audio_write(s_chunk, even) != ESP_OK) {
                atomic_store(&s_stop, true); // The reader stops filling a buffer nobody drains.
                break;
            }
            atomic_fetch_add(&s_played, (unsigned)even);
            carry = total - even;
            if (carry) s_chunk[0] = s_chunk[even];
        }
        publish_level(0);
        xSemaphoreGive(s_writer_done);
    }
}

typedef struct {
    char *id;
    size_t capacity;
} speech_header_t;

static esp_err_t speech_header(esp_http_client_event_t *event)
{
    speech_header_t *target = event->user_data;
    if (target == NULL || target->id == NULL || target->capacity == 0 ||
        event->event_id != HTTP_EVENT_ON_HEADER ||
        event->header_key == NULL || event->header_value == NULL) return ESP_OK;
    if (strcasecmp(event->header_key, "X-Speech-Stream") == 0)
        snprintf(target->id, target->capacity, "%s", event->header_value);
    return ESP_OK;
}

// *status is the HTTP status, or 0 when no response arrived.
static esp_http_client_handle_t open_stream(const char *session, const char *message, size_t offset,
                                            const char *stream_id, char *received_id, size_t received_cap,
                                            int *status)
{
    *status = 0;
    if (received_id != NULL && received_cap > 0) received_id[0] = '\0';
    if (offset > 0 && (stream_id == NULL || stream_id[0] == '\0')) {
        *status = 409;
        return NULL;
    }
    memo_endpoint_t endpoint;
    if (!memo_net_has_ip() || !memo_discovery_endpoint(&endpoint)) return NULL;
    char url[320];
    const int length = offset > 0
        ? snprintf(url, sizeof(url), "http://%s:%u/api/v1/sessions/%s/messages/%s/speech?offset=%u&stream=%s",
                   endpoint.host, endpoint.port, session, message, (unsigned)offset, stream_id)
        : snprintf(url, sizeof(url), "http://%s:%u/api/v1/sessions/%s/messages/%s/speech?offset=0",
                   endpoint.host, endpoint.port, session, message);
    if (length < 0 || length >= (int)sizeof(url)) return NULL;
    speech_header_t header = {.id = received_id, .capacity = received_cap};
    // Keepalive frames arrive every 5 s while the reply is still being thought about.
    const esp_http_client_config_t config = {
        .url = url, .method = HTTP_METHOD_GET, .timeout_ms = 12000, .buffer_size = 1024,
        .buffer_size_tx = 512, .disable_auto_redirect = true, .keep_alive_enable = false,
        .event_handler = speech_header, .user_data = &header,
    };
    esp_http_client_handle_t client = esp_http_client_init(&config);
    if (client == NULL) return NULL;
    if (CONFIG_AGENT_API_KEY[0] != '\0') {
        char authorization[sizeof(CONFIG_AGENT_API_KEY) + 8];
        snprintf(authorization, sizeof(authorization), "Bearer %s", CONFIG_AGENT_API_KEY);
        esp_http_client_set_header(client, "Authorization", authorization);
    }
    if (esp_http_client_open(client, 0) == ESP_OK && esp_http_client_fetch_headers(client) >= 0)
        *status = esp_http_client_get_status_code(client);
    if (*status == 200 && offset > 0 && received_id != NULL && received_id[0] != '\0' &&
        strcmp(received_id, stream_id) != 0) {
        *status = 409;
    }
    if (*status != 200) {
        ESP_LOGW(TAG, "speech stream unavailable status=%d", *status);
        esp_http_client_close(client);
        esp_http_client_cleanup(client);
        return NULL;
    }
    return client;
}

// After a dropped connection, waits up to 10 s for the network to return.
static bool network_back(unsigned request)
{
    for (int i = 0; i < 100 && atomic_load(&s_request) == request; i++) {
        if (memo_net_has_ip() && i >= 3) return true;
        vTaskDelay(pdMS_TO_TICKS(100));
    }
    return false;
}

enum { STREAM_DONE, STREAM_STOPPED, STREAM_BROKEN };

// Reads one connection into the playback buffer and reports how it ended.
// *stream_offset counts the stream bytes taken in, keepalives excluded, so a dropped
// connection resumes exactly there; the parser carries over between them.
static int read_stream(esp_http_client_handle_t client, unsigned request, size_t *stream_offset, uint8_t *status)
{
    uint8_t buffer[512];
    // IDF reads try to fill the buffer; a short timeout keeps stop requests
    // responsive. Keepalives every 5 s mean 15 s of silence is a dead stream.
    esp_http_client_set_timeout_ms(client, 100);
    int64_t heard = esp_timer_get_time();
    while (atomic_load(&s_request) == request) {
        const int got = esp_http_client_read(client, (char *)buffer, sizeof(buffer));
        if (got == -ESP_ERR_HTTP_EAGAIN) {
            if (esp_timer_get_time() - heard > 15000000) return STREAM_BROKEN;
            continue;
        }
        if (got <= 0) return STREAM_BROKEN;
        const int64_t now = esp_timer_get_time();
        const unsigned gap = (unsigned)((now - heard) / 1000);
        if (gap > atomic_load(&s_longest_gap_ms)) atomic_store(&s_longest_gap_ms, gap);
        heard = now;
        atomic_store(&s_heard_us, now);
        size_t offset = 0;
        while (offset < (size_t)got && atomic_load(&s_request) == request) {
            agent_speech_event_t event;
            const size_t used = agent_speech_parse(&s_parser, buffer + offset, (size_t)got - offset, &event);
            offset += used;
            agent_speech_track(stream_offset, used, &event);
            if (s_parser.invalid) return STREAM_BROKEN;
            if (event.kind == AGENT_SPEECH_AUDIO) {
                // Queue for the writer; wait in short slices so a stop stays prompt.
                size_t sent = 0;
                while (sent < event.size && atomic_load(&s_request) == request) {
                    if (atomic_load(&s_stop)) return STREAM_BROKEN; // The speaker failed.
                    sent += xStreamBufferSend(s_pcm, event.data + sent, event.size - sent, pdMS_TO_TICKS(50));
                }
            } else if (event.kind == AGENT_SPEECH_SENTENCE) {
                // Marks the start of speech; the text it carries is not shown
                // (the strip reads the reply the device fetched itself).
                publish(true, true, NULL);
            } else if (event.kind == AGENT_SPEECH_END) {
                *status = event.size ? event.data[0] : 1;
                return STREAM_DONE;
            }
        }
    }
    return STREAM_STOPPED;
}

// Plays one reply until it ends, fails or a newer request supersedes it. A
// dropped connection resumes at the exact byte it stopped at (up to 3 times).
static void play(unsigned request, const char *session, const char *message)
{
    publish(true, false, message);
    memo_net_hold_awake(true); // Modem sleep missed beacons while streaming.
    uint8_t status = 1;
    int outcome = STREAM_BROKEN, resumes = 0;
    size_t offset = 0;
    char stream_id[37] = "";
    agent_speech_parser_reset(&s_parser);
    // Recording owns the shared buffer while it runs; a reply never plays over it.
    s_pcm = agent_pcm_pool_take(AGENT_PCM_PLAYBACK);
    if (s_pcm == NULL) status = 2;
    else if (bsp_audio_set_format(16000, 16, 1) == ESP_OK) {
        atomic_store(&s_underruns, 0);
        atomic_store(&s_played, 0);
        atomic_store(&s_longest_gap_ms, 0);
        atomic_store(&s_heard_us, esp_timer_get_time());
        atomic_store(&s_ended, false);
        atomic_store(&s_stop, false);
        xTaskNotifyGive(s_writer);
        for (;;) {
            agent_speech_resume(&s_parser, &offset);
            if (offset == 0) stream_id[0] = '\0';
            int http_status;
            char next_stream[37] = "";
            esp_http_client_handle_t client = open_stream(session, message, offset, stream_id,
                                                          next_stream, sizeof(next_stream), &http_status);
            // 409: the stream this playback started on is gone; new audio
            // would not continue it, so playback ends here.
            if (http_status == 409) break;
            if (client != NULL) {
                if (offset == 0 && next_stream[0] != '\0')
                    snprintf(stream_id, sizeof(stream_id), "%s", next_stream);
                outcome = read_stream(client, request, &offset, &status);
                esp_http_client_close(client);
                esp_http_client_cleanup(client);
            }
            if (atomic_load(&s_request) != request) { outcome = STREAM_STOPPED; break; }
            if (outcome == STREAM_DONE || atomic_load(&s_stop) || resumes >= 3 || !network_back(request)) break;
            resumes++;
            ESP_LOGW(TAG, "speech stream dropped; resuming at byte %u", (unsigned)offset);
        }
        if (outcome == STREAM_STOPPED) status = 2;
        // Let the queued audio finish unless playback was stopped.
        atomic_store(&s_ended, true);
        if (status == 2) atomic_store(&s_stop, true);
        while (xSemaphoreTake(s_writer_done, pdMS_TO_TICKS(50)) != pdTRUE)
            if (atomic_load(&s_request) != request) atomic_store(&s_stop, true);
    }
    if (s_pcm != NULL) agent_pcm_pool_release(AGENT_PCM_PLAYBACK);
    s_pcm = NULL;
    memo_net_hold_awake(false);
    // "d<underruns> g<longest network gap, 0.1 s> r<resumes>"
    char tag[AGENT_TRACE_TAG_BYTES];
    const unsigned gap = atomic_load(&s_longest_gap_ms) / 100;
    snprintf(tag, sizeof(tag), "d%u g%u r%d", atomic_load(&s_underruns), gap > 999 ? 999 : gap, resumes);
    agent_trace(TRACE_SPEECH, status, 0, tag);
    ESP_LOGI(TAG, "speech end status=%u underruns=%u longest_gap_ms=%u resumes=%d", status,
             atomic_load(&s_underruns), atomic_load(&s_longest_gap_ms), resumes);
    publish(false, false, NULL);
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
    s_writer_done = xSemaphoreCreateBinaryStatic(&s_writer_done_control);
    if (s_lock == NULL || s_writer_done == NULL || agent_pcm_pool_init() != ESP_OK) return ESP_ERR_NO_MEM;
    // At the capture task's priority, above LVGL; the two never run together.
    s_writer = xTaskCreateStatic(writer_task, "agent_play", WRITER_STACK / sizeof(StackType_t), NULL, 6,
                                 s_writer_stack, &s_writer_tcb);
    if (s_writer == NULL) return ESP_ERR_NO_MEM;
    // Above LVGL and the input task so reads keep the playback buffer filled.
    return xTaskCreate(speech_task, "agent_speech", 4096, NULL, 6, &s_task) == pdPASS ? ESP_OK : ESP_ERR_NO_MEM;
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
    out->played_ms = atomic_load(&s_played) / 32; // 16 kHz PCM16 mono
}
