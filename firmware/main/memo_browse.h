// Bounded viewport and navigation decisions, independent of RTOS/LVGL.
#pragma once
#include <stdbool.h>
#include <stdint.h>

static inline int memo_viewport_top(int top, int selected, int count, int visible)
{
    if (count <= 0 || visible <= 0) return 0;
    if (selected < 0) selected = 0;
    if (selected >= count) selected = count - 1;
    if (top > count - visible) top = count - visible;
    if (top < 0) top = 0;
    if (selected < top) top = selected;
    if (selected >= top + visible) top = selected - visible + 1;
    return top;
}

typedef struct { bool visible; int offset; int length; } memo_scrollbar_t;
static inline memo_scrollbar_t memo_scrollbar_thumb(int total, int visible, int top,
                                                    int track_px, int min_thumb_px)
{
    memo_scrollbar_t bar = { .visible = false, .offset = 0, .length = 0 };
    if (total <= visible || visible <= 0 || track_px <= 0) return bar;
    if (top < 0) top = 0;
    const int max_top = total - visible;
    if (top > max_top) top = max_top;
    int length = track_px * visible / total;
    if (length < min_thumb_px) length = min_thumb_px;
    if (length > track_px) length = track_px;
    bar.offset = (track_px - length) * top / max_top;
    bar.length = length;
    bar.visible = true;
    return bar;
}

typedef struct {
    int clock_x, clock_width, title_x, title_width, battery_x;
} memo_header_layout_t;

static inline memo_header_layout_t memo_header_layout(int width, bool back)
{
    return (memo_header_layout_t){
        .clock_x = back ? 30 : 16, .clock_width = back ? 44 : 48,
        .title_x = 80, .title_width = width - 160, .battery_x = width - 64,
    };
}

typedef struct { int x, top, width, rows, pitch, row_height; } memo_rows_layout_t;

static inline memo_rows_layout_t memo_rows_layout(int width, int height)
{
    // Keep text inside the rounded panel's 24 px horizontal safe inset. The
    // first row starts below the header and the root-page indicator.
    const int pitch = (height - 44 - 12 + 6) / 4;
    return (memo_rows_layout_t){.x=16,.top=44,.width=width-32,.rows=4,
        .pitch=pitch,.row_height=pitch-6};
}

static inline memo_rows_layout_t memo_settings_layout(int width, int height)
{
    return memo_rows_layout(width, height);
}
