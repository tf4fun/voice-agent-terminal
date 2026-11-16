#pragma once
#include <stdint.h>
#include <stddef.h>
#include "esp_err.h"
#define MALLOC_CAP_8BIT 0
#define MALLOC_CAP_DEFAULT 0
size_t heap_caps_get_free_size(uint32_t caps);
size_t heap_caps_get_largest_free_block(uint32_t caps);
size_t heap_caps_get_minimum_free_size(uint32_t caps);
