// main/memo_ui_settings.h —— 设置页与子页。
#pragma once

#include "lvgl.h"
#include "memo_settings.h"

#include <stdbool.h>

/* 建页面（默认隐藏）。需持 LVGL 锁调用。 */
void memo_ui_settings_build(void);
/* Stop animations and delete after switching away; hold LVGL lock. */
void memo_ui_settings_destroy(void);

/* 每次进入设置页时刷新一次显示值（会收起子页）。需持 LVGL 锁调用。 */
void memo_ui_settings_refresh(void);

/* 只刷新各行数值，不影响当前打开的子页。需持 LVGL 锁调用。 */
void memo_ui_settings_refresh_values(void);

/* 行的语义编号，外壳据此决定进入配网页还是子页。 */
typedef enum {
    MEMO_SETTING_NETWORK = 0,
    MEMO_SETTING_SERVER,
    MEMO_SETTING_VOLUME,
    MEMO_SETTING_BRIGHTNESS,
    MEMO_SETTING_TIMEOUT,
    MEMO_SETTING_SLEEP,
    MEMO_SETTING_THEME,
    MEMO_SETTING_TIMEZONE,
    MEMO_SETTING_ABOUT,
} memo_setting_row_t;

/* Render an edit draft. These functions never persist settings or access audio;
 * application owner handles those operations outside the LVGL lock. */
void memo_ui_settings_enter(const memo_settings_t *draft);

void memo_ui_settings_show_draft(const memo_settings_t *draft);
void memo_ui_settings_error(void);

/* 当前是否停在子页（音量/亮度/息屏/休眠）。 */
bool memo_ui_settings_in_sheet(void);

lv_obj_t *memo_ui_settings_screen(void);

void memo_ui_settings_set_selected(int selected);
