package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/tts"
)

func readAll(t *testing.T, reader *SpeechReader) []byte {
	t.Helper()
	var out []byte
	for {
		chunk, ok, err := reader.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, chunk...)
	}
}

func TestSpeechReaderResumesAtAnyByte(t *testing.T) {
	stream := &speechStream{changed: make(chan struct{})}
	stream.append(frame(FrameSentence, []byte{0, 0, 'h', 'i'}))
	stream.append(frame(FrameAudio, []byte{1, 2, 3, 4, 5, 6}))
	stream.append(frame(FrameAudio, []byte{7, 8}))
	stream.end(SpeechDone)
	whole := readAll(t, &SpeechReader{stream: stream, closer: func() {}})
	// Every offset, including mid-header and mid-sample, continues exactly.
	for offset := 0; offset <= len(whole); offset++ {
		got := readAll(t, &SpeechReader{stream: stream, skip: offset, closer: func() {}})
		if !bytes.Equal(got, whole[offset:]) {
			t.Fatalf("offset %d: got %v want %v", offset, got, whole[offset:])
		}
	}
}

func TestReleaseSplitsHeldFrames(t *testing.T) {
	stream := &speechStream{changed: make(chan struct{})}
	held := append(frame(FrameAudio, []byte{1, 2}), frame(FrameAudio, []byte{3, 4, 5, 6})...)
	holding := true
	if !release(stream, &held, &holding) || holding || len(stream.frames) != 2 || len(stream.frames[1]) != 7 {
		t.Fatalf("frames %v", stream.frames)
	}
}

func TestSpeechReaderResumingUntilOffsetIsReached(t *testing.T) {
	stream := &speechStream{changed: make(chan struct{})}
	stream.append(frame(FrameAudio, []byte{1, 2, 3, 4}))
	reader := &SpeechReader{stream: stream, skip: 11, closer: func() {}}
	// The offset lies past the stream so far: the reader waits inside a frame
	// that is not there yet, where a keepalive must not be inserted.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := reader.Next(ctx); err == nil || !reader.Resuming() {
		t.Fatalf("err=%v resuming=%v", err, reader.Resuming())
	}
	stream.append(frame(FrameAudio, []byte{5, 6, 7, 8}))
	chunk, ok, err := reader.Next(context.Background())
	if err != nil || !ok || !bytes.Equal(chunk, []byte{6, 7, 8}) || reader.Resuming() {
		t.Fatalf("chunk=%v ok=%v err=%v resuming=%v", chunk, ok, err, reader.Resuming())
	}
}

func TestAbandonedSpeechIsRedoneForANewListener(t *testing.T) {
	ctx := context.Background()
	service, session := lifecycleFixture(t, lifecycleTransport(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	service.TTS = tts.Fake{}
	if err := service.Store.SetSessionReady(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	id := model.NewID()
	if _, _, err := service.Store.Send(ctx, session.ID, id, "hermes-agent", service.Hermes.Scope(), "读一下", false); err != nil {
		t.Fatal(err)
	}
	// The idle timer has stopped synthesis but the stream has not ended yet:
	// joining it would only hear "cancelled".
	_, cancel := context.WithCancel(ctx)
	stale := &speechStream{changed: make(chan struct{}), cancel: cancel, abandoned: true}
	service.speech.streams = map[string]*speechStream{id: stale}
	reader, err := service.OpenSpeech(ctx, session.ID, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.stream == stale || service.speech.streams[id] == stale {
		t.Fatal("new listener joined the abandoned stream")
	}
}
