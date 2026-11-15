// Fill geometry in LVGL content coordinates: the pill fill spans the whole track.
#pragma once
#include <stdint.h>
typedef struct { int x, y, width, height; } memo_progress_layout_t;
static inline memo_progress_layout_t memo_progress_layout(int width, int height)
{
    return (memo_progress_layout_t){0, 0, width > 0 ? width : 0, height > 0 ? height : 0};
}
static inline int memo_progress_width(int width, uint32_t value, uint32_t maximum)
{
    if (width <= 0 || maximum == 0) return 0;
    if (value > maximum) value = maximum;
    return (int)((uint64_t)width * value / maximum);
}
