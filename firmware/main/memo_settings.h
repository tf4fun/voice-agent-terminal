// main/memo_settings.h —— 设备设置的持久化（NVS）。
//
// 目前真正被使用的：音量（播放立刻生效）、时区（列表/详情时间显示）、
// 亮度（可预览）、息屏与空闲休眠。
#pragma once

#include "esp_err.h"
#include "memo_theme.h"

#include <stdbool.h>
#include <stdint.h>

typedef struct {
    uint8_t volume;           /* 0..100，步进 20 */
    uint8_t brightness;       /* 10..100，步进 10；0 由熄屏逻辑使用 */
    uint8_t screen_timeout_s; /* 0 = 常亮 */
    uint8_t sleep_min;        /* 息屏后计时，0 = 不自动休眠 */
    int16_t tz_offset_min;
    uint8_t theme;
} memo_settings_t;

/* 初始化 NVS 并载入设置；未写入过的项用默认值。 */
esp_err_t memo_settings_init(void);

/* 当前设置（只读）。未初始化时返回默认值。 */
const memo_settings_t *memo_settings_get(void);

/* Application-owner calls only, outside the LVGL lock. Commit before publishing
 * runtime values/applying volume. Return NVS errors; no automatic erase.
 * A failed flash operation cannot promise that no bytes reached persistent storage. */
esp_err_t memo_settings_set_volume(uint8_t percent);
esp_err_t memo_settings_set_brightness(uint8_t percent);
esp_err_t memo_settings_set_screen_timeout(uint8_t seconds);
esp_err_t memo_settings_set_sleep(uint8_t minutes);
esp_err_t memo_settings_set_tz(int16_t offset_min);
esp_err_t memo_settings_set_theme(uint8_t theme);

/* Positive direction increases volume (clamped); timing options cycle. */
uint8_t memo_settings_volume_step(uint8_t current, int direction);
uint8_t memo_settings_brightness_step(uint8_t current, int direction);
uint8_t memo_settings_timeout_step(uint8_t current, int direction);
uint8_t memo_settings_sleep_step(uint8_t current, int direction);

typedef enum { MEMO_EDIT_VOLUME, MEMO_EDIT_BRIGHTNESS, MEMO_EDIT_TIMEOUT, MEMO_EDIT_SLEEP, MEMO_EDIT_THEME, MEMO_EDIT_TIMEZONE } memo_settings_field_t;
typedef struct {
    memo_settings_t value;
    memo_settings_field_t field;
    bool active;
} memo_settings_edit_t;

/* Begin/adjust/cancel have no hardware or persistence side effects. Save changes
 * only the selected field; failure retains the draft and last confirmed runtime
 * value so the application can show an error and retry. Initialise edit to zero. */
esp_err_t memo_settings_edit_begin(memo_settings_edit_t *edit, memo_settings_field_t field);
void memo_settings_edit_adjust(memo_settings_edit_t *edit, int direction);
esp_err_t memo_settings_edit_save(memo_settings_edit_t *edit);
void memo_settings_edit_cancel(memo_settings_edit_t *edit);
