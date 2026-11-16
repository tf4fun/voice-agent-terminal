#pragma once

#include <stddef.h>
#include <stdint.h>

// A second-order Butterworth high-pass at 200 Hz for 16 kHz PCM16 speech.
// The small speaker cannot reproduce the low end that synthesized speech
// carries (about a third of its energy is below 250 Hz); driving it anyway
// makes heavy syllables crackle at high volume. Response: -24 dB at 50 Hz,
// -3 dB at 200 Hz, -0.1 dB at 500 Hz. Fixed point (Q24): the C3 has no FPU.
enum { AGENT_SPEECH_FILTER_SHIFT = 24 };

typedef struct {
    int32_t x1, x2, y1, y2;
    int64_t error; // The fraction the last shift dropped, carried into the next.
} agent_speech_filter_t;

static inline void agent_speech_filter_reset(agent_speech_filter_t *filter)
{
    filter->x1 = filter->x2 = filter->y1 = filter->y2 = 0;
    filter->error = 0;
}

// Filters little-endian PCM16 samples in place; state carries across calls.
static inline void agent_speech_filter_apply(agent_speech_filter_t *filter, uint8_t *pcm, size_t bytes)
{
    static const int64_t b0 = 15870858, b1 = -31741716, b2 = 15870858, a1 = -31692716, a2 = 15013500;
    for (size_t i = 0; i + 1 < bytes; i += 2) {
        const int32_t x = (int16_t)(pcm[i] | (pcm[i + 1] << 8));
        // Error feedback: without it the truncation leaves a constant offset
        // where the recursion stops decaying.
        const int64_t sum = b0 * x + b1 * filter->x1 + b2 * filter->x2 - a1 * filter->y1 - a2 * filter->y2 +
                            filter->error;
        int32_t y = (int32_t)(sum >> AGENT_SPEECH_FILTER_SHIFT);
        filter->error = sum - ((int64_t)y << AGENT_SPEECH_FILTER_SHIFT);
        filter->x2 = filter->x1;
        filter->x1 = x;
        filter->y2 = filter->y1;
        filter->y1 = y; // Unclamped, so the recursion stays exact.
        if (y > INT16_MAX) y = INT16_MAX;
        if (y < INT16_MIN) y = INT16_MIN;
        pcm[i] = (uint8_t)y;
        pcm[i + 1] = (uint8_t)((uint16_t)y >> 8);
    }
}
