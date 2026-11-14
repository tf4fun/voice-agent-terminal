#pragma once
#include "FreeRTOS.h"
typedef void *TaskHandle_t;
int xTaskCreate(void (*callback)(void *), const char *name, unsigned stack,
                void *argument, unsigned priority, TaskHandle_t *task);
void vTaskDelay(unsigned ticks);
unsigned ulTaskNotifyTake(int clear, unsigned timeout);
void xTaskNotifyGive(TaskHandle_t task);
