#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

// Diagnostic trace: a bounded ring of recent device events plus recent
// warning/error log lines. It lives in RTC memory on the device, so the record
// survives panics and watchdog resets, and is uploaded to the gateway when
// something goes wrong. It never holds audio, transcripts or credentials.

typedef enum {
    TRACE_BOOT = 1,    // a: reset reason, b: boot count
    TRACE_KEY,         // a: button, b: driver event (press/release/long)
    TRACE_GESTURE,     // a: button, b: 1 hold / 0 click, s: page
    TRACE_PAGE,        // a: page
    TRACE_CAPTURE,     // a: capture state
    TRACE_HTTP,        // a: status (0 = transport failure), b: elapsed ms, s: method and path tag
    TRACE_AUDIO,       // a: response status, b: uploaded PCM bytes
    TRACE_RECORD,      // a: flags (1 saved, 2 resolved, 4 started, 8 cancelled), b: capture error
    TRACE_SEND,        // b: reviewed draft revision
    TRACE_HEAP,        // a: largest free block / 1024, b: free bytes
    TRACE_REPORT,      // a: trigger, b: 1 uploaded / 0 failed
    TRACE_CAPTURE_ERROR, // b: esp_err_t from audio capture
    TRACE_SPEECH,      // a: end status (0 done, 1 failed, 2 stopped, 3 limit), b: 0 (earlier builds: last sentence index), s: underruns, gap, resumes
    TRACE_VOICE,       // a: voice mode shown
    TRACE_WIFI,        // a: disconnect reason, b: RSSI
} agent_trace_type_t;

typedef enum {
    TRACE_TRIGGER_NONE = 0,
    TRACE_TRIGGER_ABNORMAL_RESET,
    TRACE_TRIGGER_RECORD_FAILED,
    TRACE_TRIGGER_CAPTURE_ERROR,
    TRACE_TRIGGER_SEND_AFTER_FAILURE,
    TRACE_TRIGGER_MANUAL,
} agent_trace_trigger_t;

#define AGENT_TRACE_ENTRIES 160
#define AGENT_TRACE_LOG_BYTES 1536
#define AGENT_TRACE_TAG_BYTES 12

typedef struct {
    uint32_t ms;     // Milliseconds since that boot.
    uint8_t boot;    // Low byte of the boot count the entry belongs to.
    uint8_t type;
    uint16_t a;
    int32_t b;
    char s[AGENT_TRACE_TAG_BYTES];
} agent_trace_entry_t;

typedef struct {
    uint32_t magic;
    uint32_t boots;
    uint32_t head;   // Next entry slot.
    uint32_t count;
    uint32_t log_head;
    uint32_t log_used;
    uint32_t check;
    agent_trace_entry_t entries[AGENT_TRACE_ENTRIES];
    char log[AGENT_TRACE_LOG_BYTES];
} agent_trace_store_t;

// Pure core, host-testable. `store` is the retained ring.
void agent_trace_core_reset(agent_trace_store_t *store);
bool agent_trace_core_valid(const agent_trace_store_t *store);
void agent_trace_core_seal(agent_trace_store_t *store);
void agent_trace_core_add(agent_trace_store_t *store, uint32_t ms, uint8_t type, uint16_t a, int32_t b, const char *s);
void agent_trace_core_log(agent_trace_store_t *store, const char *line, size_t length);
void agent_trace_path_tag(const char *method, const char *path, char out[AGENT_TRACE_TAG_BYTES]);

typedef bool (*agent_trace_write_t)(void *context, const char *data, size_t length);
typedef struct {
    const char *firmware;
    const char *build;
    uint32_t reset_reason;
    uint32_t uptime_ms;
    uint32_t heap_free;
    uint32_t heap_minimum;
    uint32_t heap_largest;
    agent_trace_trigger_t trigger;
} agent_trace_meta_t;
// Rotates the log ring so its oldest byte comes first.
void agent_trace_core_linearize(agent_trace_store_t *store);
// Streams one JSON report from a snapshot (linearizing its log first); strings
// are escaped and invalid UTF-8 is replaced.
bool agent_trace_core_encode(agent_trace_store_t *store, const agent_trace_meta_t *meta,
                             agent_trace_write_t write, void *context);
const char *agent_trace_trigger_name(agent_trace_trigger_t trigger);

// Device API (thread-safe, non-blocking, callable from any task).
void agent_trace_init(void);
void agent_trace(agent_trace_type_t type, uint16_t a, int32_t b, const char *s);
void agent_trace_request(agent_trace_trigger_t trigger);
agent_trace_trigger_t agent_trace_pending(void);
// Copies the ring under the lock so it can be encoded without holding it.
void agent_trace_snapshot(agent_trace_store_t *out);
// Records an attempt; the pending trigger is cleared once uploaded or given up.
void agent_trace_reported(agent_trace_trigger_t trigger, bool uploaded, bool clear);
// Fills firmware identity, reset reason, uptime and heap figures.
void agent_trace_meta(agent_trace_meta_t *meta, agent_trace_trigger_t trigger);
