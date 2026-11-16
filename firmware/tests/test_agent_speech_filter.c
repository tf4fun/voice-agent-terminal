#include <assert.h>
#include <math.h>
#include <stdio.h>
#include <string.h>

#include "agent_speech_filter.h"

enum { RATE = 16000 };

// Gain in dB of a full second of a sine, measured after the filter settles,
// fed in odd-sized chunks so state must carry across calls.
static double gain_db(double hz, double amplitude)
{
    static uint8_t pcm[2 * RATE];
    for (int i = 0; i < RATE; i++) {
        const int16_t value = (int16_t)lround(amplitude * sin(2 * M_PI * hz * i / RATE));
        pcm[2 * i] = (uint8_t)value;
        pcm[2 * i + 1] = (uint8_t)((uint16_t)value >> 8);
    }
    agent_speech_filter_t filter;
    agent_speech_filter_reset(&filter);
    for (size_t at = 0; at < sizeof(pcm);) {
        const size_t take = sizeof(pcm) - at < 314 ? sizeof(pcm) - at : 314;
        agent_speech_filter_apply(&filter, pcm + at, take);
        at += take;
    }
    double in = 0, out = 0;
    for (int i = RATE / 2; i < RATE; i++) {
        const double x = amplitude * sin(2 * M_PI * hz * i / RATE);
        const int16_t y = (int16_t)(pcm[2 * i] | (pcm[2 * i + 1] << 8));
        in += x * x;
        out += (double)y * y;
    }
    return 10 * log10(out / in);
}

int main(void)
{
    assert(gain_db(50, 12000) < -22);
    assert(gain_db(100, 12000) < -11);
    const double corner = gain_db(200, 12000);
    assert(corner < -2.5 && corner > -3.5);
    assert(fabs(gain_db(500, 12000)) < 0.3);
    assert(fabs(gain_db(1000, 12000)) < 0.1);
    assert(fabs(gain_db(4000, 12000)) < 0.1);
    // Near full scale the output clamps instead of wrapping around.
    assert(fabs(gain_db(1000, 32767)) < 0.1);

    // A constant offset (DC) decays to silence.
    uint8_t dc[2 * 1600];
    for (size_t i = 0; i < sizeof(dc); i += 2) { dc[i] = 0x10; dc[i + 1] = 0x27; } // 10000
    agent_speech_filter_t filter;
    agent_speech_filter_reset(&filter);
    agent_speech_filter_apply(&filter, dc, sizeof(dc));
    const int16_t last = (int16_t)(dc[sizeof(dc) - 2] | (dc[sizeof(dc) - 1] << 8));
    assert(last > -50 && last < 50);

    // A trailing odd byte is left for the caller to complete.
    uint8_t odd[3] = {0x34, 0x12, 0x77};
    agent_speech_filter_reset(&filter);
    agent_speech_filter_apply(&filter, odd, sizeof(odd));
    assert(odd[2] == 0x77);
    puts("PASS: speech high-pass response, clamping, DC removal and chunked state");
    return 0;
}
