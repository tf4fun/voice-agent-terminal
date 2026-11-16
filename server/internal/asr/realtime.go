package asr

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/tf4fun/voice-agent-terminal/server/internal/audio"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/realtime"
)

var (
	ErrUpstream = errors.New("transcription_upstream_error")
	ErrLimit    = errors.New("transcript_limit_exceeded")
)

type Update struct {
	Text  string
	Final bool
}

type Transcriber interface {
	Enabled() bool
	Transcribe(context.Context, io.ReadCloser, int, func(Update) error) error
}

// Prepared is an upstream session reserved before its audio upload arrives.
// Closing an unused one returns it to the warm pool when it is still healthy.
type Prepared interface{ Close() error }

// Preparer reserves the upstream session while the device shows its preparing
// animation, so recording starts against a ready transcriber.
type Preparer interface {
	Prepare(context.Context) (Prepared, error)
	TranscribePrepared(context.Context, Prepared, io.ReadCloser, int, func(Update) error) error
}

// OpenAI transcribes through the realtime API. One transcription session
// handles many turns: each recording clears the input buffer, appends audio,
// commits once and matches its transcript by item_id. Run keeps one
// configured session warm so recordings skip the TLS and session handshake.
type OpenAI struct {
	URL, APIKey, Model string
	Log                *slog.Logger

	once sync.Once
	pool *realtime.Pool[realtimeEvent]
}

func (o *OpenAI) Enabled() bool { return o.APIKey != "" }

type realtimeEvent struct {
	Type       string `json:"type"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
}

func (o *OpenAI) getPool() *realtime.Pool[realtimeEvent] {
	o.once.Do(func() {
		o.pool = &realtime.Pool[realtimeEvent]{
			Name: "transcription",
			Open: o.open,
			Log:  o.Log,
		}
	})
	return o.pool
}

func (o *OpenAI) hasWarmSession() bool { return o.getPool().HasIdle() }

// open dials, configures transcription and waits for session.updated.
func (o *OpenAI) open(ctx context.Context) (*realtime.Conn[realtimeEvent], error) {
	connection, response, err := websocket.DefaultDialer.DialContext(ctx, o.URL, http.Header{"Authorization": {"Bearer " + o.APIKey}})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, ErrUpstream
	}
	s := realtime.NewConn[realtimeEvent](connection, 512<<10, 64, realtime.DefaultPongWait, realtime.DefaultMaxAge)
	if err := s.Write(map[string]any{
		"type": "session.update", "session": map[string]any{
			"type": "transcription", "audio": map[string]any{"input": map[string]any{
				"format":         map[string]any{"type": "audio/pcm", "rate": 24000},
				"transcription":  map[string]any{"model": o.Model, "languages": []string{"zh"}, "delay": "low"},
				"turn_detection": nil,
			}},
		},
	}); err != nil {
		_ = s.Close()
		return nil, ErrUpstream
	}
	for {
		select {
		case <-ctx.Done():
			_ = s.Close()
			return nil, ErrUpstream
		case event, ok := <-s.Events:
			if !ok || event.Type == "error" {
				_ = s.Close()
				return nil, ErrUpstream
			}
			if event.Type == "session.updated" {
				return s, nil
			}
		}
	}
}

// Run keeps one configured session warm until ctx ends.
func (o *OpenAI) Run(ctx context.Context) { o.getPool().Run(ctx) }

type openAIPrepared struct {
	owner   *OpenAI
	session *realtime.Conn[realtimeEvent]
	once    sync.Once
}

// Close returns an unused prepared session to the warm pool.
func (p *openAIPrepared) Close() error {
	p.once.Do(func() { p.owner.getPool().Release(p.session, true) })
	return nil
}

func (o *OpenAI) Prepare(ctx context.Context) (Prepared, error) {
	s, err := o.getPool().Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &openAIPrepared{owner: o, session: s}, nil
}

func (o *OpenAI) Transcribe(parent context.Context, input io.ReadCloser, rate int, emit func(Update) error) error {
	// Keep draining the device upload while an upstream session is acquired.
	input = newPrefetch(input, PrefetchBytes)
	defer input.Close()
	s, err := o.getPool().Acquire(parent)
	if err != nil {
		return err
	}
	return o.finish(s, o.stream(parent, s, input, rate, emit))
}

func (o *OpenAI) TranscribePrepared(parent context.Context, prepared Prepared, input io.ReadCloser, rate int, emit func(Update) error) error {
	reserved, ok := prepared.(*openAIPrepared)
	if !ok || reserved.owner != o {
		_ = prepared.Close()
		return o.Transcribe(parent, input, rate, emit)
	}
	var s *realtime.Conn[realtimeEvent]
	reserved.once.Do(func() { s = reserved.session })
	if s == nil {
		return o.Transcribe(parent, input, rate, emit)
	}
	input = newPrefetch(input, PrefetchBytes)
	defer input.Close()
	return o.finish(s, o.stream(parent, s, input, rate, emit))
}

// finish keeps a session only after a clean turn; any failure or cancellation
// may leave audio or an in-flight item behind, so that session is discarded.
func (o *OpenAI) finish(s *realtime.Conn[realtimeEvent], err error) error {
	o.getPool().Release(s, err == nil)
	return err
}

// stream sends one recording over a configured session and returns after the
// final transcript of the item it committed.
func (o *OpenAI) stream(parent context.Context, s *realtime.Conn[realtimeEvent], input io.ReadCloser, rate int, emit func(Update) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { input.Close() })
	defer stopClose()
	if err := s.Write(map[string]string{"type": "input_audio_buffer.clear"}); err != nil {
		return ErrUpstream
	}
	writerResult := make(chan error, 1)
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		err := audio.StreamPCM(input, rate, func(chunk []byte) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return s.Write(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(chunk)})
		})
		if err == nil {
			err = s.Write(map[string]string{"type": "input_audio_buffer.commit"})
		}
		writerResult <- err
	}()
	defer func() { cancel(); input.Close(); writer.Wait() }()
	results := writerResult
	itemID, text := "", ""
	for {
		var event realtimeEvent
		var ok bool
		select {
		case <-ctx.Done():
			return ErrUpstream
		case err := <-results:
			if err != nil {
				return err
			}
			results = nil
			continue
		case event, ok = <-s.Events:
			if !ok {
				return ErrUpstream
			}
		}
		switch event.Type {
		case "error":
			return ErrUpstream
		case "input_audio_buffer.committed", "conversation.item.input_audio_transcription.delta",
			"conversation.item.input_audio_transcription.completed", "conversation.item.input_audio_transcription.failed":
			if event.ItemID == "" {
				return ErrUpstream
			}
			// The first item after this turn's clear is ours. Events of other
			// items are ignored; ordering across turns is not guaranteed.
			if itemID == "" {
				itemID = event.ItemID
			} else if itemID != event.ItemID {
				continue
			}
		default:
			continue
		}
		switch event.Type {
		case "conversation.item.input_audio_transcription.failed":
			return ErrUpstream
		case "conversation.item.input_audio_transcription.delta":
			if !utf8.ValidString(event.Delta) {
				return ErrUpstream
			}
			if len(text)+len(event.Delta) > model.MaxTranscriptBytes {
				return ErrLimit
			}
			text += event.Delta
			if err := emit(Update{Text: text}); err != nil {
				return err
			}
		case "conversation.item.input_audio_transcription.completed":
			if !utf8.ValidString(event.Transcript) {
				return ErrUpstream
			}
			if len(event.Transcript) > model.MaxTranscriptBytes {
				return ErrLimit
			}
			return emit(Update{Text: event.Transcript, Final: true})
		}
	}
}

// Fake is an explicit offline development provider and still consumes the audio stream.
type Fake struct{}

func (*Fake) Enabled() bool { return true }
func (*Fake) Transcribe(ctx context.Context, input io.ReadCloser, rate int, emit func(Update) error) error {
	count := 0
	err := audio.StreamPCM(input, rate, func(chunk []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count += len(chunk)
		return emit(Update{Text: "测试转写"})
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return audio.ErrPCM
	}
	return emit(Update{Text: "测试转写", Final: true})
}
