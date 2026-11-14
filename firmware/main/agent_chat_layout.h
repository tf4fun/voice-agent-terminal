#pragma once

#define AGENT_CHAT_LINE_SPACE (-8)

typedef struct {
    int x, top, width, height;
    int role_y, body_y, body_width, body_height, status_x, status_y, status_width;
} agent_chat_card_layout_t;

static inline agent_chat_card_layout_t agent_chat_card_layout(int width, int height,
                                                             int line_height)
{
    const int top = 34;
    const int card_height = height - top - 12;
    const int body_y = line_height + 1;
    const int body_width = width - 50;
    return (agent_chat_card_layout_t){
        .x = 12, .top = top, .width = width - 24, .height = card_height,
        .role_y = 0, .body_y = body_y, .body_width = body_width,
        .body_height = card_height - body_y - 14,
        .status_x = 58, .status_y = 0, .status_width = body_width - 46,
    };
}
