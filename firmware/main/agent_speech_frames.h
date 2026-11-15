#pragma once

// Incremental parser for the gateway's speech stream (server/internal/gateway/
// speech.go): one kind byte, a little-endian uint16 length, then the payload.
// Bytes may arrive split at any point. Audio payloads are passed through as
// they arrive (no frame buffer); other payloads are collected, keeping at most
// AGENT_SPEECH_TEXT_MAX bytes.

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

enum {
    AGENT_SPEECH_SENTENCE = 1,  // uint16 index, then UTF-8 text.
    AGENT_SPEECH_AUDIO = 2,     // 16 kHz PCM16LE mono.
    AGENT_SPEECH_END = 3,       // One status byte.
    AGENT_SPEECH_KEEPALIVE = 4, // Empty.
};

#define AGENT_SPEECH_FRAME_MAX 4096
#define AGENT_SPEECH_TEXT_MAX 258 // Sentence index plus the start of its text.

typedef struct {
    uint8_t header[3];
    size_t header_used;
    uint8_t kind;
    size_t length, used;
    uint8_t text[AGENT_SPEECH_TEXT_MAX];
    size_t text_used;
    bool invalid;
} agent_speech_parser_t;

typedef struct {
    uint8_t kind;         // 0 when nothing completed in this call.
    const uint8_t *data;  // Audio slice (in the input) or the collected payload.
    size_t size;
    bool complete;        // The frame ended with this event.
} agent_speech_event_t;

static inline void agent_speech_parser_reset(agent_speech_parser_t *parser)
{
    parser->header_used = parser->length = parser->used = parser->text_used = 0;
    parser->kind = 0;
    parser->invalid = false;
}

// Stream offset for resuming: the gateway's cached stream has no keepalives
// (they are added per connection between frames), so they are not counted.
static inline void agent_speech_track(size_t *offset, size_t used, const agent_speech_event_t *event)
{
    *offset += used;
    if (event->kind == AGENT_SPEECH_KEEPALIVE) *offset -= 3;
}

// Before reconnecting at *offset: a frame header cut short is asked for again
// from its first byte, while a frame body simply continues.
static inline void agent_speech_resume(agent_speech_parser_t *parser, size_t *offset)
{
    if (parser->header_used > 0 && parser->header_used < 3) {
        *offset -= parser->header_used;
        parser->header_used = 0;
    }
}

// Consumes bytes and reports at most one event: an audio slice, or a
// completed non-audio frame. Returns the bytes consumed.
static inline size_t agent_speech_parse(agent_speech_parser_t *parser, const uint8_t *data, size_t size,
                                        agent_speech_event_t *event)
{
    *event = (agent_speech_event_t){0};
    size_t consumed = 0;
    while (consumed < size && parser->header_used < 3) {
        parser->header[parser->header_used++] = data[consumed++];
        if (parser->header_used == 3) {
            parser->kind = parser->header[0];
            parser->length = (size_t)parser->header[1] | ((size_t)parser->header[2] << 8);
            parser->used = parser->text_used = 0;
            if (parser->kind < AGENT_SPEECH_SENTENCE || parser->kind > AGENT_SPEECH_KEEPALIVE ||
                parser->length > AGENT_SPEECH_FRAME_MAX) {
                parser->invalid = true;
                return consumed;
            }
            if (parser->length == 0) {
                parser->header_used = 0;
                *event = (agent_speech_event_t){.kind = parser->kind, .data = parser->text, .size = 0, .complete = true};
                return consumed;
            }
        }
    }
    if (parser->header_used < 3) return consumed;
    const size_t remaining = parser->length - parser->used;
    const size_t take = remaining < size - consumed ? remaining : size - consumed;
    const bool complete = parser->used + take == parser->length;
    if (parser->kind == AGENT_SPEECH_AUDIO) {
        *event = (agent_speech_event_t){.kind = AGENT_SPEECH_AUDIO, .data = data + consumed, .size = take, .complete = complete};
    } else {
        const size_t room = AGENT_SPEECH_TEXT_MAX - parser->text_used;
        const size_t keep = take < room ? take : room;
        memcpy(parser->text + parser->text_used, data + consumed, keep);
        parser->text_used += keep;
        if (complete) *event = (agent_speech_event_t){.kind = parser->kind, .data = parser->text, .size = parser->text_used, .complete = true};
    }
    parser->used += take;
    consumed += take;
    if (complete) parser->header_used = 0;
    return consumed;
}
