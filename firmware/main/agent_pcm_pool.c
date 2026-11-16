#include "agent_pcm_pool.h"

#include <stdatomic.h>

// Both stream buffers sit over the same storage; ownership makes sure only
// one of them is ever in use, and each is reset when it is claimed.
static uint8_t s_storage[AGENT_PCM_POOL_BYTES + 1];
static StaticStreamBuffer_t s_capture_control, s_playback_control;
static StreamBufferHandle_t s_capture, s_playback;
static atomic_int s_owner = AGENT_PCM_FREE;

esp_err_t agent_pcm_pool_init(void)
{
    if (s_capture != NULL) return ESP_OK;
    s_capture = xStreamBufferCreateStatic(AGENT_PCM_POOL_BYTES, 1, s_storage, &s_capture_control);
    s_playback = xStreamBufferCreateStatic(AGENT_PCM_POOL_BYTES, 1, s_storage, &s_playback_control);
    return s_capture != NULL && s_playback != NULL ? ESP_OK : ESP_ERR_NO_MEM;
}

StreamBufferHandle_t agent_pcm_pool_take(agent_pcm_owner_t owner)
{
    int expected = AGENT_PCM_FREE;
    if (s_capture == NULL || !atomic_compare_exchange_strong(&s_owner, &expected, (int)owner)) return NULL;
    StreamBufferHandle_t buffer = owner == AGENT_PCM_CAPTURE ? s_capture : s_playback;
    xStreamBufferReset(buffer); // Nobody is reading or writing it at this point.
    return buffer;
}

void agent_pcm_pool_release(agent_pcm_owner_t owner)
{
    int expected = (int)owner;
    atomic_compare_exchange_strong(&s_owner, &expected, AGENT_PCM_FREE);
}
