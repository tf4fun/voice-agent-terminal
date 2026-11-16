#pragma once

// Voice page logic shared by the device UI and host tests: which state the
// page shows, UTF-8 and sentence-end helpers for the line ticker, and the
// layout grid.

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

typedef enum {
    AGENT_VOICE_IDLE,          // Hold OK to speak; the strip may show the last reply.
    AGENT_VOICE_PREPARING,     // Connecting the recorder.
    AGENT_VOICE_RECORDING,
    AGENT_VOICE_TRANSCRIBING,  // Released; waiting for the final transcript. No key acts here.
    AGENT_VOICE_DRAFT,         // The strip expands into the draft panel.
    AGENT_VOICE_THINKING,      // Sent; waiting until Hermes has written the whole reply.
    // Values are trace codes (TRACE_VOICE); 6 was a streamed-reply mode.
    AGENT_VOICE_SPEAKING = 7,
    AGENT_VOICE_FAILED,        // The newest reply failed or is unconfirmed: OK retries.
} agent_voice_mode_t;

// capture: 0 idle, 1 preparing, 2 recording, 3 finalizing, 4 committing (agent_capture_state_t).
static inline agent_voice_mode_t agent_voice_mode(int capture, bool has_draft, bool has_turn,
                                                  const char *state, bool speaking)
{
    if (capture == 1) return AGENT_VOICE_PREPARING;
    if (capture == 2) return AGENT_VOICE_RECORDING;
    if (capture >= 3) return AGENT_VOICE_TRANSCRIBING;
    if (has_draft) return AGENT_VOICE_DRAFT;
    if (speaking) return AGENT_VOICE_SPEAKING;
    if (!has_turn) return AGENT_VOICE_IDLE;
    // The reply is fetched whole once complete, so receiving still reads as thinking.
    if (strcmp(state, "queued") == 0 || strcmp(state, "sending") == 0 || strcmp(state, "waiting") == 0 ||
        strcmp(state, "receiving") == 0)
        return AGENT_VOICE_THINKING;
    if (strcmp(state, "failed") == 0 || strcmp(state, "unconfirmed") == 0) return AGENT_VOICE_FAILED;
    return AGENT_VOICE_IDLE;
}

static inline size_t agent_utf8_step(const char *text, size_t at)
{
    const unsigned char lead = (unsigned char)text[at];
    size_t size = lead >= 0xF0 ? 4 : lead >= 0xE0 ? 3 : lead >= 0xC0 ? 2 : 1;
    for (size_t i = 1; i < size; i++)
        if (((unsigned char)text[at + i] & 0xC0) != 0x80) return 1;
    return size;
}

static inline bool agent_sentence_end(const char *text, size_t at, size_t size)
{
    const char *c = text + at;
    if (size == 3 && (memcmp(c, "。", 3) == 0 || memcmp(c, "！", 3) == 0 || memcmp(c, "？", 3) == 0 ||
                      memcmp(c, "；", 3) == 0 || memcmp(c, "…", 3) == 0)) return true;
    if (size != 1) return false;
    if (*c == '\n') return true;
    if (*c == '!' || *c == '?' || *c == ';' || *c == '.')
        return c[1] == '\0' || c[1] == ' ' || c[1] == '\n';
    return false;
}

// Start of the last max_runes characters of text.
static inline const char *agent_utf8_tail(const char *text, size_t max_runes)
{
    const size_t length = strlen(text);
    size_t runes = 0, at = length;
    while (at > 0 && runes < max_runes) {
        at--;
        while (at > 0 && ((unsigned char)text[at] & 0xC0) == 0x80) at--;
        runes++;
    }
    return text + at;
}

// Layout grid on the 240 x 320 panel (see the design draft):
// header 0-28, mode dots at 31, orb, status line, one-line strip, key hints.
enum {
    VOICE_ORB = 96, VOICE_ORB_X = 72, VOICE_ORB_Y = 54, // Arc clears the mode marks by 8 px.
    VOICE_ORB_SMALL = 44, VOICE_ORB_SMALL_X = 20, VOICE_ORB_SMALL_Y = 44,
    VOICE_STATUS_Y = 168, VOICE_TIMER_Y = 160,
    VOICE_STRIP_X = 14, VOICE_STRIP_Y = 212, VOICE_STRIP_W = 212, VOICE_STRIP_H = 44,
    VOICE_PANEL_Y = 98, VOICE_PANEL_H = 178,
    VOICE_SIDE_X = 76, VOICE_SIDE_Y = 42,
    VOICE_HINTS_Y = 280, VOICE_HINTS_H = 34,
    VOICE_TAIL_RUNES = 10,
};
