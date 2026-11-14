#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include "esp_err.h"

esp_err_t agent_capture_init(void);
esp_err_t agent_capture_begin(void);
void agent_capture_end(void);
bool agent_capture_running(void);
esp_err_t agent_capture_error(void);
size_t agent_capture_read(void *buffer, size_t capacity, uint32_t wait_ms);
uint32_t agent_capture_duration_ms(void);
uint8_t agent_capture_level(void);

