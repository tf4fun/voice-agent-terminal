#ifndef ESP_PLATFORM
#define _POSIX_C_SOURCE 200809L // strnlen on host builds
#endif

#include "agent_trace.h"

#include <stdio.h>
#include <string.h>

#define TRACE_MAGIC 0x41545231u // "ATR1"

static uint32_t checksum(const agent_trace_store_t *store)
{
    // Covers the header fields; entry contents are tolerated as best effort.
    return (store->magic * 2654435761u) ^ (store->boots * 40503u) ^ (store->head << 7) ^
           (store->count << 15) ^ (store->log_head << 3) ^ (store->log_used << 21) ^ 0x5bd1e995u;
}

void agent_trace_core_reset(agent_trace_store_t *store)
{
    memset(store, 0, sizeof(*store));
    store->magic = TRACE_MAGIC;
    agent_trace_core_seal(store);
}

bool agent_trace_core_valid(const agent_trace_store_t *store)
{
    return store->magic == TRACE_MAGIC && store->head < AGENT_TRACE_ENTRIES &&
           store->count <= AGENT_TRACE_ENTRIES && store->log_head < AGENT_TRACE_LOG_BYTES &&
           store->log_used <= AGENT_TRACE_LOG_BYTES && store->check == checksum(store);
}

void agent_trace_core_seal(agent_trace_store_t *store) { store->check = checksum(store); }

void agent_trace_core_add(agent_trace_store_t *store, uint32_t ms, uint8_t type, uint16_t a, int32_t b, const char *s)
{
    agent_trace_entry_t *entry = &store->entries[store->head];
    entry->ms = ms;
    entry->boot = (uint8_t)store->boots;
    entry->type = type;
    entry->a = a;
    entry->b = b;
    memset(entry->s, 0, sizeof(entry->s));
    if (s != NULL) strncpy(entry->s, s, sizeof(entry->s) - 1);
    store->head = (store->head + 1) % AGENT_TRACE_ENTRIES;
    if (store->count < AGENT_TRACE_ENTRIES) store->count++;
    agent_trace_core_seal(store);
}

void agent_trace_core_log(agent_trace_store_t *store, const char *line, size_t length)
{
    if (length >= AGENT_TRACE_LOG_BYTES) {
        line += length - (AGENT_TRACE_LOG_BYTES - 1);
        length = AGENT_TRACE_LOG_BYTES - 1;
    }
    for (size_t i = 0; i < length; i++) {
        store->log[store->log_head] = line[i];
        store->log_head = (store->log_head + 1) % AGENT_TRACE_LOG_BYTES;
    }
    store->log_used = store->log_used + length > AGENT_TRACE_LOG_BYTES ? AGENT_TRACE_LOG_BYTES : store->log_used + length;
    agent_trace_core_seal(store);
}

static bool uuid_like(const char *start, size_t length)
{
    if (length != 36) return false;
    for (size_t i = 0; i < length; i++) {
        const char c = start[i];
        if (i == 8 || i == 13 || i == 18 || i == 23) { if (c != '-') return false; }
        else if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) return false;
    }
    return true;
}

// "POST /sessions/<id>/segments/<id>/audio?..." becomes "P audio".
void agent_trace_path_tag(const char *method, const char *path, char out[AGENT_TRACE_TAG_BYTES])
{
    const char *end = path + strcspn(path, "?");
    const char *name = "";
    size_t name_length = 0;
    const char *part = path;
    while (part < end) {
        while (part < end && *part == '/') part++;
        const char *next = part;
        while (next < end && *next != '/') next++;
        if (next > part && !uuid_like(part, (size_t)(next - part))) { name = part; name_length = (size_t)(next - part); }
        part = next;
    }
    const size_t room = AGENT_TRACE_TAG_BYTES - 3;
    if (name_length > room) name_length = room;
    out[0] = method != NULL && method[0] != '\0' ? method[0] : '?';
    out[1] = ' ';
    memcpy(out + 2, name, name_length);
    out[2 + name_length] = '\0';
}

const char *agent_trace_trigger_name(agent_trace_trigger_t trigger)
{
    switch (trigger) {
    case TRACE_TRIGGER_ABNORMAL_RESET: return "abnormal_reset";
    case TRACE_TRIGGER_RECORD_FAILED: return "record_failed";
    case TRACE_TRIGGER_CAPTURE_ERROR: return "capture_error";
    case TRACE_TRIGGER_SEND_AFTER_FAILURE: return "send_after_failure";
    case TRACE_TRIGGER_MANUAL: return "manual";
    default: return "none";
    }
}

typedef struct {
    agent_trace_write_t write;
    void *context;
    char buffer[256];
    size_t used;
    bool ok;
} writer_t;

static void flush(writer_t *w)
{
    if (w->ok && w->used > 0) w->ok = w->write(w->context, w->buffer, w->used);
    w->used = 0;
}

static void put(writer_t *w, const char *data, size_t length)
{
    while (w->ok && length > 0) {
        size_t room = sizeof(w->buffer) - w->used;
        if (room == 0) { flush(w); continue; }
        const size_t take = length < room ? length : room;
        memcpy(w->buffer + w->used, data, take);
        w->used += take;
        data += take;
        length -= take;
    }
}

static void puts_raw(writer_t *w, const char *text) { put(w, text, strlen(text)); }

static size_t utf8_sequence(const unsigned char *s, size_t length)
{
    if (s[0] < 0x80) return 1;
    size_t need;
    uint32_t min;
    if ((s[0] & 0xe0) == 0xc0) { need = 2; min = 0x80; }
    else if ((s[0] & 0xf0) == 0xe0) { need = 3; min = 0x800; }
    else if ((s[0] & 0xf8) == 0xf0) { need = 4; min = 0x10000; }
    else return 0;
    if (length < need) return 0;
    uint32_t code = s[0] & (0x7f >> need);
    for (size_t i = 1; i < need; i++) {
        if ((s[i] & 0xc0) != 0x80) return 0;
        code = (code << 6) | (s[i] & 0x3f);
    }
    if (code < min || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff)) return 0;
    return need;
}

static void put_string(writer_t *w, const char *text, size_t length)
{
    put(w, "\"", 1);
    const unsigned char *s = (const unsigned char *)text;
    size_t i = 0;
    while (i < length) {
        const unsigned char c = s[i];
        if (c == '"' || c == '\\') { char escaped[2] = {'\\', (char)c}; put(w, escaped, 2); i++; }
        else if (c == '\n') { put(w, "\\n", 2); i++; }
        else if (c < 0x20 || c == 0x7f) { char escaped[8]; snprintf(escaped, sizeof(escaped), "\\u%04x", c); put(w, escaped, 6); i++; }
        else if (c < 0x80) { put(w, (const char *)s + i, 1); i++; }
        else {
            const size_t n = utf8_sequence(s + i, length - i);
            if (n == 0) { put(w, "?", 1); i++; }
            else { put(w, (const char *)s + i, n); i += n; }
        }
    }
    put(w, "\"", 1);
}

static void put_uint(writer_t *w, const char *key, uint32_t value)
{
    char text[48];
    const int n = snprintf(text, sizeof(text), "\"%s\":%lu,", key, (unsigned long)value);
    put(w, text, (size_t)n);
}

static void reverse(char *text, size_t length)
{
    for (size_t i = 0, j = length; i + 1 < j; i++, j--) { const char c = text[i]; text[i] = text[j - 1]; text[j - 1] = c; }
}

void agent_trace_core_linearize(agent_trace_store_t *store)
{
    // Rotate in place so the oldest retained byte is first; no extra buffer.
    const uint32_t used = store->log_used;
    const uint32_t start = (store->log_head + AGENT_TRACE_LOG_BYTES - used) % AGENT_TRACE_LOG_BYTES;
    if (start != 0) {
        reverse(store->log, start);
        reverse(store->log + start, AGENT_TRACE_LOG_BYTES - start);
        reverse(store->log, AGENT_TRACE_LOG_BYTES);
    }
    store->log_head = used % AGENT_TRACE_LOG_BYTES;
    agent_trace_core_seal(store);
}

bool agent_trace_core_encode(agent_trace_store_t *store, const agent_trace_meta_t *meta,
                             agent_trace_write_t write, void *context)
{
    agent_trace_core_linearize(store);
    writer_t w = {.write = write, .context = context, .ok = true};
    puts_raw(&w, "{\"firmware\":");
    put_string(&w, meta->firmware, strlen(meta->firmware));
    puts_raw(&w, ",\"build\":");
    put_string(&w, meta->build, strlen(meta->build));
    puts_raw(&w, ",\"trigger\":");
    const char *trigger = agent_trace_trigger_name(meta->trigger);
    put_string(&w, trigger, strlen(trigger));
    puts_raw(&w, ",");
    put_uint(&w, "boot", store->boots);
    put_uint(&w, "reset_reason", meta->reset_reason);
    put_uint(&w, "uptime_ms", meta->uptime_ms);
    put_uint(&w, "heap_free", meta->heap_free);
    put_uint(&w, "heap_minimum", meta->heap_minimum);
    put_uint(&w, "heap_largest", meta->heap_largest);
    puts_raw(&w, "\"events\":[");
    const uint32_t first = (store->head + AGENT_TRACE_ENTRIES - store->count) % AGENT_TRACE_ENTRIES;
    for (uint32_t i = 0; i < store->count; i++) {
        const agent_trace_entry_t *e = &store->entries[(first + i) % AGENT_TRACE_ENTRIES];
        char text[80];
        const int n = snprintf(text, sizeof(text), "%s[%u,%lu,%u,%u,%ld,", i ? "," : "", (unsigned)e->boot,
                               (unsigned long)e->ms, (unsigned)e->type, (unsigned)e->a, (long)e->b);
        put(&w, text, (size_t)n);
        put_string(&w, e->s, strnlen(e->s, sizeof(e->s)));
        put(&w, "]", 1);
    }
    puts_raw(&w, "],\"logs\":");
    // The log ring is linearized oldest first; a wrapped ring starts at a line boundary.
    const char *ordered = store->log;
    const uint32_t used = store->log_used;
    size_t skip = 0;
    if (used == AGENT_TRACE_LOG_BYTES) {
        while (skip < used && ordered[skip] != '\n') skip++;
        if (skip < used) skip++;
    }
    put_string(&w, ordered + skip, used - skip);
    puts_raw(&w, "}");
    flush(&w);
    return w.ok;
}

#ifdef ESP_PLATFORM

#include <stdarg.h>
#include <stdatomic.h>

#include "esp_app_desc.h"
#include "esp_attr.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"

static RTC_NOINIT_ATTR agent_trace_store_t s_store;
static portMUX_TYPE s_lock = portMUX_INITIALIZER_UNLOCKED;
static _Atomic int s_pending;
static vprintf_like_t s_console;

static uint32_t now_ms(void) { return (uint32_t)(esp_timer_get_time() / 1000); }

// Mirrors W/E lines into the retained ring, then prints them as before.
static int trace_vprintf(const char *format, va_list args)
{
    const char *level = format;
    if (level[0] == '\033') { // Skip a color escape such as "\033[0;33m".
        const char *m = strchr(level, 'm');
        if (m != NULL) level = m + 1;
    }
    if (level[0] == 'W' || level[0] == 'E') {
        char line[112];
        va_list copy;
        va_copy(copy, args);
        int n = vsnprintf(line, sizeof(line), format, copy);
        va_end(copy);
        if (n > 0) {
            size_t length = (size_t)n < sizeof(line) ? (size_t)n : sizeof(line) - 1;
            // Drop color escapes and keep one line per record.
            size_t out = 0;
            for (size_t i = 0; i < length; i++) {
                if (line[i] == '\033') { while (i < length && line[i] != 'm') i++; continue; }
                line[out++] = line[i];
            }
            if (out == 0 || line[out - 1] != '\n') { if (out == sizeof(line) - 1) out--; line[out++] = '\n'; }
            portENTER_CRITICAL_SAFE(&s_lock);
            agent_trace_core_log(&s_store, line, out);
            portEXIT_CRITICAL_SAFE(&s_lock);
        }
    }
    return s_console != NULL ? s_console(format, args) : vprintf(format, args);
}

static bool abnormal(esp_reset_reason_t reason)
{
    return reason == ESP_RST_PANIC || reason == ESP_RST_INT_WDT || reason == ESP_RST_TASK_WDT ||
           reason == ESP_RST_WDT || reason == ESP_RST_BROWNOUT || reason == ESP_RST_CPU_LOCKUP;
}

void agent_trace_init(void)
{
    const esp_reset_reason_t reason = esp_reset_reason();
    portENTER_CRITICAL(&s_lock);
    if (!agent_trace_core_valid(&s_store) || reason == ESP_RST_POWERON) agent_trace_core_reset(&s_store);
    s_store.boots++;
    agent_trace_core_seal(&s_store);
    portEXIT_CRITICAL(&s_lock);
    agent_trace(TRACE_BOOT, (uint16_t)reason, (int32_t)s_store.boots, NULL);
    if (abnormal(reason)) agent_trace_request(TRACE_TRIGGER_ABNORMAL_RESET);
    s_console = esp_log_set_vprintf(trace_vprintf);
}

void agent_trace(agent_trace_type_t type, uint16_t a, int32_t b, const char *s)
{
    const uint32_t ms = now_ms();
    portENTER_CRITICAL_SAFE(&s_lock);
    agent_trace_core_add(&s_store, ms, (uint8_t)type, a, b, s);
    portEXIT_CRITICAL_SAFE(&s_lock);
}

void agent_trace_request(agent_trace_trigger_t trigger)
{
    int expected = TRACE_TRIGGER_NONE;
    atomic_compare_exchange_strong(&s_pending, &expected, (int)trigger);
}

agent_trace_trigger_t agent_trace_pending(void) { return (agent_trace_trigger_t)atomic_load(&s_pending); }

void agent_trace_snapshot(agent_trace_store_t *out)
{
    portENTER_CRITICAL(&s_lock);
    memcpy(out, &s_store, sizeof(*out));
    portEXIT_CRITICAL(&s_lock);
}

void agent_trace_reported(agent_trace_trigger_t trigger, bool uploaded, bool clear)
{
    if (clear) {
        int expected = (int)trigger;
        atomic_compare_exchange_strong(&s_pending, &expected, TRACE_TRIGGER_NONE);
    }
    agent_trace(TRACE_REPORT, (uint16_t)trigger, uploaded ? 1 : 0, NULL);
}

void agent_trace_meta(agent_trace_meta_t *meta, agent_trace_trigger_t trigger)
{
    static char build[17];
    if (build[0] == '\0') esp_app_get_elf_sha256(build, sizeof(build));
    *meta = (agent_trace_meta_t){
        .firmware = esp_app_get_description()->version,
        .build = build,
        .reset_reason = (uint32_t)esp_reset_reason(),
        .uptime_ms = now_ms(),
        .heap_free = (uint32_t)heap_caps_get_free_size(MALLOC_CAP_8BIT),
        .heap_minimum = (uint32_t)heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT),
        .heap_largest = (uint32_t)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT),
        .trigger = trigger,
    };
}

#else

// Host builds (unit tests): the same API over a plain, unlocked store.
static agent_trace_store_t s_store;
static int s_pending;
static uint32_t s_clock_ms;

void agent_trace_init(void) { agent_trace_core_reset(&s_store); s_store.boots = 1; agent_trace_core_seal(&s_store); }
void agent_trace(agent_trace_type_t type, uint16_t a, int32_t b, const char *s)
{
    if (!agent_trace_core_valid(&s_store)) agent_trace_init();
    agent_trace_core_add(&s_store, s_clock_ms++, (uint8_t)type, a, b, s);
}
void agent_trace_request(agent_trace_trigger_t trigger) { if (s_pending == TRACE_TRIGGER_NONE) s_pending = (int)trigger; }
agent_trace_trigger_t agent_trace_pending(void) { return (agent_trace_trigger_t)s_pending; }
void agent_trace_snapshot(agent_trace_store_t *out) { memcpy(out, &s_store, sizeof(*out)); }
void agent_trace_reported(agent_trace_trigger_t trigger, bool uploaded, bool clear)
{
    if (clear && s_pending == (int)trigger) s_pending = TRACE_TRIGGER_NONE;
    agent_trace(TRACE_REPORT, (uint16_t)trigger, uploaded ? 1 : 0, NULL);
}
void agent_trace_meta(agent_trace_meta_t *meta, agent_trace_trigger_t trigger)
{
    *meta = (agent_trace_meta_t){.firmware = "host", .build = "host", .uptime_ms = s_clock_ms, .trigger = trigger};
}

#endif
