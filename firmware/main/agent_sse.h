#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <string.h>

// The gateway emits one bounded JSON data line per SSE event.
#define AGENT_SSE_LINE_BYTES 1152

typedef struct {
    char line[AGENT_SSE_LINE_BYTES];
    size_t used;
} agent_sse_t;

typedef bool (*agent_sse_event_fn)(const char *json, void *context);

static inline bool agent_sse_feed(agent_sse_t *stream, const char *bytes, size_t length,
                                  agent_sse_event_fn event, void *context)
{
    for (size_t index = 0; index < length; index++) {
        const char character = bytes[index];
        if (character == '\r') continue;
        if (character == '\n') {
            stream->line[stream->used] = '\0';
            if (stream->used >= 5 && memcmp(stream->line, "data:", 5) == 0) {
                const char *json = stream->line + 5;
                if (*json == ' ') json++;
                if (!event(json, context)) return false;
            }
            stream->used = 0;
        } else {
            if (stream->used == sizeof(stream->line) - 1) return false;
            stream->line[stream->used++] = character;
        }
    }
    return true;
}
