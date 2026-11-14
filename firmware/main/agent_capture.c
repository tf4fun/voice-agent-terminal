#include "agent_capture.h"

#include <stdatomic.h>
#include "bsp_audio.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/stream_buffer.h"
#include "freertos/task.h"

static const char *TAG = "agent_capture";
static StreamBufferHandle_t s_pcm;
static TaskHandle_t s_task;
static atomic_bool s_requested, s_running;
static _Atomic esp_err_t s_error;
static atomic_uint s_level, s_duration;
static int16_t s_samples[320];

static void capture_task(void *arg)
{
    (void)arg;
    for (;;) {
        ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
        if (!s_requested) continue;
        s_running = true;
        uint32_t captured_samples = 0;
        ESP_LOGI(TAG, "PCM started free=%u largest=%u stack=%u",
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT),
                 (unsigned)uxTaskGetStackHighWaterMark(NULL));
        while (s_requested) {
            esp_err_t err = bsp_audio_read(s_samples, sizeof(s_samples));
            if (err != ESP_OK) { s_error = err; break; }
            uint32_t level = 0;
            for (size_t i = 0; i < 320; i++) level += s_samples[i] < 0 ? -(int32_t)s_samples[i] : s_samples[i];
            level = level / 320 / 128;
            s_level = level > 100 ? 100 : level;
            if (xStreamBufferSend(s_pcm, s_samples, sizeof(s_samples), 0) != sizeof(s_samples)) {
                s_error = ESP_ERR_NO_MEM;
                ESP_LOGW(TAG, "PCM queue full; network consumer is behind capture");
                break;
            }
            captured_samples += sizeof(s_samples) / sizeof(s_samples[0]);
            // Match the PCM byte limit even if the hardware clock runs fast.
            s_duration = captured_samples / 16;
            if (s_duration >= 300000) break;
        }
        ESP_LOGI(TAG, "PCM stopped error=%s duration_ms=%u free=%u largest=%u stack=%u",
                 esp_err_to_name(s_error), (unsigned)s_duration,
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT),
                 (unsigned)uxTaskGetStackHighWaterMark(NULL));
        s_requested = false;
        s_running = false;
        s_level = 0;
    }
}

esp_err_t agent_capture_init(void)
{
    if (s_task != NULL) return ESP_OK;
    if (s_pcm == NULL) s_pcm = xStreamBufferCreate(8192, 1);
    if (s_pcm == NULL) return ESP_ERR_NO_MEM;
    if (xTaskCreate(capture_task, "agent_pcm", 3072, NULL, 6, &s_task) != pdPASS) return ESP_ERR_NO_MEM;
    return ESP_OK;
}

esp_err_t agent_capture_begin(void)
{
    if (s_task == NULL || s_running || s_requested) return ESP_ERR_INVALID_STATE;
    esp_err_t err = bsp_audio_set_format(16000, 16, 1);
    if (err != ESP_OK) return err;
    // The producer and consumer are idle at this boundary.
    if (xStreamBufferReset(s_pcm) != pdPASS) return ESP_ERR_INVALID_STATE;
    s_error = ESP_OK;
    s_duration = 0;
    s_requested = true;
    xTaskNotifyGive(s_task);
    return ESP_OK;
}

void agent_capture_end(void) { s_requested = false; }
bool agent_capture_running(void) { return s_requested || s_running; }
esp_err_t agent_capture_error(void) { return s_error; }
size_t agent_capture_read(void *buffer, size_t capacity, uint32_t wait_ms)
{
    return xStreamBufferReceive(s_pcm, buffer, capacity, pdMS_TO_TICKS(wait_ms));
}
uint32_t agent_capture_duration_ms(void) { return s_duration; }
uint8_t agent_capture_level(void) { return s_level; }

