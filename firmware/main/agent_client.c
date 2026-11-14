#include "agent_client.h"

#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <errno.h>
#include <limits.h>
#include "agent_sse.h"
#include "memo_text.h"
#include "agent_capture.h"
#include "cJSON.h"
#include "esp_http_client.h"
#include "esp_log.h"
#include "esp_random.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "memo_discovery.h"
#include "memo_net.h"
#include "memo_provision.h"
#include "sdkconfig.h"

typedef enum {
    CMD_REFRESH, CMD_OPEN, CMD_CLOSE, CMD_RECORD, CMD_SEND,
    CMD_REVIEW, CMD_RETRY, CMD_PROVISION, CMD_PAUSE,
} command_kind_t;

typedef struct {
    command_kind_t kind;
    char id[37], title[AGENT_TITLE_BYTES + 1];
    uint32_t revision, selection;
    bool enabled;
} command_t;

static const char *TAG = "agent_client";
static SemaphoreHandle_t s_lock;
static QueueHandle_t s_commands;
static TaskHandle_t s_sync_task;
static agent_view_t s_view;
static atomic_bool s_held, s_cancelled, s_paused, s_reader_active, s_discover;
static atomic_bool s_resync;
static char s_operation_body[4096], s_sync_body[8192];
static uint8_t s_audio_chunk[1280];
static bool s_audio_started;
static char s_pending_message[37];
static uint32_t s_pending_revision;
static int s_draft_total;
static agent_view_t s_read_view;
static char *s_input_text, *s_reply_text;
static size_t s_reply_capacity;
static char s_cached_message[37];
static uint32_t s_cached_draft_revision;
static bool s_input_loaded, s_reply_loaded;
static agent_sse_t s_events;

typedef struct {
    uint32_t revision;
    char state[16];
} content_headers_t;

static void clear_message_cache(void)
{
    free(s_input_text);
    free(s_reply_text);
    s_input_text = s_reply_text = NULL;
    s_reply_capacity = 0;
    s_cached_message[0] = '\0';
    s_cached_draft_revision = 0;
    s_input_loaded = s_reply_loaded = false;
}

static esp_err_t response_header(esp_http_client_event_t *event)
{
    content_headers_t *headers = event->user_data;
    if (headers == NULL || event->event_id != HTTP_EVENT_ON_HEADER ||
        event->header_key == NULL || event->header_value == NULL) return ESP_OK;
    if (strcasecmp(event->header_key, "X-Revision") == 0) {
        char *end = NULL;
        const unsigned long value = strtoul(event->header_value, &end, 10);
        if (end != event->header_value && *end == '\0' && value <= UINT32_MAX)
            headers->revision = (uint32_t)value;
    } else if (strcasecmp(event->header_key, "X-Message-State") == 0) {
        agent_copy_text(headers->state, sizeof(headers->state), event->header_value);
    }
    return ESP_OK;
}

static void lock(void) { xSemaphoreTake(s_lock, portMAX_DELAY); }
static void unlock(void) { xSemaphoreGive(s_lock); }
static void changed(void) { s_view.generation++; }

void agent_client_copy(agent_view_t *out)
{
    if (out == NULL || s_lock == NULL) return;
    lock();
    *out = s_view;
    unlock();
}

static void notice(const char *text)
{
    lock();
    agent_copy_text(s_view.notice, sizeof(s_view.notice), text);
    changed();
    unlock();
}

static void uuid(char out[37])
{
    uint8_t b[16];
    esp_fill_random(b, sizeof(b));
    b[6] = (b[6] & 15) | 64;
    b[8] = (b[8] & 63) | 128;
    snprintf(out, 37, "%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
             b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15]);
}

static esp_http_client_handle_t http_client_with_headers(const char *path, esp_http_client_method_t method,
                                                         content_headers_t *headers)
{
    memo_endpoint_t endpoint;
    if (!memo_net_has_ip() || !memo_discovery_endpoint(&endpoint)) return NULL;
    char url[384];
    int length = snprintf(url, sizeof(url), "http://%s:%u/api/v1%s", endpoint.host, endpoint.port, path);
    if (length < 0 || length >= (int)sizeof(url)) return NULL;
    const esp_http_client_config_t config = {
        .url = url, .method = method, .timeout_ms = 4000, .buffer_size = 1024,
        .buffer_size_tx = 512, .disable_auto_redirect = true, .keep_alive_enable = false,
        .event_handler = response_header, .user_data = headers,
    };
    esp_http_client_handle_t client = esp_http_client_init(&config);
    if (client == NULL) return NULL;
    esp_http_client_set_header(client, "Accept", "application/json");
    if (CONFIG_AGENT_API_KEY[0] != '\0') {
        char authorization[sizeof(CONFIG_AGENT_API_KEY) + 8];
        snprintf(authorization, sizeof(authorization), "Bearer %s", CONFIG_AGENT_API_KEY);
        esp_http_client_set_header(client, "Authorization", authorization);
    }
    return client;
}

static esp_http_client_handle_t http_client(const char *path, esp_http_client_method_t method)
{
    return http_client_with_headers(path, method, NULL);
}

static bool write_all(esp_http_client_handle_t client, const void *data, size_t size)
{
    const char *bytes = data;
    while (size > 0) {
        const int written = esp_http_client_write(client, bytes, size);
        if (written <= 0) return false;
        bytes += written;
        size -= written;
    }
    return true;
}

static int read_response(esp_http_client_handle_t client, char *body, size_t capacity)
{
    const int64_t length = esp_http_client_fetch_headers(client);
    if (length < 0 || length >= (int64_t)capacity) return 0;
    size_t used = 0;
    for (;;) {
        if (used == capacity - 1) {
            if (!esp_http_client_is_complete_data_received(client)) return 0;
            break;
        }
        const int got = esp_http_client_read(client, body + used, capacity - used - 1);
        if (got < 0) return 0;
        if (got == 0) {
            if (!esp_http_client_is_complete_data_received(client)) return 0;
            break;
        }
        used += got;
    }
    body[used] = '\0';
    return esp_http_client_get_status_code(client);
}

static int request(esp_http_client_method_t method, const char *path, const char *payload,
                   char *body, size_t capacity)
{
    body[0] = '\0';
    esp_http_client_handle_t client = http_client(path, method);
    if (client == NULL) return 0;
    const size_t size = payload == NULL ? 0 : strlen(payload);
    if (payload != NULL) esp_http_client_set_header(client, "Content-Type", "application/json");
    int status = 0;
    if (esp_http_client_open(client, size) == ESP_OK && (size == 0 || write_all(client, payload, size)))
        status = read_response(client, body, capacity);
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    return status;
}

static const char *http_notice(int status)
{
    switch (status) {
    case 401: return "服务器认证失败";
    case 409: return "内容已更新，请重试";
    case 413: return "草稿已满";
    case 503: return "服务器尚未就绪";
    default: return "连接失败，请重试";
    }
}

static bool connected(void)
{
    memo_endpoint_t endpoint;
    if (!memo_net_has_ip() || !memo_discovery_endpoint(&endpoint) || !endpoint.confirmed) return false;
    return true;
}

static void discover_server(void)
{
    if (!memo_net_has_ip() || s_paused) return;
    s_discover = true;
    memo_discovery_search(2000);
    memo_endpoint_t endpoint;
    if (memo_discovery_endpoint(&endpoint)) {
        const int status = request(HTTP_METHOD_GET, "/server", NULL, s_operation_body, sizeof(s_operation_body));
        cJSON *root = cJSON_Parse(s_operation_body);
        const cJSON *service = cJSON_GetObjectItemCaseSensitive(root, "service");
        const cJSON *protocol = cJSON_GetObjectItemCaseSensitive(root, "protocol");
        if (status == 200 && cJSON_IsString(service) && strcmp(service->valuestring, "voice-agent-server") == 0 &&
            cJSON_IsNumber(protocol) && protocol->valueint == 1 && cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "drafts")) &&
            cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "bounded_paging")) &&
            cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "text_before")) &&
            cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "text_tail")) &&
            cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "draft_text")) &&
            cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "replace_recording")) &&
            cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "staged_recording")) &&
            cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "replacement_reservation")) &&
            cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "whole_messages")) &&
            cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "reply_sse"))) {
            memo_discovery_confirm(&endpoint);
            notice("");
        } else {
            memo_discovery_invalidate(&endpoint);
            notice(status == 200 ? "服务器版本不兼容" : http_notice(status));
        }
        cJSON_Delete(root);
    }
    lock();
    s_view.connected = connected();
    changed();
    unlock();
    s_discover = false;
    s_resync = true;
}

static bool still_selected(const agent_view_t *view)
{
    return s_view.selection == view->selection && strcmp(s_view.session_id, view->session_id) == 0;
}

static bool response_has_more(const char *json)
{
    cJSON *root = cJSON_Parse(json);
    const bool more = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "has_more"));
    cJSON_Delete(root);
    return more;
}

static bool poll_list(agent_view_t *view)
{
    char path[80];
    snprintf(path, sizeof(path), "/sessions?limit=4&offset=%d", view->session_requested);
    int status = request(HTTP_METHOD_GET, path, NULL, s_sync_body, sizeof(s_sync_body));
    agent_session_t items[AGENT_PAGE_SIZE] = {0};
    int count = 0;
    if (status != 200 || !agent_parse_sessions(s_sync_body, items, &count)) {
        lock(); s_view.sessions_loading = false; s_view.session_requested = s_view.session_offset; unlock();
        return false;
    }
    const bool more = response_has_more(s_sync_body);
    lock();
    if (s_view.session_requested == view->session_requested) {
        memcpy(s_view.sessions, items, sizeof(items));
        s_view.session_count = count;
        s_view.session_offset = view->session_requested;
        s_view.sessions_more = more;
        s_view.sessions_loading = false;
        s_view.sessions_ready = true;
        changed();
    }
    unlock();
    return true;
}

static bool poll_draft(agent_view_t *view)
{
    char path[160];
    snprintf(path, sizeof(path), "/sessions/%s/draft?limit=4&offset=%d", view->session_id, view->segment_requested);
    const int status = request(HTTP_METHOD_GET, path, NULL, s_sync_body, sizeof(s_sync_body));
    agent_segment_t items[AGENT_PAGE_SIZE] = {0};
    int count = 0;
    int total = 0;
    uint32_t revision = 0;
    if (status != 200 || !agent_parse_draft(s_sync_body, items, &count, &revision, &total)) {
        lock(); s_view.segments_loading = false; s_view.segment_requested = s_view.segment_offset; unlock();
        return false;
    }
    const bool more = response_has_more(s_sync_body);
    lock();
    if (still_selected(view) && s_view.segment_requested == view->segment_requested &&
        s_view.text_request == view->text_request && revision >= s_view.draft_revision) {
        memcpy(s_view.segments, items, sizeof(items));
        const bool new_draft = s_view.segment_total == 0 && total > 0;
        s_view.segment_count = count;
        s_view.segment_total = total;
        s_view.segment_offset = view->segment_requested;
        s_view.segments_more = more;
        s_view.segments_loading = false;
        s_view.draft_revision = revision;
        if (total == 0 && s_view.text_kind == AGENT_TEXT_DRAFT && s_view.capture == AGENT_IDLE) {
            s_view.text_id[0] = '\0';
            memset(&s_view.text, 0, sizeof(s_view.text));
            s_view.text_request++;
            s_view.text_kind = AGENT_TEXT_REPLY;
            s_view.text_loading = false;
            s_view.chat_loading = true;
            s_view.text_generation++;
            s_resync = true;
        }
        if (total > 0 && (s_view.text_id[0] == '\0' || (new_draft && s_view.capture == AGENT_IDLE))) {
            agent_copy_text(s_view.text_id, sizeof(s_view.text_id), view->session_id);
            s_view.text_kind = AGENT_TEXT_DRAFT;
            memset(&s_view.text, 0, sizeof(s_view.text));
            s_view.text_request++;
            s_view.text_loading = true;
        }
        changed();
    }
    unlock();
    return true;
}

// The caller holds s_lock while comparing a response with the current intent.
static bool same_text_request(const agent_view_t *view)
{
    return still_selected(view) && s_view.text_request == view->text_request &&
        s_view.text_kind == view->text_kind && strcmp(s_view.text_id, view->text_id) == 0 &&
        s_view.text.offset == view->text.offset;
}

static bool poll_text(agent_view_t *view)
{
    if (view->text_id[0] == '\0') return true;
    char path[240];
    if (view->text_kind != AGENT_TEXT_SEGMENT || view->capture == AGENT_IDLE) return true;
    snprintf(path, sizeof(path), "/sessions/%s/segments/%s/text?limit=512&tail=1", view->session_id, view->text_id);
    const int status = request(HTTP_METHOD_GET, path, NULL, s_sync_body, sizeof(s_sync_body));
    agent_text_t text;
    if (status != 200 || !agent_parse_text(s_sync_body, &text)) {
        if (status == 400 || status == 404) {
            lock();
            if (same_text_request(view)) {
                memset(&s_view.text, 0, sizeof(s_view.text));
                s_view.text_request++;
                if (status == 404) s_view.text_id[0] = '\0';
                changed();
            }
            unlock();
        }
        return false;
    }
    lock();
    if (same_text_request(view)) {
        s_view.text = text;
        s_view.text_loading = false;
        changed();
    }
    unlock();
    return true;
}

// Return complete UTF-8 content with one exact allocation and no JSON text copy.
static int read_content(const char *path, char **text, content_headers_t *headers)
{
    *text = NULL;
    *headers = (content_headers_t){0};
    esp_http_client_handle_t client = http_client_with_headers(path, HTTP_METHOD_GET, headers);
    if (client == NULL) return 0;
    int status = 0;
    if (esp_http_client_open(client, 0) == ESP_OK) {
        const int64_t length = esp_http_client_fetch_headers(client);
        status = esp_http_client_get_status_code(client);
        if (status == 200 && length >= 0 && length < UINT32_MAX) {
            char *body = malloc((size_t)length + 1);
            if (body == NULL) {
                notice("内存不足，请退出会话");
                status = 0;
            } else {
                size_t used = 0;
                while (used < (size_t)length) {
                    const size_t remaining = (size_t)length - used;
                    const int capacity = remaining > INT_MAX ? INT_MAX : (int)remaining;
                    const int received = esp_http_client_read(client, body + used, capacity);
                    if (received <= 0) break;
                    used += (size_t)received;
                }
                body[used] = '\0';
                if (used == (size_t)length && memchr(body, '\0', used) == NULL &&
                    memo_text_is_valid_utf8(body) && esp_http_client_is_complete_data_received(client)) {
                    *text = body;
                } else {
                    free(body);
                    status = 0;
                }
            }
        } else if (status == 200) status = 0;
    }
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    return status;
}

char *agent_client_text_copy(uint32_t selection, uint32_t generation)
{
    lock();
    const char *source = s_view.text_kind == AGENT_TEXT_REPLY ?
        (s_reply_text == NULL ? "" : s_reply_text) : s_input_text;
    if (s_view.text_kind == AGENT_TEXT_SEGMENT) source = s_view.text.text;
    char *copy = NULL;
    if (selection == s_view.selection && generation == s_view.text_generation && source != NULL) {
        const size_t length = strlen(source);
        copy = malloc(length + 1);
        if (copy != NULL) memcpy(copy, source, length + 1);
        else {
            agent_copy_text(s_view.notice, sizeof(s_view.notice), "内存不足，请退出会话");
            changed();
        }
    }
    unlock();
    return copy;
}

static bool poll_full_draft(agent_view_t *view)
{
    lock();
    const bool cached = s_input_loaded && s_cached_draft_revision == view->draft_revision;
    unlock();
    if (cached) {
        lock();
        if (still_selected(view) && s_view.text_request == view->text_request &&
            s_view.text_kind == AGENT_TEXT_DRAFT &&
            (s_view.text_loading || s_view.text.revision != s_cached_draft_revision ||
             s_view.text.total != strlen(s_input_text) || s_view.chat_loading)) {
            s_view.text.revision = s_cached_draft_revision;
            s_view.text.total = (uint32_t)strlen(s_input_text);
            s_view.text_loading = s_view.chat_loading = s_view.chat_sending = false;
            s_view.chat_ready = true;
            s_view.text_generation++;
            changed();
        }
        unlock();
        return true;
    }
    char path[128];
    snprintf(path, sizeof(path), "/sessions/%s/draft/content?revision=%lu",
             view->session_id, (unsigned long)view->draft_revision);
    char *text = NULL;
    content_headers_t headers;
    const int status = read_content(path, &text, &headers);
    if (status != 200 || text == NULL || headers.revision == 0) { free(text); return false; }
    lock();
    if (still_selected(view) && s_view.text_request == view->text_request &&
        s_view.capture == AGENT_IDLE && s_view.text_kind == AGENT_TEXT_DRAFT) {
        free(s_input_text);
        s_input_text = text;
        text = NULL;
        s_input_loaded = true;
        s_cached_message[0] = '\0';
        s_cached_draft_revision = headers.revision;
        s_view.draft_revision = s_view.text.revision = headers.revision;
        s_view.text.total = (uint32_t)strlen(s_input_text);
        s_view.text_loading = s_view.chat_loading = s_view.chat_sending = false;
        s_view.chat_ready = true;
        s_view.text_generation++;
        changed();
    }
    unlock();
    free(text);
    return true;
}

static bool poll_turn(agent_view_t *view)
{
    char path[192];
    if (view->turn_anchor[0] == '\0')
        snprintf(path, sizeof(path), "/sessions/%s/turn", view->session_id);
    else
        snprintf(path, sizeof(path), "/sessions/%s/turn?message=%s", view->session_id, view->turn_anchor);
    agent_turn_t turn;
    const int status = request(HTTP_METHOD_GET, path, NULL, s_sync_body, sizeof(s_sync_body));
    if (status != 200 || !agent_parse_turn(s_sync_body, &turn)) return false;
    lock();
    const bool selected = still_selected(view) && s_view.chat_request == view->chat_request &&
        s_view.capture == AGENT_IDLE && s_view.text_kind != AGENT_TEXT_DRAFT;
    if (!selected) { unlock(); return true; }
    if (!turn.present) {
        clear_message_cache();
        s_view.turn = turn;
        memset(&s_view.message, 0, sizeof(s_view.message));
        s_view.chat_loading = s_view.text_loading = s_view.chat_sending = false;
        s_view.chat_ready = true;
        s_view.text_generation++;
        changed();
        unlock();
        return true;
    }
    if (strcmp(s_cached_message, turn.message.id) != 0) {
        clear_message_cache();
        agent_copy_text(s_cached_message, sizeof(s_cached_message), turn.message.id);
    }
    const bool need_input = !s_input_loaded;
    const bool need_reply = !s_reply_loaded || turn.message.revision != s_view.message.revision ||
        turn.message.reply_bytes != (s_reply_text == NULL ? 0 : strlen(s_reply_text));
    unlock();
    char *input = NULL, *reply = NULL;
    content_headers_t input_headers = {0}, reply_headers = {0};
    if (need_input) {
        snprintf(path, sizeof(path), "/sessions/%s/messages/%s/content?part=input", view->session_id, turn.message.id);
        if (read_content(path, &input, &input_headers) != 200 || input == NULL) goto failed;
    }
    if (need_reply) {
        snprintf(path, sizeof(path), "/sessions/%s/messages/%s/content?part=reply", view->session_id, turn.message.id);
        if (read_content(path, &reply, &reply_headers) != 200 || reply == NULL) goto failed;
        if (reply_headers.revision != 0) turn.message.revision = reply_headers.revision;
        if (reply_headers.state[0] != '\0') agent_copy_text(turn.message.state, sizeof(turn.message.state), reply_headers.state);
        turn.message.reply_bytes = (uint32_t)strlen(reply);
    }
    lock();
    if (still_selected(view) && s_view.chat_request == view->chat_request &&
        s_view.capture == AGENT_IDLE && s_view.text_kind != AGENT_TEXT_DRAFT &&
        strcmp(s_cached_message, turn.message.id) == 0) {
        if (input != NULL) { free(s_input_text); s_input_text = input; input = NULL; s_input_loaded = true; }
        if (reply != NULL) {
            free(s_reply_text);
            s_reply_text = reply;
            s_reply_capacity = strlen(reply) + 1;
            reply = NULL;
            s_reply_loaded = true;
        }
        s_view.turn = turn;
        s_view.message = turn.message;
        agent_copy_text(s_view.text_id, sizeof(s_view.text_id), turn.message.id);

        agent_copy_text(s_view.turn_anchor, sizeof(s_view.turn_anchor), turn.message.id);
        s_view.chat_ready = true;
        s_view.chat_loading = s_view.text_loading = s_view.chat_sending = false;
        s_view.text_generation++;
        if (strcmp(s_view.notice, "读取失败，请重试") == 0) s_view.notice[0] = '\0';
        changed();
    }
    unlock();
    free(input);
    free(reply);
    return true;
failed:
    free(input);
    free(reply);
    return false;
}

typedef struct {
    char session[37];
    uint32_t selection, request;
} watch_context_t;

static bool watch_current(const watch_context_t *watch)
{
    lock();
    const bool current = !s_paused && memo_net_has_ip() && s_view.capture == AGENT_IDLE &&
        s_view.text_kind != AGENT_TEXT_DRAFT && s_view.selection == watch->selection &&
        s_view.chat_request == watch->request && strcmp(s_view.session_id, watch->session) == 0;
    unlock();
    return current;
}

static bool append_reply(const char *id, size_t offset, const char *delta)
{
    const size_t incoming = strlen(delta);
    if (!memo_text_is_valid_utf8(delta)) return false;
    lock();
    if (strcmp(id, s_cached_message) != 0) { unlock(); return true; }
    const size_t current = s_reply_text == NULL ? 0 : strlen(s_reply_text);
    if (offset > current) { unlock(); return false; }
    const size_t overlap = current - offset < incoming ? current - offset : incoming;
    if (overlap != 0 && memcmp(s_reply_text + offset, delta, overlap) != 0) { unlock(); return false; }
    const size_t extra = incoming - overlap;
    if (extra == 0) { unlock(); return true; }
    if (extra > SIZE_MAX - current - 1) { unlock(); return false; }
    const size_t needed = current + extra + 1;
    if (needed > s_reply_capacity) {
        size_t capacity = s_reply_capacity + s_reply_capacity / 2;
        if (capacity < needed) capacity = needed;
        char *next = realloc(s_reply_text, capacity);
        if (next == NULL) {
            agent_copy_text(s_view.notice, sizeof(s_view.notice), "内存不足，请退出会话");
            changed();
            unlock();
            return false;
        }
        s_reply_text = next;
        s_reply_capacity = capacity;
    }
    memcpy(s_reply_text + current, delta + overlap, extra);
    s_reply_text[current + extra] = '\0';
    s_reply_loaded = true;
    s_view.message.reply_bytes = (uint32_t)(current + extra);
    agent_copy_text(s_view.message.state, sizeof(s_view.message.state), "receiving");
    s_view.turn.message = s_view.message;
    if (s_view.text_kind == AGENT_TEXT_REPLY) s_view.text_generation++;
    changed();
    unlock();
    return true;
}

static bool stream_event(const char *json, void *context)
{
    watch_context_t *watch = context;
    if (!watch_current(watch)) return false;
    cJSON *root = cJSON_Parse(json);
    const cJSON *type = cJSON_GetObjectItemCaseSensitive(root, "type");
    const cJSON *id = cJSON_GetObjectItemCaseSensitive(root, "message_id");
    bool refresh = false, ok = cJSON_IsString(type);
    if (ok && strcmp(type->valuestring, "resync") == 0) refresh = true;
    else if (ok && cJSON_IsString(id)) {
        lock();
        const bool selected = strcmp(id->valuestring, s_cached_message) == 0;
        unlock();
        if (selected && strcmp(type->valuestring, "chat.delta") == 0) {
            const cJSON *text = cJSON_GetObjectItemCaseSensitive(root, "text");
            const cJSON *offset = cJSON_GetObjectItemCaseSensitive(root, "offset");
            const bool valid_offset = offset == NULL || (cJSON_IsNumber(offset) &&
                offset->valuedouble >= 0 && offset->valuedouble <= UINT32_MAX &&
                offset->valuedouble == (uint32_t)offset->valuedouble);
            if (!cJSON_IsString(text) || !valid_offset) ok = false;
            else refresh = !append_reply(id->valuestring, offset == NULL ? 0 : (size_t)offset->valuedouble, text->valuestring);
        } else if (selected && (strcmp(type->valuestring, "chat.reset") == 0 ||
                   strcmp(type->valuestring, "message.updated") == 0)) refresh = true;
    }
    cJSON_Delete(root);
    if (refresh && watch_current(watch)) {
        agent_client_copy(&s_read_view);
        ok = poll_turn(&s_read_view);
    }
    return ok;
}

static bool watch_reply(agent_view_t *view)
{
    char path[112];
    snprintf(path, sizeof(path), "/events?session_id=%s", view->session_id);
    watch_context_t watch = {.selection = view->selection, .request = view->chat_request};
    agent_copy_text(watch.session, sizeof(watch.session), view->session_id);
    esp_http_client_handle_t client = http_client(path, HTTP_METHOD_GET);
    if (client == NULL) return false;
    esp_http_client_set_header(client, "Accept", "text/event-stream");
    bool opened = esp_http_client_open(client, 0) == ESP_OK;
    if (opened) opened = esp_http_client_fetch_headers(client) >= 0 && esp_http_client_get_status_code(client) == 200;
    if (opened) {
        // IDF reads try to fill the requested buffer. Bound the small-event tail
        // wait to one UI frame instead of holding tokens for a quarter second.
        esp_http_client_set_timeout_ms(client, 40);
        memset(&s_events, 0, sizeof(s_events));
        char bytes[256];
        while (watch_current(&watch)) {
            const int count = esp_http_client_read(client, bytes, sizeof(bytes));
            if (count == -ESP_ERR_HTTP_EAGAIN) continue;
            if (count <= 0 || !agent_sse_feed(&s_events, bytes, (size_t)count, stream_event, &watch)) break;
            lock();
            const bool active = agent_message_active(s_view.message.state);
            unlock();
            if (!active) break;
        }
    }
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    lock();
    const bool active = agent_message_active(s_view.message.state);
    unlock();
    return !watch_current(&watch) || !active;
}

static void sync_task(void *arg)
{
    (void)arg;
    int64_t last_list = 0;
    unsigned failures = 0;
    for (;;) {
        if (!s_paused && connected()) {
            s_reader_active = true;
            agent_client_copy(&s_read_view);
            const bool urgent = atomic_exchange(&s_resync, false);
            bool ok = true;
            if (!s_read_view.session_switching && s_read_view.session_id[0] == '\0') {
                const int64_t now = esp_timer_get_time();
                if (urgent || s_read_view.sessions_loading || now - last_list >= 5000000) {
                    ok = poll_list(&s_read_view);
                    last_list = now;
                }
            } else if (!s_read_view.session_switching && s_read_view.capture != AGENT_IDLE) {
                ok = poll_draft(&s_read_view);
                agent_client_copy(&s_read_view);
                if (ok && s_read_view.text_kind == AGENT_TEXT_SEGMENT) ok = poll_text(&s_read_view);
            } else if (!s_read_view.session_switching && s_read_view.text_kind == AGENT_TEXT_DRAFT) {
                ok = poll_draft(&s_read_view);
                agent_client_copy(&s_read_view);
                if (ok && s_read_view.text_kind == AGENT_TEXT_DRAFT) ok = poll_full_draft(&s_read_view);
                else if (ok) ok = poll_turn(&s_read_view);
            } else if (!s_read_view.session_switching) {
                if (s_read_view.segments_loading) {
                    ok = poll_draft(&s_read_view);
                    agent_client_copy(&s_read_view);
                }
                if (ok && s_read_view.text_kind == AGENT_TEXT_DRAFT) ok = poll_full_draft(&s_read_view);
                else if (ok && (urgent || s_read_view.chat_loading || !s_read_view.chat_ready)) ok = poll_turn(&s_read_view);
                agent_client_copy(&s_read_view);
                if (ok && s_read_view.text_kind != AGENT_TEXT_DRAFT &&
                    s_read_view.turn.present && agent_message_active(s_read_view.message.state)) ok = watch_reply(&s_read_view);
            }
            if (!ok && !s_paused) {
                lock();
                const bool memory_error = strcmp(s_view.notice, "内存不足，请退出会话") == 0;
                const bool idle = s_view.capture == AGENT_IDLE;
                unlock();
                if (!memory_error) {
                    notice("读取失败，请重试");
                    if (++failures >= 3 && idle) {
                        memo_endpoint_t endpoint;
                        if (memo_discovery_endpoint(&endpoint)) memo_discovery_invalidate(&endpoint);
                        failures = 0;
                    }
                }
                s_resync = true;
            } else if (ok) {
                failures = 0;
                lock();
                if (strcmp(s_view.notice, "读取失败，请重试") == 0) {
                    s_view.notice[0] = '\0';
                    changed();
                }
                unlock();
            }
            s_reader_active = false;
        }
        ulTaskNotifyTake(pdTRUE, pdMS_TO_TICKS(400));
    }
}

static uint32_t draft_revision(const char *session)
{
    char path[120];
    snprintf(path, sizeof(path), "/sessions/%s/draft?limit=1", session);
    if (request(HTTP_METHOD_GET, path, NULL, s_operation_body, sizeof(s_operation_body)) != 200) return 0;
    uint32_t revision = 0;
    agent_parse_revision(s_operation_body, &revision);
    cJSON *root = cJSON_Parse(s_operation_body);
    const cJSON *total = cJSON_GetObjectItemCaseSensitive(root, "total");
    s_draft_total = cJSON_IsNumber(total) && total->valueint >= 0 && total->valueint <= 64 ? total->valueint : 0;
    cJSON_Delete(root);
    return revision;
}

static void refresh_recorded_draft(const char *session)
{
    if (draft_revision(session) == 0) return;
    const int offset = s_draft_total > 0 ? (s_draft_total - 1) / AGENT_PAGE_SIZE * AGENT_PAGE_SIZE : 0;
    char path[128];
    snprintf(path, sizeof(path), "/sessions/%s/draft?limit=4&offset=%d", session, offset);
    agent_segment_t items[AGENT_PAGE_SIZE] = {0};
    int count = 0, total = 0;
    uint32_t revision = 0;
    if (request(HTTP_METHOD_GET, path, NULL, s_operation_body, sizeof(s_operation_body)) != 200 ||
        !agent_parse_draft(s_operation_body, items, &count, &revision, &total)) return;
    lock();
    if (strcmp(s_view.session_id, session) == 0) {
        memcpy(s_view.segments, items, sizeof(items));
        s_view.segment_count = count;
        s_view.segment_total = total;
        s_view.segment_offset = s_view.segment_requested = offset;
        s_view.segments_loading = false;
        s_view.segments_more = offset + count < total;
        s_view.draft_revision = revision;
        changed();
    }
    unlock();
}

static bool upload_audio(const char *session, const char *segment, uint32_t revision)
{
    s_audio_started = false;
    char path[192];
    snprintf(path, sizeof(path), "/sessions/%s/segments/%s/audio?sample_rate=16000&replace=1&defer_commit=1&revision=%lu",
             session, segment, (unsigned long)revision);
    esp_http_client_handle_t client = http_client(path, HTTP_METHOD_POST);
    if (client == NULL) return false;
    esp_http_client_set_header(client, "Content-Type", "audio/pcm");
    bool ok = esp_http_client_open(client, -1) == ESP_OK;
    if (ok && s_held) ok = agent_capture_begin() == ESP_OK;
    else ok = false;
    if (ok) {
        s_audio_started = true;
        lock();
        s_view.capture = AGENT_RECORDING;
        changed();
        unlock();
    }
    size_t sent = 0;
    while (ok && !s_cancelled) {
        if (!s_held) agent_capture_end();
        const size_t length = agent_capture_read(s_audio_chunk, sizeof(s_audio_chunk), 80);
        if (length == 0) {
            if (!agent_capture_running()) break;
            continue;
        }
        char prefix[16];
        const int prefix_size = snprintf(prefix, sizeof(prefix), "%x\r\n", (unsigned)length);
        ok = write_all(client, prefix, prefix_size) && write_all(client, s_audio_chunk, length) && write_all(client, "\r\n", 2);
        sent += length;
    }
    agent_capture_end();
    while (agent_capture_running()) vTaskDelay(pdMS_TO_TICKS(10));
    if (agent_capture_error() != ESP_OK || sent < 3200 || s_cancelled) ok = false;
    lock();
    s_view.capture = AGENT_FINALIZING;
    changed();
    unlock();
    uint32_t commit_revision = 0;
    if (ok) {
        esp_http_client_set_timeout_ms(client, 30000);
        int status = 0;
        if (!s_cancelled && write_all(client, "0\r\n\r\n", 5))
            status = read_response(client, s_operation_body, sizeof(s_operation_body));
        ESP_LOGI(TAG, "audio response=%d bytes=%u network_stack=%u", status, (unsigned)sent,
                 (unsigned)uxTaskGetStackHighWaterMark(NULL));
        ok = status == 200 && agent_parse_revision(s_operation_body, &commit_revision) && commit_revision > 0;
    }
    // Release the upload client before allocating the commit request client.
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    lock();
    ok = ok && !s_cancelled;
    if (ok) {
        s_view.capture = AGENT_COMMITTING;
        changed();
    }
    unlock();
    if (!ok) return false;
    snprintf(path, sizeof(path), "/sessions/%s/segments/%s/commit", session, segment);
    char body[48];
    snprintf(body, sizeof(body), "{\"revision\":%lu}", (unsigned long)commit_revision);
    int status = request(HTTP_METHOD_POST, path, body, s_operation_body, sizeof(s_operation_body));
    if (status == 0)
        status = request(HTTP_METHOD_POST, path, body, s_operation_body, sizeof(s_operation_body));
    return status == 200;
}

static bool resolve_recording(const char *session, const char *segment, bool *committed)
{
    char path[128];
    snprintf(path, sizeof(path), "/sessions/%s/segments/%s/abort", session, segment);
    const int status = request(HTTP_METHOD_POST, path, NULL, s_operation_body, sizeof(s_operation_body));
    cJSON *root = cJSON_Parse(s_operation_body);
    const cJSON *result = cJSON_GetObjectItemCaseSensitive(root, "status");
    const bool saved = status == 200 && cJSON_IsString(result) && strcmp(result->valuestring, "committed") == 0;
    const bool cancelled = status == 200 && cJSON_IsString(result) && strcmp(result->valuestring, "cancelled") == 0;
    cJSON_Delete(root);
    *committed = saved;
    return saved || cancelled || status == 404;
}

static void name_session(const char *session, const char *segment)
{
    char path[160], title[96];
    snprintf(path, sizeof(path), "/sessions/%s/segments/%s/text?limit=48", session, segment);
    agent_text_t text;
    if (request(HTTP_METHOD_GET, path, NULL, s_operation_body, sizeof(s_operation_body)) != 200 ||
        !agent_parse_text(s_operation_body, &text) || text.text[0] == '\0') return;
    for (char *p = text.text; *p != '\0'; p++) if ((unsigned char)*p < 32) *p = ' ';
    agent_copy_text(title, sizeof(title), text.text);
    cJSON *root = cJSON_CreateObject();
    if (root == NULL || cJSON_AddStringToObject(root, "title", title) == NULL) { cJSON_Delete(root); return; }
    char *body = cJSON_PrintUnformatted(root);
    cJSON_Delete(root);
    if (body == NULL) return;
    snprintf(path, sizeof(path), "/sessions/%s", session);
    const int status = request(HTTP_METHOD_PATCH, path, body, s_operation_body, sizeof(s_operation_body));
    cJSON_free(body);
    if (status == 200) {
        lock();
        if (strcmp(s_view.session_id, session) == 0) {
            agent_copy_text(s_view.title, sizeof(s_view.title), title);
            changed();
        }
        unlock();
    }
}

static void record(const command_t *command)
{
    char session[37], segment[37], title[64];
    agent_copy_text(session, sizeof(session), command->id);
    const bool create = command->enabled;
    bool prepared = false;
    bool audio_kept = false;
    bool created = false;
    s_audio_started = false;
    if (!s_held) { notice(""); goto finished; }
    if (!connected()) { notice("请先连接服务器"); goto finished; }
    if (create) {
        uuid(session);
        agent_copy_text(title, sizeof(title), "新会话");
        char body[192];
        snprintf(body, sizeof(body), "{\"id\":\"%s\",\"title\":\"%s\"}", session, title);
        created = true;
        const int status = request(HTTP_METHOD_POST, "/sessions", body, s_operation_body, sizeof(s_operation_body));
        ESP_LOGI(TAG, "create session response=%d", status);
        if (status != 201) {
            notice(status == 202 ? "会话暂未就绪，请重试" : http_notice(status));
            goto finished;
        }
        lock();
        agent_copy_text(s_view.session_id, sizeof(s_view.session_id), session);
        agent_copy_text(s_view.title, sizeof(s_view.title), title);
        s_view.selection++;
        clear_message_cache();
        s_view.turn = (agent_turn_t){0};
        s_view.text_generation++;
        s_view.session_switching = false;
        s_view.segment_offset = 0;
        s_view.segment_requested = 0;
        s_view.segment_count = 0;

        s_view.chat_ready = s_view.chat_sending = false;
        s_view.chat_loading = true;

        s_view.turn_anchor[0] = '\0';
        s_view.chat_request++;

        s_view.text_kind = AGENT_TEXT_REPLY;
        s_view.text_loading = false;
        memset(&s_view.text, 0, sizeof(s_view.text));
        s_view.text_request++;
        s_view.text_id[0] = '\0';
        changed();
        unlock();
    }
    if (!s_held) goto finished;
    uint32_t revision = draft_revision(session);
    if (revision == 0) { notice("读取草稿失败"); goto finished; }
    uuid(segment);
    char path[160], body[160];
    snprintf(path, sizeof(path), "/sessions/%s/segments", session);
    snprintf(body, sizeof(body), "{\"id\":\"%s\",\"revision\":%lu,\"replace\":true}", segment, (unsigned long)revision);
    int status = request(HTTP_METHOD_POST, path, body, s_operation_body, sizeof(s_operation_body));
    if (status == 0) status = request(HTTP_METHOD_POST, path, body, s_operation_body, sizeof(s_operation_body));
    if (status != 201 || !agent_parse_revision(s_operation_body, &revision)) {
        bool committed = false;
        resolve_recording(session, segment, &committed);
        notice(http_notice(status));
        goto finished;
    }
    prepared = true;
    lock();
    agent_copy_text(s_view.text_id, sizeof(s_view.text_id), segment);
    s_view.text_kind = AGENT_TEXT_SEGMENT;
    memset(&s_view.text, 0, sizeof(s_view.text));
    s_view.text_request++;
    changed();
    unlock();
    bool saved = s_held && upload_audio(session, segment, revision);
    bool resolved = saved;
    if (!saved && s_audio_started) resolved = resolve_recording(session, segment, &saved);
    ESP_LOGI(TAG, "record result saved=%d resolved=%d started=%d cancelled=%d capture_error=%d network_stack=%u",
             saved, resolved, s_audio_started, (bool)s_cancelled, (int)agent_capture_error(),
             (unsigned)uxTaskGetStackHighWaterMark(NULL));
    if (saved) {
        audio_kept = true;
        if (create || strcmp(command->title, "新会话") == 0) name_session(session, segment);
        notice("");
    } else if (!s_audio_started) {
        revision = draft_revision(session);
        snprintf(path, sizeof(path), "/sessions/%s/segments/%s", session, segment);
        snprintf(body, sizeof(body), "{\"revision\":%lu}", (unsigned long)revision);
        request(HTTP_METHOD_DELETE, path, body, s_operation_body, sizeof(s_operation_body));
        notice(!s_cancelled && s_held ? "无法开始录音" : "");
    } else if (s_cancelled) {
        notice("");
    } else {
        // An unresolved commit may already have saved a new session's message.
        audio_kept = !resolved;
        notice(resolved ? "转写失败，请重新录音" : "结果待确认，请稍后重试");
    }
finished:
    if (prepared) {
        refresh_recorded_draft(session);
        lock();
        if (strcmp(s_view.session_id, session) == 0) {
            s_view.text_kind = AGENT_TEXT_DRAFT;
            agent_copy_text(s_view.text_id, sizeof(s_view.text_id), session);
            memset(&s_view.text, 0, sizeof(s_view.text));
            s_view.text_request++;
            s_view.text_loading = true;
            s_view.text_generation++;
            if (s_view.segment_total == 0) s_view.text_id[0] = '\0';
            if (s_view.segment_count == 1 && strcmp(s_view.segments[0].id, segment) == 0 &&
                strcmp(s_view.segments[0].state, "ready") == 0 && s_view.segments[0].bytes > 0) {
                audio_kept = true;
                s_view.notice[0] = '\0';
            }
            changed();
        }
        unlock();
    }
    if (created && !audio_kept) {
        char cleanup[96];
        snprintf(cleanup, sizeof(cleanup), "/sessions/%s", session);
        request(HTTP_METHOD_DELETE, cleanup, NULL, s_operation_body, sizeof(s_operation_body));
    }
    lock();
    s_view.capture = AGENT_IDLE;
    if (strcmp(s_view.notice, "正在取消") == 0) s_view.notice[0] = '\0';
    if (prepared && audio_kept) s_view.capture_serial++;
    changed();
    unlock();
    s_resync = true;
}

static bool pause_reader(void)
{
    s_paused = true;
    const int64_t deadline = esp_timer_get_time() + 15000000;
    while (s_reader_active) {
        if (esp_timer_get_time() >= deadline) { s_paused = false; return false; }
        vTaskDelay(pdMS_TO_TICKS(20));
    }
    return true;
}

static void provision(bool enabled, bool pause_only)
{
    if (enabled) {
        if (!pause_reader()) { notice("网络忙，请重试"); return; }
        lock();
        clear_message_cache();
        s_view.turn = (agent_turn_t){0};
        s_view.chat_ready = false;
        s_view.chat_loading = true;
        s_view.text_generation++;
        unlock();
        memo_discovery_stop();
        esp_err_t err = memo_net_suspend();
        if (err == ESP_OK && !pause_only) err = memo_provision_start();
        lock();
        s_view.provisioning = !pause_only;
        s_view.paused = true;
        s_view.connected = false;
        changed();
        unlock();
        if (err != ESP_OK) notice("网络切换失败，请重试");
    } else {
        if (memo_provision_stop() != ESP_OK) { notice("网络释放失败，请重试"); return; }
        const esp_err_t network = memo_net_start();
        const esp_err_t discovery = memo_discovery_init();
        if (network != ESP_OK || discovery != ESP_OK) { notice("网络启动失败，请重试"); return; }
        lock();
        s_view.provisioning = s_view.paused = false;
        changed();
        unlock();
        s_paused = false;
        s_resync = true;
    }
}

static void execute(const command_t *command)
{
    if (command->kind == CMD_REFRESH) { discover_server(); return; }
    if (command->kind == CMD_PROVISION) { provision(command->enabled, false); return; }
    if (command->kind == CMD_PAUSE) { provision(true, true); return; }
    if (command->kind == CMD_RECORD) { record(command); return; }
    if (command->kind == CMD_OPEN || command->kind == CMD_CLOSE) {
        lock();
        clear_message_cache();
        s_view.turn = (agent_turn_t){0};
        s_view.text_generation++;
        agent_copy_text(s_view.session_id, sizeof(s_view.session_id), command->kind == CMD_OPEN ? command->id : "");
        agent_copy_text(s_view.title, sizeof(s_view.title), command->title);
        s_view.selection++;
        s_view.session_switching = false;
        s_view.segment_offset = 0;
        s_view.segment_requested = 0;
        s_view.segments_loading = command->kind == CMD_OPEN;
        s_view.session_requested = s_view.session_offset = 0;
        s_view.segment_count = 0;

        s_view.chat_ready = s_view.chat_sending = false;
        s_view.chat_loading = true;

        s_view.turn_anchor[0] = '\0';
        s_view.chat_request++;

        s_view.text_kind = AGENT_TEXT_REPLY;
        s_view.text_loading = false;
        s_view.segment_total = 0;
        s_view.text_id[0] = '\0';
        memset(&s_view.text, 0, sizeof(s_view.text));
        s_view.text_request++;
        memset(&s_view.message, 0, sizeof(s_view.message));
        s_pending_message[0] = '\0';
        changed();
        unlock();
        s_resync = true;
        return;
    }
    if (command->kind == CMD_REVIEW) {
        lock();
        if (command->selection == s_view.selection) {
            agent_copy_text(s_view.text_id, sizeof(s_view.text_id), s_view.session_id);
            s_view.text_kind = AGENT_TEXT_DRAFT;
            s_view.text_request++;
            s_view.text_loading = true;
            s_view.text_generation++;
            changed();
        }
        unlock();
        s_resync = true;
        return;
    }
    char session[37], path[160], body[192];
    lock();
    agent_copy_text(session, sizeof(session), s_view.session_id);
    const bool selected = command->selection == s_view.selection;
    unlock();
    if (!selected || !connected()) { notice("请先连接服务器"); return; }
    if (command->kind == CMD_SEND) {
        if (s_pending_message[0] == '\0' || s_pending_revision != command->revision) {
            uuid(s_pending_message);
            s_pending_revision = command->revision;
        }
        snprintf(path, sizeof(path), "/sessions/%s/messages", session);
        snprintf(body, sizeof(body), "{\"id\":\"%s\",\"revision\":%lu}", s_pending_message, (unsigned long)command->revision);
        const int status = request(HTTP_METHOD_POST, path, body, s_operation_body, sizeof(s_operation_body));
        agent_message_t message;
        if (status == 202 && agent_parse_message(s_operation_body, &message)) {
            lock();
            s_view.message = message;
            s_view.turn = (agent_turn_t){.message = message, .present = true};
            agent_copy_text(s_cached_message, sizeof(s_cached_message), message.id);
            s_cached_draft_revision = 0;
            free(s_reply_text);
            s_reply_text = NULL;
            s_reply_capacity = 0;
            s_reply_loaded = true;
            s_view.text_generation++;
            agent_copy_text(s_view.text_id, sizeof(s_view.text_id), message.id);
            s_view.text_kind = AGENT_TEXT_REPLY;
            s_view.text_loading = false;
            s_view.text_request++;
            s_view.chat_sending = s_view.chat_loading = true;

            s_view.turn_anchor[0] = '\0';
            s_view.chat_request++;
            s_view.segment_offset = 0;
            s_view.segment_requested = 0;
            s_view.segment_count = s_view.segment_total = 0;
            changed();
            unlock();
            notice("");
        } else notice(http_notice(status));
    } else if (command->kind == CMD_RETRY) {
        snprintf(path, sizeof(path), "/sessions/%s/messages/%s/retry", session, command->id);
        const int status = request(HTTP_METHOD_POST, path, NULL, s_operation_body, sizeof(s_operation_body));
        agent_message_t message;
        if (status == 200 && agent_parse_message(s_operation_body, &message) &&
            strcmp(message.id, command->id) == 0 && message.state[0] != '\0') {
            lock();
            s_view.message = message;
            s_view.chat_sending = false;
            s_view.chat_loading = true;

            s_view.turn_anchor[0] = '\0';
            s_view.chat_request++;
            if (s_view.capture == AGENT_IDLE &&
                (s_view.text_kind == AGENT_TEXT_REPLY || s_view.text_kind == AGENT_TEXT_INPUT)) {
                agent_copy_text(s_view.text_id, sizeof(s_view.text_id), message.id);
                memset(&s_view.text, 0, sizeof(s_view.text));
                s_view.text_loading = false;
                s_view.text_request++;
            }
            changed();
            unlock();
            notice("");
        } else notice(http_notice(status));
    }
    s_resync = true;
}

static void network_task(void *arg)
{
    (void)arg;
    int64_t last_search = -10000000;
    command_t command;
    for (;;) {
        if (xQueueReceive(s_commands, &command, pdMS_TO_TICKS(250)) == pdTRUE) {
            lock(); s_view.busy = true; changed(); unlock();
            execute(&command);
            if (s_sync_task != NULL) xTaskNotifyGive(s_sync_task);
            lock(); s_view.busy = uxQueueMessagesWaiting(s_commands) > 0; changed(); unlock();
        }
        const bool online = connected();
        lock();
        if (s_view.connected != online) { s_view.connected = online; changed(); }
        unlock();
        const int64_t now = esp_timer_get_time();
        if (!s_paused && !online && memo_net_has_ip() && now - last_search > 10000000) {
            discover_server();
            last_search = now;
        }
    }
}

static esp_err_t queue(command_t *command)
{
    if (s_commands == NULL) return ESP_ERR_INVALID_STATE;
    lock();
    if (command->selection != 0 && command->selection != s_view.selection) {
        unlock();
        return ESP_ERR_INVALID_STATE;
    }
    command->selection = s_view.selection;
    const bool queued = xQueueSend(s_commands, command, 0) == pdTRUE;
    if (queued) {
        if (command->kind == CMD_REVIEW) s_view.text_request++;
        s_view.busy = true;
        if (command->kind == CMD_OPEN) {
            s_view.session_switching = true;
            s_view.chat_ready = false;
            agent_copy_text(s_view.title, sizeof(s_view.title), command->title);
        }
        changed();
    }
    unlock();
    return queued ? ESP_OK : ESP_ERR_NO_MEM;
}

esp_err_t agent_client_init(void)
{
    if (s_commands != NULL) return ESP_ERR_INVALID_STATE;
    s_lock = xSemaphoreCreateMutex();
    s_commands = xQueueCreate(6, sizeof(command_t));
    if (s_lock == NULL || s_commands == NULL) return ESP_ERR_NO_MEM;
    if (xTaskCreate(network_task, "agent_net", 6144, NULL, 4, NULL) != pdPASS) return ESP_ERR_NO_MEM;
    if (xTaskCreate(sync_task, "agent_sync", 5120, NULL, 3, &s_sync_task) != pdPASS) return ESP_ERR_NO_MEM;
    ESP_LOGI(TAG, "bounded HTTP gateway client ready");
    return ESP_OK;
}

esp_err_t agent_client_refresh(void) { command_t c = {.kind = CMD_REFRESH}; return queue(&c); }
bool agent_client_discovery_pending(void) { return s_discover; }
static esp_err_t page_request(int direction, int *requested, int current, bool *loading, bool more)
{
    if (*loading || s_view.busy || (direction < 0 && current == 0) || (direction > 0 && !more)) return ESP_ERR_INVALID_STATE;
    *requested = direction == 0 ? 0 : current + direction * AGENT_PAGE_SIZE;
    *loading = true;
    changed();
    s_resync = true;
    return ESP_OK;
}
esp_err_t agent_client_sessions_page(int direction)
{
    lock();
    const esp_err_t err = page_request(direction, &s_view.session_requested, s_view.session_offset, &s_view.sessions_loading, s_view.sessions_more);
    unlock();
    return err;
}
void agent_client_message_select(bool input)
{
    lock();
    if (s_view.turn.present && s_view.capture == AGENT_IDLE && !s_view.chat_loading) {
        const agent_text_kind_t kind = input ? AGENT_TEXT_INPUT : AGENT_TEXT_REPLY;
        if (s_view.text_kind != kind) {
            s_view.text_kind = kind;
            s_view.text_generation++;
            changed();
        }
    }
    unlock();
}

esp_err_t agent_client_history(int direction)
{
    lock();
    const char *id = direction < 0 ? s_view.turn.before : s_view.turn.after;
    if (s_view.capture != AGENT_IDLE || s_view.busy || s_view.chat_loading || id[0] == '\0') {
        unlock();
        return ESP_ERR_INVALID_STATE;
    }
    agent_copy_text(s_view.turn_anchor, sizeof(s_view.turn_anchor), id);
    agent_copy_text(s_view.text_id, sizeof(s_view.text_id), id);

    s_view.text_kind = direction < 0 ? AGENT_TEXT_REPLY : AGENT_TEXT_INPUT;
    s_view.chat_loading = s_view.text_loading = true;
    s_view.chat_request++;
    s_view.text_request++;
    s_view.text_generation++;
    changed();
    unlock();
    xTaskNotifyGive(s_sync_task);
    return ESP_OK;
}

esp_err_t agent_client_open(const agent_session_t *session)
{
    if (session == NULL || !agent_valid_id(session->id)) return ESP_ERR_INVALID_ARG;
    command_t c = {.kind = CMD_OPEN};
    agent_copy_text(c.id, sizeof(c.id), session->id);
    agent_copy_text(c.title, sizeof(c.title), session->title);
    return queue(&c);
}
esp_err_t agent_client_close_session(void) { command_t c = {.kind = CMD_CLOSE}; return queue(&c); }
esp_err_t agent_client_record(bool new_session)
{
    command_t c = {.kind = CMD_RECORD, .enabled = new_session};
    lock();
    if (s_view.busy || s_view.capture != AGENT_IDLE || !s_view.connected || s_view.paused ||
        (!new_session && s_view.session_id[0] == '\0')) { unlock(); return ESP_ERR_INVALID_STATE; }
    agent_copy_text(c.id, sizeof(c.id), s_view.session_id);
    agent_copy_text(c.title, sizeof(c.title), s_view.title);
    s_view.capture = AGENT_PREPARING;
    s_cancelled = false;
    s_view.text_request++;
    s_view.notice[0] = '\0';
    changed();
    unlock();
    s_held = true;
    const esp_err_t err = queue(&c);
    if (err != ESP_OK) {
        lock(); s_view.capture = AGENT_IDLE; changed(); unlock();
        s_held = false;
    }
    return err;
}
void agent_client_release_record(void)
{
    s_held = false;
    agent_capture_end();
    lock();
    if (s_view.capture == AGENT_PREPARING) {
        agent_copy_text(s_view.notice, sizeof(s_view.notice), "正在取消");
        changed();
    }
    unlock();
}
bool agent_client_cancel_record(void)
{
    lock();
    const bool accepted = s_view.capture != AGENT_IDLE && s_view.capture != AGENT_COMMITTING;
    if (accepted) {
        s_cancelled = true;
        s_held = false;
        changed();
    }
    unlock();
    if (accepted) agent_capture_end();
    return accepted;
}
esp_err_t agent_client_send(uint32_t revision, uint32_t selection)
{
    lock();
    const bool ready = selection != 0 && selection == s_view.selection && !s_view.busy &&
        s_view.capture == AGENT_IDLE && s_view.segment_total > 0 &&
        s_view.text_kind == AGENT_TEXT_DRAFT && s_view.text.revision == revision &&
        s_view.draft_revision == revision && s_view.text.total > 0;
    unlock();
    if (!ready) return ESP_ERR_INVALID_STATE;
    command_t c = {.kind = CMD_SEND, .revision = revision, .selection = selection};
    return queue(&c);
}
esp_err_t agent_client_review_draft(void)
{
    command_t command = {.kind = CMD_REVIEW};
    return queue(&command);
}

esp_err_t agent_client_retry(const char *id, uint32_t selection)
{
    if (selection == 0 || !agent_valid_id(id)) return ESP_ERR_INVALID_ARG;
    command_t c = {.kind = CMD_RETRY, .selection = selection};
    agent_copy_text(c.id, sizeof(c.id), id);
    return agent_valid_id(c.id) ? queue(&c) : ESP_ERR_INVALID_STATE;
}
esp_err_t agent_client_provision(bool enabled) { command_t c = {.kind = CMD_PROVISION, .enabled = enabled}; return queue(&c); }
esp_err_t agent_client_pause(void) { command_t c = {.kind = CMD_PAUSE}; return queue(&c); }

