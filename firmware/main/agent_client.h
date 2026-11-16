#pragma once

#include "agent_protocol.h"
#include "esp_err.h"

typedef enum { AGENT_IDLE, AGENT_PREPARING, AGENT_RECORDING, AGENT_FINALIZING } agent_capture_state_t;
// SEGMENT is the live transcript while recording; DRAFT the unsent voice draft.
typedef enum { AGENT_TEXT_REPLY, AGENT_TEXT_INPUT, AGENT_TEXT_SEGMENT, AGENT_TEXT_DRAFT } agent_text_kind_t;

// The voice draft lives only in device RAM until it is sent; a reboot loses it.
// Spoken instructions are short, so it is bounded like a short message.
#define AGENT_DRAFT_MAX_CHARS 140
#define AGENT_DRAFT_MAX_BYTES (AGENT_DRAFT_MAX_CHARS * 4)
#define AGENT_STR_(value) #value
#define AGENT_STR(value) AGENT_STR_(value)
#define AGENT_DRAFT_TOO_LONG "超过 " AGENT_STR(AGENT_DRAFT_MAX_CHARS) " 字，请缩短后重录"

typedef struct {
    agent_session_t sessions[AGENT_PAGE_SIZE];
    int session_count, session_offset;
    bool sessions_more;
    int session_requested;
    bool sessions_loading, sessions_ready;
    char session_id[37], title[AGENT_TITLE_BYTES + 1];
    // A new chat has a local ID only; the gateway creates it with the first send.
    bool session_local;
    // Characters in the unsent draft (0 = none); the serial changes with its text.
    uint32_t draft_chars, draft_serial;
    uint32_t text_request;
    char turn_anchor[37];
    uint32_t chat_request;
    bool chat_loading, chat_ready, chat_sending, text_loading;
    agent_message_t message;
    agent_turn_t turn;
    uint32_t text_generation;
    agent_text_t text;
    agent_text_kind_t text_kind;
    char text_id[37];
    agent_capture_state_t capture;
    bool connected, busy, provisioning, paused, session_switching;
    bool speech; // Gateway capability: spoken replies.
    char notice[64];
    uint32_t generation, selection, capture_serial;
} agent_view_t;

esp_err_t agent_client_init(void);
void agent_client_copy(agent_view_t *out);
// Caller owns the returned full-message snapshot. NULL means stale or out of memory.
char *agent_client_text_copy(uint32_t selection, uint32_t generation);
void agent_client_message_select(bool input);
esp_err_t agent_client_history(int direction);
esp_err_t agent_client_refresh(void);
bool agent_client_discovery_pending(void);
esp_err_t agent_client_sessions_page(int direction);
esp_err_t agent_client_open(const agent_session_t *session);
esp_err_t agent_client_close_session(void);
esp_err_t agent_client_record(bool new_session);
void agent_client_release_record(void);
// False once saving has started; leaving that page keeps the save running.
bool agent_client_cancel_record(void);
// Sends the draft shown with this serial; a new chat's session is created by it.
esp_err_t agent_client_send(uint32_t draft_serial, uint32_t reviewed_selection);
esp_err_t agent_client_retry(const char *id, uint32_t reviewed_selection);
// The loaded input or reply of the shown exchange regardless of which one the
// text card displays (voice page). Caller owns the copy; NULL when not loaded.
char *agent_client_part_copy(uint32_t selection, bool input);
// The opening of that part, cut at a UTF-8 boundary into the caller's buffer;
// nothing is allocated. False when it is not loaded.
bool agent_client_part_prefix(uint32_t selection, bool input, char *out, size_t capacity);
// Discards the unsent draft (long Down on the voice page); nothing is sent.
esp_err_t agent_client_clear_draft(void);
// Returns from older history to the newest exchange.
esp_err_t agent_client_show_latest(void);
esp_err_t agent_client_provision(bool enabled);
esp_err_t agent_client_pause(void);

