#pragma once

#include <stdbool.h>
#include <stddef.h>

typedef enum {
    AGENT_PAGE_SESSIONS, AGENT_PAGE_CHAT,
    AGENT_PAGE_SETTINGS, AGENT_PAGE_PROVISION,
    AGENT_PAGE_PREPARING, AGENT_PAGE_RECORD,
} agent_page_t;

static const agent_page_t agent_root_pages[] = {AGENT_PAGE_SESSIONS, AGENT_PAGE_SETTINGS};
#define AGENT_ROOT_PAGE_COUNT (sizeof(agent_root_pages) / sizeof(agent_root_pages[0]))

typedef struct { int selected, direction; } agent_step_t;

static inline agent_step_t agent_page_step(int selected, int count, int direction, bool newer, bool older)
{
    agent_step_t step = {.selected = selected};
    if (direction < 0) {
        if (selected > 0) step.selected--;
        else if (newer) step.direction = -1;
    } else if (direction > 0) {
        if (selected + 1 < count) step.selected++;
        else if (older) step.direction = 1;
    }
    return step;
}

static inline bool agent_page_is_root(agent_page_t page, bool editing)
{
    if (editing) return false;
    for (size_t i = 0; i < AGENT_ROOT_PAGE_COUNT; i++) {
        if (agent_root_pages[i] == page) return true;
    }
    return false;
}

static inline agent_page_t agent_page_cycle(agent_page_t page, const agent_page_t *pages, size_t count)
{
    for (size_t i = 0; i < count; i++) {
        if (pages[i] == page) return pages[(i + 1) % count];
    }
    return page;
}

static inline bool agent_sleep_ready(bool network_paused, bool busy, bool button_released)
{
    return network_paused && !busy && button_released;
}

static inline agent_page_t agent_page_exit(agent_page_t page, bool editing)
{
    if (page == AGENT_PAGE_SETTINGS && editing) return AGENT_PAGE_SETTINGS;
    if (page == AGENT_PAGE_PROVISION) return AGENT_PAGE_SETTINGS;
    return AGENT_PAGE_SESSIONS;
}

static inline bool agent_page_can_record(agent_page_t page)
{
    return page == AGENT_PAGE_SESSIONS || page == AGENT_PAGE_CHAT;
}

