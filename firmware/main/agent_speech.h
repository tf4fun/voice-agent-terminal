#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "esp_err.h"

// Plays a reply's spoken voice from the gateway once the reply is complete. One
// stream at a time; recording, leaving the session or a long Down stops it.

typedef struct {
    bool active;     // A stream is open or about to open (it waits while Hermes thinks).
    bool audible;    // The gateway has started speaking the reply.
    char message_id[37];
    uint8_t level;   // 0..100 loudness of the audio being played.
    uint32_t played_ms; // Audio sent to the speaker for the current stream.
} agent_speech_view_t;

esp_err_t agent_speech_init(void);
// Starts speaking a message, replacing any current stream.
void agent_speech_play(const char *session_id, const char *message_id);
// Stops playback; with wait, returns once the speaker is released (at most ~0.5 s).
void agent_speech_stop(bool wait);
void agent_speech_copy(agent_speech_view_t *out);
