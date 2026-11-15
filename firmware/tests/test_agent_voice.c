#include <assert.h>
#include <stdio.h>
#include <string.h>

#include "agent_speech_frames.h"
#include "agent_voice.h"

static void test_modes(void)
{
    assert(agent_voice_mode(2, true, true, "completed", true, true) == AGENT_VOICE_RECORDING);
    assert(agent_voice_mode(3, false, true, "completed", false, true) == AGENT_VOICE_TRANSCRIBING);
    assert(agent_voice_mode(4, false, true, "completed", false, true) == AGENT_VOICE_TRANSCRIBING);
    assert(agent_voice_mode(0, true, true, "completed", true, true) == AGENT_VOICE_DRAFT);
    assert(agent_voice_mode(0, false, true, "receiving", true, true) == AGENT_VOICE_SPEAKING);
    assert(agent_voice_mode(0, false, true, "waiting", false, false) == AGENT_VOICE_THINKING);
    assert(agent_voice_mode(0, false, true, "receiving", false, false) == AGENT_VOICE_THINKING);
    assert(agent_voice_mode(0, false, true, "receiving", false, true) == AGENT_VOICE_REPLYING);
    assert(agent_voice_mode(0, false, true, "failed", false, true) == AGENT_VOICE_FAILED);
    assert(agent_voice_mode(0, false, true, "completed", false, true) == AGENT_VOICE_IDLE);
    assert(agent_voice_mode(0, false, false, "", false, false) == AGENT_VOICE_IDLE);
}

static void test_sentences(void)
{
    const char *reply = "## 天气\n周六多云，22 到 28 度。适合登山！\n- **南门**上山。\n\n| --- |\nOK, 3.5 km. Bye";
    uint16_t starts[8], ends[8];
    const int count = agent_sentence_split(reply, starts, ends, 8);
    const char *want[] = {"天气", "周六多云，22 到 28 度。", "适合登山！", "**南门**上山。", "OK, 3.5 km.", "Bye"};
    assert(count == 6);
    for (int i = 0; i < count; i++) {
        char got[64];
        memcpy(got, reply + starts[i], ends[i] - starts[i]);
        got[ends[i] - starts[i]] = '\0';
        if (strcmp(got, want[i]) != 0) { fprintf(stderr, "sentence %d: %s\n", i, got); assert(0); }
    }
    assert(agent_sentence_split("", starts, ends, 8) == 0);
    assert(agent_sentence_split("一。二。三。四。", starts, ends, 2) == 2);
    assert(strcmp(agent_utf8_tail("帮我查一下天气", 3), "下天气") == 0);
    assert(strcmp(agent_utf8_tail("ab", 5), "ab") == 0);
}

static void test_frames(void)
{
    // Two frames split at awkward points: a sentence and an end.
    const uint8_t stream[] = {1, 5, 0, 2, 0, 'h', 'i', '!', 3, 1, 0, 0};
    agent_speech_parser_t parser;
    agent_speech_parser_reset(&parser);
    int frames = 0;
    for (size_t i = 0; i < sizeof(stream);) {
        bool complete = false;
        const size_t step = i + 2 <= sizeof(stream) ? 2 : 1;
        i += agent_speech_parse(&parser, stream + i, step, &complete);
        if (complete) {
            frames++;
            if (frames == 1) assert(parser.kind == AGENT_SPEECH_SENTENCE && parser.length == 5 && parser.payload[2] == 'h');
            if (frames == 2) assert(parser.kind == AGENT_SPEECH_END && parser.payload[0] == 0);
        }
    }
    assert(frames == 2 && !parser.invalid);
    const uint8_t bad[] = {9, 0, 0};
    bool complete = false;
    agent_speech_parser_reset(&parser);
    agent_speech_parse(&parser, bad, sizeof(bad), &complete);
    assert(parser.invalid);
}

int main(void)
{
    test_modes();
    test_sentences();
    test_frames();
    puts("voice page logic and speech frames: PASS");
    return 0;
}
