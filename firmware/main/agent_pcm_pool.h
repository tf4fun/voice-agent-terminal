#pragma once

#include "esp_err.h"
#include "freertos/FreeRTOS.h"
#include "freertos/stream_buffer.h"

// One PCM buffer shared by recording and spoken replies. They never run
// together: starting a recording ends playback, so a single 16 KiB reservation
// gives each twice the room the two separate 8 KiB buffers had.

#define AGENT_PCM_POOL_BYTES 16384 // ~512 ms of 16 kHz PCM16

typedef enum { AGENT_PCM_FREE, AGENT_PCM_CAPTURE, AGENT_PCM_PLAYBACK } agent_pcm_owner_t;

esp_err_t agent_pcm_pool_init(void);
// Claims the buffer, emptied, for one user; NULL while the other holds it.
StreamBufferHandle_t agent_pcm_pool_take(agent_pcm_owner_t owner);
void agent_pcm_pool_release(agent_pcm_owner_t owner);
