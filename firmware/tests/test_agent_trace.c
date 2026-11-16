#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "agent_trace.h"
#include "cJSON.h"

typedef struct {
    char data[32768];
    size_t used;
    size_t calls;
    size_t fail_after;
} sink_t;

static bool sink(void *context, const char *data, size_t length)
{
    sink_t *s = context;
    if (s->fail_after && ++s->calls > s->fail_after) return false;
    assert(s->used + length < sizeof(s->data));
    memcpy(s->data + s->used, data, length);
    s->used += length;
    s->data[s->used] = '\0';
    return true;
}

static void test_tags(void)
{
    char tag[AGENT_TRACE_TAG_BYTES];
    agent_trace_path_tag("POST", "/sessions/0b5d6c2e-1f4a-4c4b-9a5e-2d2f1a0c9e11/segments/1b5d6c2e-1f4a-4c4b-9a5e-2d2f1a0c9e11/audio?sample_rate=16000", tag);
    assert(strcmp(tag, "P audio") == 0);
    agent_trace_path_tag("GET", "/sessions/0b5d6c2e-1f4a-4c4b-9a5e-2d2f1a0c9e11/draft/content", tag);
    assert(strcmp(tag, "G content") == 0);
    agent_trace_path_tag("GET", "/sessions/0b5d6c2e-1f4a-4c4b-9a5e-2d2f1a0c9e11", tag);
    assert(strcmp(tag, "G sessions") == 0);
    agent_trace_path_tag("POST", "/sessions/x/conversation-with-a-long-name", tag);
    assert(strlen(tag) == AGENT_TRACE_TAG_BYTES - 1 && strncmp(tag, "P conver", 8) == 0);
}

static void test_ring_and_encode(void)
{
    agent_trace_store_t *store = malloc(sizeof(*store));
    agent_trace_core_reset(store);
    assert(agent_trace_core_valid(store));
    store->boots = 7;
    agent_trace_core_seal(store);
    for (uint32_t i = 0; i < AGENT_TRACE_ENTRIES + 25; i++)
        agent_trace_core_add(store, i * 10, TRACE_KEY, (uint16_t)(i % 3), (int32_t)i, "tag\"\\");
    assert(store->count == AGENT_TRACE_ENTRIES);
    // Wrap the log ring several times; only whole recent lines survive.
    char line[64];
    for (int i = 0; i < 200; i++) {
        const int n = snprintf(line, sizeof(line), "W (%d) agent: line %d\n", i * 10, i);
        agent_trace_core_log(store, line, (size_t)n);
    }
    // Invalid UTF-8 and control bytes must still yield valid JSON.
    const char bad[] = "E (1) x: \xff\xfe bad \xe4\xbd\xa0\x01ok\n";
    agent_trace_core_log(store, bad, sizeof(bad) - 1);
    assert(agent_trace_core_valid(store));
    sink_t *out = calloc(1, sizeof(*out));
    const agent_trace_meta_t meta = {.firmware = "0.2.0", .build = "abc123", .reset_reason = 4, .uptime_ms = 99999,
                                     .heap_free = 80000, .heap_minimum = 65000, .heap_largest = 60000,
                                     .trigger = TRACE_TRIGGER_RECORD_FAILED};
    assert(agent_trace_core_encode(store, &meta, sink, out));
    cJSON *root = cJSON_Parse(out->data);
    assert(root != NULL);
    assert(strcmp(cJSON_GetObjectItem(root, "trigger")->valuestring, "record_failed") == 0);
    assert(cJSON_GetObjectItem(root, "boot")->valueint == 7);
    const cJSON *events = cJSON_GetObjectItem(root, "events");
    assert(cJSON_GetArraySize(events) == AGENT_TRACE_ENTRIES);
    const cJSON *first = cJSON_GetArrayItem(events, 0);
    assert(cJSON_GetArrayItem(first, 1)->valueint == 250); // Oldest retained entry is #25.
    assert(strcmp(cJSON_GetArrayItem(first, 5)->valuestring, "tag\"\\") == 0);
    const char *logs = cJSON_GetObjectItem(root, "logs")->valuestring;
    assert(strncmp(logs, "W (", 3) == 0);                  // Starts at a line boundary.
    assert(strstr(logs, "line 199\n") != NULL);
    assert(strstr(logs, "line 0\n") == NULL);
    assert(strstr(logs, "?? bad \xe4\xbd\xa0\x01ok") != NULL); // cJSON decodes \u0001 back.
    cJSON_Delete(root);
    // A failing transport stops the stream and reports failure.
    sink_t *broken = calloc(1, sizeof(*broken));
    broken->fail_after = 2;
    assert(!agent_trace_core_encode(store, &meta, sink, broken));
    // A corrupted header is detected so a reboot starts a fresh ring.
    store->head = AGENT_TRACE_ENTRIES + 3;
    assert(!agent_trace_core_valid(store));
    free(out);
    free(broken);
    free(store);
}

int main(void)
{
    test_tags();
    test_ring_and_encode();
    puts("agent trace tests passed");
    return 0;
}
