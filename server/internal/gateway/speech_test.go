package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/config"
	"github.com/tf4fun/voice-agent-terminal/server/internal/hermes"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
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
	if _, _, err := service.Store.Send(ctx, session.ID, id, "hermes-agent", service.Hermes.Scope(), "", "读一下", false); err != nil {
		t.Fatal(err)
	}
	// The idle timer has stopped synthesis but the stream has not ended yet:
	// joining it would only hear "cancelled".
	_, cancel := context.WithCancel(ctx)
	stale := &speechStream{id: model.NewID(), changed: make(chan struct{}), cancel: cancel, abandoned: true}
	message, err := service.Store.Message(ctx, session.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	key := speechKey(id, message.Attempts)
	service.speech.streams = map[string]*speechStream{key: stale}
	reader, err := service.OpenSpeech(ctx, session.ID, id, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.stream == stale || service.speech.streams[key] == stale {
		t.Fatal("new listener joined the abandoned stream")
	}
}

// A resume continues only the stream it started on. When that stream is gone
// (failed, evicted, a restart, re-synthesized at the same attempt, or a retry
// wrote a new reply) new audio need not match the bytes already played, so the
// resume is refused even if a replacement stream is already cached.
func TestSpeechResumeNeedsTheStreamItStartedOn(t *testing.T) {
	ctx := context.Background()
	service, session := lifecycleFixture(t, lifecycleTransport(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	service.TTS = tts.Fake{}
	if err := service.Store.SetSessionReady(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	id := model.NewID()
	if _, _, err := service.Store.Send(ctx, session.ID, id, "hermes-agent", service.Hermes.Scope(), "", "读一下", false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenSpeech(ctx, session.ID, id, "", 100); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume without a stream: %v", err)
	}
	if len(service.speech.streams) != 0 {
		t.Fatal("a refused resume started synthesis")
	}
	message, err := service.Store.Message(ctx, session.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	ended := func(status byte) *speechStream {
		stream := &speechStream{id: model.NewID(), changed: make(chan struct{}), cancel: func() {}}
		stream.append(frame(FrameAudio, []byte{1, 2, 3, 4}))
		stream.end(status)
		return stream
	}
	key := speechKey(id, message.Attempts)
	failed := ended(SpeechFailed)
	service.speech.streams = map[string]*speechStream{key: failed}
	if _, err := service.OpenSpeech(ctx, session.ID, id, failed.id, 3); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume into a failed stream: %v", err)
	}
	done := ended(SpeechDone)
	service.speech.streams = map[string]*speechStream{key: done}
	// Omitting the stream ID refuses to resume even when the stream exists.
	if _, err := service.OpenSpeech(ctx, session.ID, id, "", 3); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume without stream ID: %v", err)
	}
	reader, err := service.OpenSpeech(ctx, session.ID, id, done.id, 3)
	if err != nil || reader.stream != done || reader.StreamID() != done.id {
		t.Fatalf("resume into the cached stream: %v", err)
	}
	reader.Close()

	// Same attempt re-synthesized by another listener gets a new stream ID.
	reSynth := ended(SpeechDone)
	service.speech.streams[key] = reSynth
	if _, err := service.OpenSpeech(ctx, session.ID, id, done.id, 3); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume across same-attempt re-synthesis: %v", err)
	}
	if _, err := service.OpenSpeech(ctx, session.ID, id, "", 3); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume across same-attempt re-synthesis without ID: %v", err)
	}

	// Retry under the same message ID, with another listener creating attempt 1's stream.
	if err := service.Store.SetMessageState(ctx, id, "failed", "error", "upstream_failure"); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.Retry(ctx, session.ID, id, service.Hermes.Scope()); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.SetMessageState(ctx, id, "completed", "stop", ""); err != nil {
		t.Fatal(err)
	}
	replacement := ended(SpeechDone)
	service.speech.streams[speechKey(id, message.Attempts+1)] = replacement
	if _, err := service.OpenSpeech(ctx, session.ID, id, reSynth.id, 3); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume across a retry with replacement cached: %v", err)
	}
	if _, err := service.OpenSpeech(ctx, session.ID, id, "", 3); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume across a retry without ID: %v", err)
	}
	if got := service.speech.streams[speechKey(id, message.Attempts+1)]; got != replacement {
		t.Fatal("refused resume replaced the active replacement stream")
	}
}

func TestSpeechResumeRejectsEvictionAndGatewayRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sid := model.NewID()
	mid := model.NewID()
	if _, _, err := st.Send(ctx, sid, mid, "m", "scope", "", "读一下", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionReady(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendReply(ctx, mid, "回复内容"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageState(ctx, mid, "completed", "stop", ""); err != nil {
		t.Fatal(err)
	}

	service := New(st, config.Config{}, &hermes.Client{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.TTS = tts.Fake{}
	first, err := service.OpenSpeech(ctx, sid, mid, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.StreamID()
	if got := readAll(t, first); len(got) == 0 {
		t.Fatal("empty first speech stream")
	}
	first.Close()

	// Evict the finished stream by filling speechMaxStreams with newer finished streams.
	service.speech.mu.Lock()
	target := service.speech.streams[speechKey(mid, 0)]
	target.mu.Lock()
	target.finished = time.Now().Add(-time.Minute)
	target.mu.Unlock()
	for i := 0; i < speechMaxStreams-1; i++ {
		other := &speechStream{id: model.NewID(), changed: make(chan struct{}), cancel: func() {}, done: true, status: SpeechDone, finished: time.Now()}
		service.speech.streams[model.NewID()+"/0"] = other
	}
	service.pruneSpeechLocked()
	if service.speech.streams[speechKey(mid, 0)] != nil {
		service.speech.mu.Unlock()
		t.Fatal("oldest finished stream was not evicted")
	}
	service.speech.mu.Unlock()

	if _, err := service.OpenSpeech(ctx, sid, mid, firstID, 3); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume after eviction: %v", err)
	}
	// Another listener recreates the stream, then the evicted listener retries.
	recreated, err := service.OpenSpeech(ctx, sid, mid, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	recreated.Close()
	if _, err := service.OpenSpeech(ctx, sid, mid, firstID, 3); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume after eviction and recreation: %v", err)
	}

	// Gateway restart: new Service over same store, new listener recreates stream.
	restarted := New(st, config.Config{}, &hermes.Client{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restarted.TTS = tts.Fake{}
	fresh, err := restarted.OpenSpeech(ctx, sid, mid, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Close()
	if _, err := restarted.OpenSpeech(ctx, sid, mid, recreated.StreamID(), 3); !errors.Is(err, ErrSpeechRestarted) {
		t.Fatalf("resume across gateway restart: %v", err)
	}
}

func TestSpokenAudioIsShapedForTheSpeaker(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sid, mid := model.NewID(), model.NewID()
	if _, _, err := st.Send(ctx, sid, mid, "m", "scope", "", "读一下", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionReady(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendReply(ctx, mid, "回复内容"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageState(ctx, mid, "completed", "stop", ""); err != nil {
		t.Fatal(err)
	}
	speak := func(cfg config.Config) (samples int, peak int) {
		service := New(st, cfg, &hermes.Client{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		service.TTS = tts.Fake{}
		reader, err := service.OpenSpeech(ctx, sid, mid, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		stream := readAll(t, reader)
		for at := 0; at+3 <= len(stream); {
			kind, size := stream[at], int(stream[at+1])|int(stream[at+2])<<8
			if kind == FrameAudio {
				for i := at + 3; i+1 < at+3+size; i += 2 {
					v := int(int16(uint16(stream[i]) | uint16(stream[i+1])<<8))
					peak = max(peak, v, -v)
					samples++
				}
			}
			at += 3 + size
		}
		return samples, peak
	}
	// The fake reader's tones peak at 3000 (-20.8 dBFS).
	plain, plainPeak := speak(config.Config{})
	shaped, shapedPeak := speak(config.Config{SpeechHighPassHz: 200, SpeechCeilingDB: -30})
	if plainPeak < 2990 || plain == 0 {
		t.Fatalf("unshaped audio: %d samples, peak %d", plain, plainPeak)
	}
	// The limiter's look-ahead tail is released: no audio is lost.
	if shaped != plain {
		t.Fatalf("shaped %d samples, plain %d", shaped, plain)
	}
	if ceiling := 1036; shapedPeak > ceiling+1 { // 32767 at -30 dB
		t.Fatalf("shaped peak %d above the -30 dBFS ceiling %d", shapedPeak, ceiling)
	}
}
