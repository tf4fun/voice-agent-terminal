#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "esp_err.h"

// Plays a reply's spoken voice from the gateway while it is generated. One
// stream at a time; recording, leaving the session or a long Down stops it.

#define AGENT_SPEECH_SENTENCE_BYTES 192

typedef struct {
    bool active;     // A stream is open or about to open.
    bool audible;    // Audio has started for the current sentence.
    uint16_t index;  // Current sentence.
    char sentence[AGENT_SPEECH_SENTENCE_BYTES];
    char message_id[37];
    uint8_t level;   // 0..100 loudness of the audio being played.
    uint32_t serial; // Changes whenever a stream starts or stops.
} agent_speech_view_t;

esp_err_t agent_speech_init(void);
// Starts speaking a message, replacing any current stream.
void agent_speech_play(const char *session_id, const char *message_id);
// Stops playback; with wait, returns once the speaker is released (at most ~0.5 s).
void agent_speech_stop(bool wait);
void agent_speech_copy(agent_speech_view_t *out);
