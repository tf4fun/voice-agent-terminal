// main/memo_ui_common.h —— 视觉设计语言里的基础件与配色。
//
// 只放"每个页面都会用到"的东西：颜色、卡片、标签。页面自己的布局留在各页面文件里。
#pragma once

#include "lvgl.h"

typedef enum {
    UI_COLOR_BACKGROUND, UI_COLOR_FOREGROUND, UI_COLOR_SECONDARY,
    UI_COLOR_BORDER, UI_COLOR_SURFACE, UI_COLOR_SELECTED, UI_COLOR_RECORDING,
    UI_COLOR_COUNT,
} memo_ui_color_t;

uint32_t memo_ui_color(memo_ui_color_t color);
// Recolor the existing object tree under the LVGL lock. Reading position and
// editor state survive scheduled changes and live theme/timezone previews.
void memo_ui_apply_theme(lv_obj_t *screen, bool dark);
void memo_ui_clock_update(uint64_t utc_seconds, int offset_minutes);

#define UI_BG memo_ui_color(UI_COLOR_BACKGROUND)
#define UI_FG memo_ui_color(UI_COLOR_FOREGROUND)
#define UI_DIM memo_ui_color(UI_COLOR_SECONDARY)
#define UI_BORDER memo_ui_color(UI_COLOR_BORDER)
#define UI_SURFACE memo_ui_color(UI_COLOR_SURFACE)
#define UI_SELECTED memo_ui_color(UI_COLOR_SELECTED)
#define UI_RECORDING memo_ui_color(UI_COLOR_RECORDING)

#define VM_RADIUS_CARD 10
#define VM_RADIUS_PANEL 10
#define VM_STROKE 1
#define VM_INSET 8

/* 去掉默认样式，只留一个纯色圆角矩形。 */
void memo_ui_flat_rect(lv_obj_t *obj, uint32_t color, int radius);

/* Rounded surface with a subtle border. shadow_out remains NULL. */
lv_obj_t *memo_ui_card(lv_obj_t *parent, int x, int y, int w, int h, lv_obj_t **shadow_out);

/* 建一个指定字体与颜色的标签。 */
lv_obj_t *memo_ui_label(lv_obj_t *parent, const lv_font_t *font, uint32_t color);

/* 显示/隐藏一个面板（shadow 为 NULL 时只处理面板）。 */
void memo_ui_card_visible(lv_obj_t *card, lv_obj_t *shadow, bool visible);

/* Live pages register battery labels; cached SOC is sampled by the input
 * owner outside the LVGL lock. Creation/update require that lock. */
lv_obj_t *memo_ui_battery(lv_obj_t *screen);
void memo_ui_battery_update(int soc);
/* Unregister labels and delete an inactive screen and its children/animations.
 * Hold the LVGL lock; switch to another screen before calling. */
void memo_ui_screen_delete(lv_obj_t *screen);

/* Single low-brightness terminal rule, without an emphasized leading segment. */
void memo_ui_rule(lv_obj_t *screen, int x, int y, int w);

/* 1 px framed track with a centred, 1 px inset fill; hold LVGL lock. */
lv_obj_t *memo_ui_meter_create(lv_obj_t *parent, int width, int height, lv_obj_t **fill);

/* Single status row with centered title, local time, battery and optional back icon. */
lv_obj_t *memo_ui_header(lv_obj_t *screen, const char *title, bool back);

/* A focused row uses a neutral selected surface with normal text colors. */
void memo_ui_set_selected(lv_obj_t *box, bool selected);

/* 4 px 轨道 + 亮绿滑块的位置指示器；创建与更新都需持 LVGL 锁。 */
lv_obj_t *memo_ui_scrollbar_create(lv_obj_t *screen, int x, int y, int h);
void memo_ui_scrollbar_update(lv_obj_t *track, int track_h, int total, int visible, int top);
