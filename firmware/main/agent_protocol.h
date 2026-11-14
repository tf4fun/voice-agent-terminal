#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define AGENT_PAGE_SIZE 4
#define AGENT_TITLE_BYTES 256
#define AGENT_TEXT_BYTES 512

typedef struct {
    char id[37], title[AGENT_TITLE_BYTES + 1], state[16];
    int64_t updated_at;
} agent_session_t;

typedef struct {
    char id[37], state[16], preview[81];
    uint32_t bytes;
} agent_segment_t;

typedef struct {
    char id[37], state[16], error[48];
    uint32_t revision, input_bytes, reply_bytes;
} agent_message_t;

typedef struct {
    agent_message_t message;
    char before[37], after[37];
    bool present;
} agent_turn_t;

bool agent_parse_turn(const char *json, agent_turn_t *turn);

typedef struct {
    char text[AGENT_TEXT_BYTES + 1];
    uint32_t offset, end, total, revision;
} agent_text_t;

bool agent_valid_id(const char *id);
void agent_copy_text(char *out, size_t capacity, const char *input);
bool agent_parse_sessions(const char *json, agent_session_t *items, int *count);
bool agent_parse_draft(const char *json, agent_segment_t *items, int *count, uint32_t *revision, int *total);
bool agent_parse_messages(const char *json, agent_message_t *items, int *count);
bool agent_parse_message(const char *json, agent_message_t *item);
bool agent_parse_text(const char *json, agent_text_t *text);
bool agent_parse_revision(const char *json, uint32_t *revision);
bool agent_message_active(const char *state);

