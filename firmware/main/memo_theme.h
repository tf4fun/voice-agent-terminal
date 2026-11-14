#pragma once

#include <stdbool.h>
#include <stdint.h>

typedef enum { MEMO_THEME_LIGHT, MEMO_THEME_DARK, MEMO_THEME_AUTO } memo_theme_t;

static inline int memo_local_minutes(uint64_t utc_seconds, int offset_minutes)
{
    int minutes = (int)((utc_seconds / 60) % 1440) + offset_minutes;
    return (minutes % 1440 + 1440) % 1440;
}

static inline bool memo_theme_dark(memo_theme_t theme, uint64_t utc_seconds, int offset_minutes)
{
    if (theme != MEMO_THEME_AUTO) return theme == MEMO_THEME_DARK;
    if (utc_seconds == 0) return true;
    const int minutes = memo_local_minutes(utc_seconds, offset_minutes);
    return minutes < 7 * 60 || minutes >= 19 * 60;
}
