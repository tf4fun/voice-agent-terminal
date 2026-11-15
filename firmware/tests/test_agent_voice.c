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
    // A sentence, audio split across reads, and an end frame, fed two bytes at a time.
    const uint8_t stream[] = {1, 5, 0, 2, 0, 'h', 'i', '!', 2, 6, 0, 1, 2, 3, 4, 5, 6, 3, 1, 0, 0};
    agent_speech_parser_t parser;
    agent_speech_parser_reset(&parser);
    int sentences = 0, ends = 0;
    uint8_t audio[8];
    size_t audio_used = 0;
    for (size_t i = 0; i < sizeof(stream);) {
        agent_speech_event_t event;
        const size_t step = i + 2 <= sizeof(stream) ? 2 : 1;
        size_t used = 0;
        while (used < step) {
            used += agent_speech_parse(&parser, stream + i + used, step - used, &event);
            if (event.kind == AGENT_SPEECH_SENTENCE) {
                sentences++;
                assert(event.complete && event.size == 5 && event.data[2] == 'h');
            } else if (event.kind == AGENT_SPEECH_AUDIO) {
                memcpy(audio + audio_used, event.data, event.size);
                audio_used += event.size;
            } else if (event.kind == AGENT_SPEECH_END) {
                ends++;
                assert(event.size == 1 && event.data[0] == 0);
            }
        }
        i += step;
    }
    assert(sentences == 1 && ends == 1 && audio_used == 6 && audio[0] == 1 && audio[5] == 6 && !parser.invalid);
    // A long sentence keeps only its start.
    uint8_t long_frame[3 + 600] = {1, 0x58, 0x02};
    memset(long_frame + 3, 'x', 600);
    agent_speech_parser_reset(&parser);
    agent_speech_event_t event;
    size_t at = 0;
    do { at += agent_speech_parse(&parser, long_frame + at, sizeof(long_frame) - at, &event); } while (event.kind == 0);
    assert(at == sizeof(long_frame) && event.size == AGENT_SPEECH_TEXT_MAX);
    const uint8_t bad[] = {9, 0, 0};
    agent_speech_parser_reset(&parser);
    agent_speech_parse(&parser, bad, sizeof(bad), &event);
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
