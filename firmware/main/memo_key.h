// Per-press gestures, independent of the driver's delayed multi-click events.
#pragma once
#include <stdbool.h>
#include <stdint.h>

typedef struct {
    int64_t pressed_us;
    bool down, held, enabled;
} memo_key_t;

typedef enum { MEMO_KEY_NONE, MEMO_KEY_CLICK, MEMO_KEY_HOLD } memo_key_action_t;

static inline void memo_key_press(memo_key_t *key, int64_t at_us, bool enabled)
{
    *key = (memo_key_t){.pressed_us = at_us, .down = true, .enabled = enabled};
}

static inline memo_key_action_t memo_key_hold(memo_key_t *key, int64_t at_us,
                                              int64_t hold_us, bool driver_long)
{
    if (!key->down || !key->enabled || key->held) return MEMO_KEY_NONE;
    if (!driver_long && at_us - key->pressed_us < hold_us) return MEMO_KEY_NONE;
    key->held = true;
    return MEMO_KEY_HOLD;
}

static inline memo_key_action_t memo_key_release(memo_key_t *key, int64_t at_us,
                                                 int64_t hold_us)
{
    memo_key_action_t action = memo_key_hold(key, at_us, hold_us, false);
    if (key->down && key->enabled && !key->held) action = MEMO_KEY_CLICK;
    key->down = false;
    return action;
}
