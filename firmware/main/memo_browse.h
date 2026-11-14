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

// Return -1/1 for server-page movement, 0 for selection within current rows.
static inline int memo_browse_move(int *selected, int count, int direction, bool newer, bool older)
{
    if (direction < 0) {
        if (*selected > 0) --*selected;
        else if (newer) return -1;
    } else if (direction > 0) {
        if (*selected + 1 < count) ++*selected;
        else if (older) return 1;
    }
    return 0;
}

typedef struct { int index, page; bool request_page; } memo_detail_step_t;
static inline memo_detail_step_t memo_detail_step(int index, int count, int local_count,
                                                 int direction, bool newer, bool older)
{
    memo_detail_step_t step = {.index = -1};
    if (index < 0 || index >= count || direction == 0) return step;
    if (direction < 0) {
        // A remote page's first entry follows the newer page, not the local
        // prefix repeated above every server page in the list model.
        if (index == local_count && newer) return (memo_detail_step_t){-1, -1, true};
        if (index > 0) step.index = index - 1;
    } else {
        if (index < local_count && index + 1 == local_count && newer)
            return (memo_detail_step_t){-1, 0, true}; // Local -> latest remote.
        if (index + 1 < count) step.index = index + 1;
        else if (older) return (memo_detail_step_t){-1, 1, true};
    }
    return step;
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
typedef struct { int time_x, time_w, text_x, text_w, duration_x, duration_w; } memo_list_columns_t;
static inline memo_list_columns_t memo_list_columns(int row_width)
{
    // Summary occupies its own 31 px line. Metadata uses a separate 18 px
    // line, so even a long pending label cannot overlap time or duration.
    const int duration_x = row_width - 12 - 40;
    return (memo_list_columns_t){12, 40, 12, row_width - 24, duration_x, 40};
}

static inline memo_rows_layout_t memo_rows_layout(int width, int height)
{
    // Keep text inside the rounded panel's 24 px horizontal safe inset.
    const int pitch = (height - 34 - 12 + 6) / 4;
    return (memo_rows_layout_t){.x=16,.top=34,.width=width-32,.rows=4,
        .pitch=pitch,.row_height=pitch-6};
}

static inline memo_rows_layout_t memo_settings_layout(int width, int height)
{
    return memo_rows_layout(width, height);
}
