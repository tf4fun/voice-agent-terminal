#pragma once

#include "agent_protocol.h"
#include "esp_err.h"

typedef enum { AGENT_IDLE, AGENT_PREPARING, AGENT_RECORDING, AGENT_FINALIZING, AGENT_COMMITTING } agent_capture_state_t;
typedef enum { AGENT_TEXT_REPLY, AGENT_TEXT_INPUT, AGENT_TEXT_SEGMENT, AGENT_TEXT_DRAFT } agent_text_kind_t;

typedef struct {
    agent_session_t sessions[AGENT_PAGE_SIZE];
    int session_count, session_offset;
    bool sessions_more;
    int session_requested, segment_requested;
    bool sessions_loading, segments_loading, sessions_ready;
    char session_id[37], title[AGENT_TITLE_BYTES + 1];
    agent_segment_t segments[AGENT_PAGE_SIZE];
    int segment_count, segment_offset, segment_total;
    bool segments_more;
    uint32_t draft_revision, text_request;
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
esp_err_t agent_client_send(uint32_t reviewed_revision, uint32_t reviewed_selection);
esp_err_t agent_client_review_draft(void);
esp_err_t agent_client_retry(const char *id, uint32_t reviewed_selection);
esp_err_t agent_client_provision(bool enabled);
esp_err_t agent_client_pause(void);

