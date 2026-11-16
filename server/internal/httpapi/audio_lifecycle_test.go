package httpapi

import (
	"context"
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

func TestCompletedChunkedAudioCanCloseInputAndReturnText(t *testing.T) {
	e := setup(t)
	e.service.ASR = &inputClosingTranscriber{}
	recording := model.NewID()
	e.request(t, "POST", "/api/v1/transcriptions", map[string]string{"id": recording}, 201)
	// An opaque reader keeps Content-Length unknown and uses actual chunked HTTP.
	input := io.NopCloser(strings.NewReader(strings.Repeat("\x00", 6400)))
	request, err := http.NewRequest("POST", e.server.URL+"/api/v1/transcriptions/"+recording+"/audio", input)
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
	if response.StatusCode != 200 || string(body) != "completed transcript" ||
		response.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("completed audio returned %d %q %q", response.StatusCode, response.Header.Get("Content-Type"), body)
	}
}
