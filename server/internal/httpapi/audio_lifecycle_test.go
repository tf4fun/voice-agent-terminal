package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/asr"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

// The real realtime adapter closes its input after receiving the final transcript.
type inputClosingTranscriber struct{}

func (*inputClosingTranscriber) Enabled() bool { return true }
func (*inputClosingTranscriber) Transcribe(ctx context.Context, input io.ReadCloser, rate int, emit func(asr.Update) error) error {
	if _, err := io.Copy(io.Discard, input); err != nil {
		return err
	}
	if err := emit(asr.Update{Text: "completed transcript", Final: true}); err != nil {
		return err
	}
	if err := input.Close(); err != nil {
		return err
	}
	// Allow net/http's background read to process a deadline set during cleanup.
	select {
	case <-ctx.Done():
	case <-time.After(25 * time.Millisecond):
	}
	return nil
}

func TestCompletedChunkedAudioCanCloseInputAndCommit(t *testing.T) {
	e := setup(t)
	e.service.ASR = &inputClosingTranscriber{}
	session, segment := model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "input lifecycle"}, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": segment, "revision": 1}, 201)
	// An opaque reader keeps Content-Length unknown and uses actual chunked HTTP.
	input := io.NopCloser(strings.NewReader(strings.Repeat("\x00", 6400)))
	request, err := http.NewRequest("POST", e.server.URL+base+"/segments/"+segment+"/audio?revision=2&replace=1&defer_commit=1", input)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer device-test")
	request.Header.Set("Content-Type", "audio/pcm")
	response, err := e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var captured struct{ Revision int64 }
	if err := json.Unmarshal(body, &captured); err != nil || response.StatusCode != 200 || captured.Revision <= 2 {
		t.Fatalf("completed audio returned %d: %s (%v)", response.StatusCode, body, err)
	}
	e.request(t, "POST", base+"/segments/"+segment+"/commit", map[string]any{"revision": captured.Revision}, 200)
	text, _, err := e.service.Store.DraftText(context.Background(), session)
	if err != nil || text != "completed transcript" {
		t.Fatalf("draft text=%q err=%v", text, err)
	}
}
