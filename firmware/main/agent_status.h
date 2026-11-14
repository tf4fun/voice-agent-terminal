#pragma once

#include <stdbool.h>

typedef enum {
    AGENT_STATUS_NEEDS_NETWORK,
    AGENT_STATUS_CONNECTING,
    AGENT_STATUS_DISCOVERING,
    AGENT_STATUS_SYNCING,
    AGENT_STATUS_EMPTY,
    AGENT_STATUS_ERROR,
} agent_status_t;

typedef struct {
    int x, y, width, height;
    int icon_x, icon_y, icon_size, title_y, detail_y;
} agent_status_layout_t;

static inline agent_status_t agent_session_status(bool has_ip, bool connecting, bool server_ready,
                                                  bool sessions_ready, bool loading, bool has_notice)
{
    if (!has_ip) return connecting ? AGENT_STATUS_CONNECTING : AGENT_STATUS_NEEDS_NETWORK;
    if (has_notice) return AGENT_STATUS_ERROR;
    if (!server_ready) return AGENT_STATUS_DISCOVERING;
    if (!sessions_ready || loading) return AGENT_STATUS_SYNCING;
    return AGENT_STATUS_EMPTY;
}

static inline agent_status_layout_t agent_status_layout(int width, int height)
{
    const int panel_width = width - 32;
    return (agent_status_layout_t){
        .x = 16, .y = 52 + (height - 52 - 24 - 148) / 2,
        .width = panel_width, .height = 148,
        .icon_x = (panel_width - 40) / 2, .icon_y = 0, .icon_size = 40,
        .title_y = 48, .detail_y = 84,
    };
}
