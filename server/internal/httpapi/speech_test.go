package httpapi

import (
	"encoding/binary"
	"io"
	"net/http"
	"strconv"
	"testing"
	"unicode/utf8"

	"github.com/tf4fun/voice-agent-terminal/server/internal/gateway"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/tts"
)

type speechFrame struct {
	kind    byte
	payload []byte
}

func readSpeech(t *testing.T, e *environment, path string) []speechFrame {
	t.Helper()
	request, _ := http.NewRequest("GET", e.server.URL+path, nil)
	request.Header.Set("Authorization", "Bearer device-test")
	response, err := e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("X-Speech-Format") != "pcm_s16le;rate=16000;channels=1" {
		t.Fatalf("speech status %d", response.StatusCode)
	}
	var frames []speechFrame
	header := make([]byte, 3)
	for {
		if _, err := io.ReadFull(response.Body, header); err != nil {
			t.Fatalf("stream ended without an end frame: %v", err)
		}
		payload := make([]byte, binary.LittleEndian.Uint16(header[1:]))
		if _, err := io.ReadFull(response.Body, payload); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, speechFrame{header[0], payload})
		if header[0] == gateway.FrameEnd {
			return frames
		}
	}
}

func TestReplySpeechStreamsSentencesAndAudio(t *testing.T) {
	e := setup(t)
	session, segment, message := model.NewID(), model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "朗读"}, 201)
	path := base + "/messages/" + message + "/speech"
	e.request(t, "POST", base+"/segments", map[string]any{"id": segment, "revision": 1, "text": "读一下"}, 201)
	e.request(t, "POST", base+"/messages", map[string]any{"id": message, "revision": 2}, 202)
	e.request(t, "GET", path, nil, 503) // No synthesizer configured.
	e.service.TTS = tts.Fake{}
	// Opened while the reply may still be streaming; speech starts once it is complete.
	frames := readSpeech(t, e, path)
	sentences, audio := 0, 0
	spoken := ""
	for _, f := range frames {
		switch f.kind {
		case gateway.FrameSentence:
			if binary.LittleEndian.Uint16(f.payload) != uint16(sentences) {
				t.Fatal("sentence index out of order")
			}
			spoken += string(f.payload[2:])
			sentences++
		case gateway.FrameAudio:
			audio += len(f.payload)
		case gateway.FrameKeepalive:
			if len(f.payload) != 0 {
				t.Fatal("keepalive with payload")
			}
		}
	}
	end := frames[len(frames)-1]
	if sentences == 0 || end.payload[0] != gateway.SpeechDone {
		t.Fatalf("sentences=%d end=%v", sentences, end.payload)
	}
	// The fake speaks 100 ms (3200 bytes) per character.
	if want := utf8.RuneCountInString(spoken) * 3200; audio != want {
		t.Fatalf("audio %d bytes for %q, want %d", audio, spoken, want)
	}
	// A second listener replays the finished stream from the start.
	if again := readSpeech(t, e, path); len(again) != len(frames) {
		t.Fatalf("replay has %d frames, first had %d", len(again), len(frames))
	}
	// Resuming at a byte offset continues there: past the sentence frame,
	// the next bytes are the first audio frame.
	resumed := readSpeech(t, e, path+"?offset="+strconv.Itoa(3+len(frames[0].payload)))
	if resumed[0].kind != gateway.FrameAudio || len(resumed) != len(frames)-1 {
		t.Fatalf("resume started with kind %d, %d frames", resumed[0].kind, len(resumed))
	}
	e.request(t, "GET", path+"?offset=x", nil, 400)
	e.request(t, "GET", base+"/messages/"+model.NewID()+"/speech", nil, 404)
}

func TestClearDraftRetiresAllSegmentsAtTheReviewedRevision(t *testing.T) {
	e := setup(t)
	session := model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "清空"}, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": model.NewID(), "revision": 1, "text": "第一段"}, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": model.NewID(), "revision": 2, "text": "第二段"}, 201)
	e.request(t, "DELETE", base+"/draft", map[string]any{"revision": 2}, 409) // Stale review.
	e.request(t, "DELETE", base+"/draft", map[string]any{"revision": 3}, 200)
	if text := string(e.request(t, "GET", base+"/draft/content", nil, 200)); text != "" {
		t.Fatalf("draft after clear: %q", text)
	}
}
