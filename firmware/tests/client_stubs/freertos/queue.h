#pragma once
#include "FreeRTOS.h"
typedef void *QueueHandle_t;
QueueHandle_t xQueueCreate(unsigned count, size_t size);
int xQueueSend(QueueHandle_t queue, const void *item, unsigned timeout);
int xQueueReceive(QueueHandle_t queue, void *item, unsigned timeout);
unsigned uxQueueMessagesWaiting(QueueHandle_t queue);
