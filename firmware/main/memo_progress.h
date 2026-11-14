// Track geometry in LVGL content coordinates: 1 px border plus 1 px inset.
#pragma once
#include <stdint.h>
typedef struct { int x, y, width, height; } memo_progress_layout_t;
static inline memo_progress_layout_t memo_progress_layout(int width, int height)
{
    return (memo_progress_layout_t){1, 1, width > 4 ? width - 4 : 0, height > 4 ? height - 4 : 0};
}
static inline int memo_progress_width(int width, uint32_t value, uint32_t maximum)
{
    if (width <= 4 || maximum == 0) return 0;
    if (value > maximum) value = maximum;
    return (int)((uint64_t)(width - 4) * value / maximum);
}
