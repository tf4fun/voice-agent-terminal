#pragma once
#include <stdint.h>
#include <stddef.h>
#include "esp_err.h"
#define ESP_GPIO_WAKEUP_GPIO_LOW 0
#define ESP_SLEEP_WAKEUP_GPIO 7
esp_err_t esp_deep_sleep_enable_gpio_wakeup(uint64_t mask, int mode);
void esp_deep_sleep_start(void);
int esp_sleep_get_wakeup_cause(void);
