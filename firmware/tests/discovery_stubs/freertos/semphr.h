#pragma once

#include <stdint.h>

typedef struct test_semaphore *SemaphoreHandle_t;
SemaphoreHandle_t xSemaphoreCreateMutex(void);
void xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t timeout);
void xSemaphoreGive(SemaphoreHandle_t semaphore);
