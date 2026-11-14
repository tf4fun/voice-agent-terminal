// Application idle policy and shutdown ordering, independent of ESP-IDF/LVGL.
#pragma once
#include <stdbool.h>
#include <stdint.h>

typedef struct {
    int64_t activity_ms;
    bool blank;
    bool consuming_wake;
    bool wake_released;
    int64_t blank_since_ms; // Valid while blank; deep-sleep delay starts here.
} memo_power_t;

typedef enum { MEMO_POWER_ON, MEMO_POWER_BLANK, MEMO_POWER_SLEEP } memo_power_action_t;

// Consume the entire first gesture, including CLICK delivered after RELEASE.
// Call for every physical event, even keys that have no application action.
static inline bool memo_power_key(memo_power_t *p, int64_t now_ms, bool press, bool release)
{
    p->activity_ms = now_ms;
    if (p->blank) {
        p->blank = false;
        p->blank_since_ms = 0;
        p->consuming_wake = true;
        p->wake_released = false;
    } else if (p->consuming_wake && p->wake_released && press) {
        p->consuming_wake = false;
    }
    if (p->consuming_wake && release) p->wake_released = true;
    return p->consuming_wake;
}

static inline memo_power_action_t memo_power_poll(memo_power_t *p, int64_t now_ms,
                                                 unsigned screen_s, unsigned sleep_min,
                                                 bool keep_on, bool block_sleep)
{
    if (keep_on || now_ms < p->activity_ms) p->activity_ms = now_ms;
    const int64_t idle = now_ms - p->activity_ms;
    const bool blank = !keep_on && screen_s && idle >= (int64_t)screen_s * 1000;
    if (!blank) {
        p->blank = false;
        p->blank_since_ms = 0;
        return MEMO_POWER_ON;
    }
    // Start at the actual first blank decision, including after a delayed poll.
    if (!p->blank || now_ms < p->blank_since_ms) p->blank_since_ms = now_ms;
    p->blank = true;
    if (!block_sleep && sleep_min && now_ms - p->blank_since_ms >= (int64_t)sleep_min * 60000)
        return MEMO_POWER_SLEEP;
    return MEMO_POWER_BLANK;
}

