#pragma once

// Incremental parser for the gateway's speech stream (server/internal/gateway/
// speech.go): one kind byte, a little-endian uint16 length, then the payload.
// Bytes may arrive split at any point; complete frames are handed out whole.

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

typedef struct {
    uint8_t header[3];
    size_t header_used;
    uint8_t kind;
    size_t length, used;
    uint8_t payload[AGENT_SPEECH_FRAME_MAX];
    bool invalid;
} agent_speech_parser_t;

static inline void agent_speech_parser_reset(agent_speech_parser_t *parser)
{
    parser->header_used = parser->length = parser->used = 0;
    parser->kind = 0;
    parser->invalid = false;
}

// Consumes bytes up to the end of one frame. Returns the bytes consumed and
// sets *complete when parser->kind/payload/length hold a whole frame.
static inline size_t agent_speech_parse(agent_speech_parser_t *parser, const uint8_t *data, size_t size, bool *complete)
{
    *complete = false;
    size_t consumed = 0;
    while (consumed < size && parser->header_used < 3) {
        parser->header[parser->header_used++] = data[consumed++];
        if (parser->header_used == 3) {
            parser->kind = parser->header[0];
            parser->length = (size_t)parser->header[1] | ((size_t)parser->header[2] << 8);
            parser->used = 0;
            if (parser->kind < AGENT_SPEECH_SENTENCE || parser->kind > AGENT_SPEECH_KEEPALIVE ||
                parser->length > AGENT_SPEECH_FRAME_MAX) {
                parser->invalid = true;
                return consumed;
            }
        }
    }
    if (parser->header_used < 3) return consumed;
    const size_t take = parser->length - parser->used < size - consumed ? parser->length - parser->used : size - consumed;
    memcpy(parser->payload + parser->used, data + consumed, take);
    parser->used += take;
    consumed += take;
    if (parser->used == parser->length) {
        *complete = true;
        parser->header_used = 0;
    }
    return consumed;
}
