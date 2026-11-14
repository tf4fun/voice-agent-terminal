// main/memo_settings.c
#include "memo_settings.h"

#include <string.h>

#include "esp_log.h"
#include "nvs.h"
#include "nvs_flash.h"

#include "bsp_audio.h"

static const char *TAG = "memo_settings";
static const char *NS = "memo";

static memo_settings_t s_settings;
static bool s_ready;
static nvs_handle_t s_nvs;

/* 音量 20% 一档：从 20 开始，0 表示静音，暂停在 100。 */
static const uint8_t kVolumeSteps[] = {0, 20, 40, 60, 80, 100};
static const uint8_t kTimeoutSteps[] = {0, 30, 60, 180};
static const uint8_t kSleepSteps[] = {0, 1, 5, 15};
static const uint8_t kThemeSteps[] = {MEMO_THEME_LIGHT, MEMO_THEME_DARK, MEMO_THEME_AUTO};

static const memo_settings_t kDefaults = {
    .volume = 80,
    .brightness = 100,
    .screen_timeout_s = 30,
    .sleep_min = 5,
    .tz_offset_min = 480,
    .theme = MEMO_THEME_AUTO,
};

static uint8_t step_in(const uint8_t *table, size_t count, uint8_t current, int direction)
{
    size_t index = 0;
    for (size_t i = 0; i < count; i++) {
        if (table[i] == current) {
            index = i;
            break;
        }
    }
    if (direction > 0) {
        index = (index + 1) % count;
    } else if (direction < 0) {
        index = (index + count - 1) % count;
    }
    return table[index];
}

uint8_t memo_settings_volume_step(uint8_t current, int direction)
{
    int next = current + (direction > 0 ? 20 : direction < 0 ? -20 : 0);
    if (next < 0) next = 0;
    if (next > 100) next = 100;
    return (uint8_t)next;
}

uint8_t memo_settings_brightness_step(uint8_t current, int direction)
{
    int next = current + (direction > 0 ? 10 : direction < 0 ? -10 : 0);
    if (next < 10) next = 10;
    if (next > 100) next = 100;
    return (uint8_t)next;
}

uint8_t memo_settings_timeout_step(uint8_t current, int direction)
{
    return step_in(kTimeoutSteps, sizeof(kTimeoutSteps), current, direction);
}

uint8_t memo_settings_sleep_step(uint8_t current, int direction)
{
    return step_in(kSleepSteps, sizeof(kSleepSteps), current, direction);
}

static bool is_option(int32_t value, const uint8_t *table, size_t count)
{
    for (size_t i = 0; i < count; i++) if (value == table[i]) return true;
    return false;
}

static int32_t read_i32(const char *key, int32_t fallback)
{
    int32_t value = fallback;
    if (nvs_get_i32(s_nvs, key, &value) != ESP_OK) return fallback;
    return value;
}

esp_err_t memo_settings_init(void)
{
    if (s_ready) return ESP_OK;
    s_settings = kDefaults;
    esp_err_t err = nvs_flash_init();
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "NVS unavailable; settings and credentials preserved");
        return err;
    }
    err = nvs_open(NS, NVS_READWRITE, &s_nvs);
    if (err != ESP_OK) {
        s_nvs = 0;
        return err;
    }

    // Validate before narrowing: corrupt/old integer values must not wrap.
    int32_t value = read_i32("vol", kDefaults.volume);
    if (is_option(value, kVolumeSteps, sizeof(kVolumeSteps))) s_settings.volume = (uint8_t)value;
    value = read_i32("br", kDefaults.brightness);
    if (value >= 10 && value <= 100 && value % 10 == 0) s_settings.brightness = (uint8_t)value;
    value = read_i32("to", kDefaults.screen_timeout_s);
    if (is_option(value, kTimeoutSteps, sizeof(kTimeoutSteps))) s_settings.screen_timeout_s = (uint8_t)value;
    value = read_i32("sl", kDefaults.sleep_min);
    if (is_option(value, kSleepSteps, sizeof(kSleepSteps))) s_settings.sleep_min = (uint8_t)value;
    value = read_i32("tz", kDefaults.tz_offset_min);
    if (value >= -720 && value <= 840) s_settings.tz_offset_min = (int16_t)value;

    value = read_i32("th", kDefaults.theme);
    if (value >= MEMO_THEME_LIGHT && value <= MEMO_THEME_AUTO) s_settings.theme = (uint8_t)value;

    s_ready = true;
    ESP_LOGI(TAG, "settings: volume=%u%% timeout=%us sleep=%umin tz=%d",
             s_settings.volume, s_settings.screen_timeout_s, s_settings.sleep_min,
             s_settings.tz_offset_min);
    return ESP_OK;
}

const memo_settings_t *memo_settings_get(void)
{
    return s_ready ? &s_settings : &kDefaults;
}

static esp_err_t persist_i32(const char *key, int32_t value, int32_t previous)
{
    if (!s_ready || s_nvs == 0) return ESP_ERR_INVALID_STATE;
    esp_err_t err = nvs_set_i32(s_nvs, key, value);
    if (err == ESP_OK) err = nvs_commit(s_nvs);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "setting %s not confirmed: %s", key, esp_err_to_name(err));
        // NVS set may have reached Flash before a later failure. Restore the
        // last confirmed value when possible; persistent I/O failure is still
        // an error, never a promise of rollback or a reason to erase NVS.
        esp_err_t rollback = nvs_set_i32(s_nvs, key, previous);
        if (rollback == ESP_OK) rollback = nvs_commit(s_nvs);
        if (rollback != ESP_OK) ESP_LOGE(TAG, "setting %s rollback not confirmed", key);
    }
    return err;
}

esp_err_t memo_settings_set_volume(uint8_t percent)
{
    if (!is_option(percent, kVolumeSteps, sizeof(kVolumeSteps))) return ESP_ERR_INVALID_ARG;
    esp_err_t err = persist_i32("vol", percent, s_settings.volume);
    if (err != ESP_OK) return err;
    s_settings.volume = percent;
    bsp_audio_set_volume(percent);
    return ESP_OK;
}

esp_err_t memo_settings_set_screen_timeout(uint8_t seconds)
{
    if (!is_option(seconds, kTimeoutSteps, sizeof(kTimeoutSteps))) return ESP_ERR_INVALID_ARG;
    esp_err_t err = persist_i32("to", seconds, s_settings.screen_timeout_s);
    if (err == ESP_OK) s_settings.screen_timeout_s = seconds;
    return err;
}

esp_err_t memo_settings_set_brightness(uint8_t percent)
{
    if (percent < 10 || percent > 100 || percent % 10 != 0) return ESP_ERR_INVALID_ARG;
    esp_err_t err = persist_i32("br", percent, s_settings.brightness);
    if (err == ESP_OK) s_settings.brightness = percent;
    return err;
}

esp_err_t memo_settings_set_sleep(uint8_t minutes)
{
    if (!is_option(minutes, kSleepSteps, sizeof(kSleepSteps))) return ESP_ERR_INVALID_ARG;
    esp_err_t err = persist_i32("sl", minutes, s_settings.sleep_min);
    if (err == ESP_OK) s_settings.sleep_min = minutes;
    return err;
}

esp_err_t memo_settings_set_tz(int16_t offset_min)
{
    if (offset_min < -720 || offset_min > 840) return ESP_ERR_INVALID_ARG;
    esp_err_t err = persist_i32("tz", offset_min, s_settings.tz_offset_min);
    if (err == ESP_OK) s_settings.tz_offset_min = offset_min;
    return err;
}

esp_err_t memo_settings_set_theme(uint8_t theme)
{
    if (theme > MEMO_THEME_AUTO) return ESP_ERR_INVALID_ARG;
    esp_err_t err = persist_i32("th", theme, s_settings.theme);
    if (err == ESP_OK) s_settings.theme = theme;
    return err;
}

esp_err_t memo_settings_edit_begin(memo_settings_edit_t *edit, memo_settings_field_t field)
{
    if (edit == NULL || field < MEMO_EDIT_VOLUME || field > MEMO_EDIT_TIMEZONE) return ESP_ERR_INVALID_ARG;
    if (edit->active) return ESP_ERR_INVALID_STATE;
    edit->value = *memo_settings_get();
    edit->field = field;
    edit->active = true;
    return ESP_OK;
}

void memo_settings_edit_adjust(memo_settings_edit_t *edit, int direction)
{
    if (edit == NULL || !edit->active) return;
    switch (edit->field) {
    case MEMO_EDIT_VOLUME:
        edit->value.volume = memo_settings_volume_step(edit->value.volume, direction);
        break;
    case MEMO_EDIT_BRIGHTNESS:
        edit->value.brightness = memo_settings_brightness_step(edit->value.brightness, direction);
        break;
    case MEMO_EDIT_TIMEOUT:
        edit->value.screen_timeout_s = memo_settings_timeout_step(edit->value.screen_timeout_s, direction);
        break;
    case MEMO_EDIT_SLEEP:
        edit->value.sleep_min = memo_settings_sleep_step(edit->value.sleep_min, direction);
        break;
    case MEMO_EDIT_THEME:
        edit->value.theme = step_in(kThemeSteps, sizeof(kThemeSteps), edit->value.theme, direction);
        break;
    case MEMO_EDIT_TIMEZONE: {
        int minutes = edit->value.tz_offset_min + (direction > 0 ? 15 : direction < 0 ? -15 : 0);
        if (minutes < -720) minutes = -720;
        if (minutes > 840) minutes = 840;
        edit->value.tz_offset_min = (int16_t)minutes;
        break;
    }
    }
}

esp_err_t memo_settings_edit_save(memo_settings_edit_t *edit)
{
    if (edit == NULL || !edit->active) return ESP_ERR_INVALID_STATE;
    esp_err_t err = ESP_ERR_INVALID_ARG;
    switch (edit->field) {
    case MEMO_EDIT_VOLUME: err = memo_settings_set_volume(edit->value.volume); break;
    case MEMO_EDIT_BRIGHTNESS: err = memo_settings_set_brightness(edit->value.brightness); break;
    case MEMO_EDIT_TIMEOUT: err = memo_settings_set_screen_timeout(edit->value.screen_timeout_s); break;
    case MEMO_EDIT_SLEEP: err = memo_settings_set_sleep(edit->value.sleep_min); break;
    case MEMO_EDIT_THEME: err = memo_settings_set_theme(edit->value.theme); break;
    case MEMO_EDIT_TIMEZONE: err = memo_settings_set_tz(edit->value.tz_offset_min); break;
    }
    if (err == ESP_OK) edit->active = false;
    return err;
}

void memo_settings_edit_cancel(memo_settings_edit_t *edit)
{
    if (edit != NULL) {
        edit->value = *memo_settings_get();
        edit->active = false;
    }
}
