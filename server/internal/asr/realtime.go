package asr

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/tf4fun/voice-agent-terminal/server/internal/audio"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

var ErrUpstream = errors.New("transcription_upstream_error")

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

// Keep-alive and rotation for warm sessions. The realtime API documents a
// bounded session lifetime, so a warm session is replaced well before it.
var (
	pingInterval  = 20 * time.Second
	pongWait      = 50 * time.Second
	sessionMaxAge = 50 * time.Minute
	openTimeout   = 15 * time.Second
)

// OpenAI transcribes through the realtime API. One transcription session
// handles many turns: each recording clears the input buffer, appends audio,
// commits once and matches its transcript by item_id. Run keeps one
// configured session warm so recordings skip the TLS and session handshake.
type OpenAI struct {
	URL, APIKey, Model string
	Log                *slog.Logger

	mu      sync.Mutex
	idle    *session
	inUse   int // Sessions checked out to recordings or reservations.
	running bool
	wake    chan struct{}
}

func (o *OpenAI) Enabled() bool { return o.APIKey != "" }

type realtimeEvent struct {
	Type       string `json:"type"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
}

// session owns one WebSocket. Its reader goroutine is the only reader: it
// answers pings, extends the read deadline on pongs and forwards events.
type session struct {
	connection *websocket.Conn
	events     chan realtimeEvent
	done       chan struct{}
	stop       chan struct{}
	created    time.Time
	closeOnce  sync.Once
}

func newSession(connection *websocket.Conn) *session {
	s := &session{
		connection: connection,
		events:     make(chan realtimeEvent, 64),
		done:       make(chan struct{}),
		stop:       make(chan struct{}),
		created:    time.Now(),
	}
	connection.SetReadLimit(512 << 10)
	_ = connection.SetReadDeadline(time.Now().Add(pongWait))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(pongWait))
	})
	go s.read()
	return s
}

func (s *session) read() {
	defer close(s.done)
	defer close(s.events)
	for {
		var event realtimeEvent
		if err := s.connection.ReadJSON(&event); err != nil {
			return
		}
		_ = s.connection.SetReadDeadline(time.Now().Add(pongWait))
		select {
		case s.events <- event:
		case <-s.stop:
			return
		}
	}
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.stop)
		_ = s.connection.Close()
	})
	return nil
}

func (s *session) healthy() bool {
	select {
	case <-s.done:
		return false
	default:
		return time.Since(s.created) < sessionMaxAge
	}
}

func (s *session) ping() error {
	return s.connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
}

// write is used by one goroutine at a time: open, then each turn's writer.
func (s *session) write(value any) error {
	_ = s.connection.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return s.connection.WriteJSON(value)
}

// open dials, configures transcription and waits for session.updated.
func (o *OpenAI) open(ctx context.Context) (*session, error) {
	connection, response, err := websocket.DefaultDialer.DialContext(ctx, o.URL, http.Header{"Authorization": {"Bearer " + o.APIKey}})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, ErrUpstream
	}
	s := newSession(connection)
	if err := s.write(map[string]any{
		"type": "session.update", "session": map[string]any{
			"type": "transcription", "audio": map[string]any{"input": map[string]any{
				"format":         map[string]any{"type": "audio/pcm", "rate": 24000},
				"transcription":  map[string]any{"model": o.Model, "languages": []string{"zh"}, "delay": "low"},
				"turn_detection": nil,
			}},
		},
	}); err != nil {
		s.Close()
		return nil, ErrUpstream
	}
	for {
		select {
		case <-ctx.Done():
			s.Close()
			return nil, ErrUpstream
		case event, ok := <-s.events:
			if !ok || event.Type == "error" {
				s.Close()
				return nil, ErrUpstream
			}
			if event.Type == "session.updated" {
				return s, nil
			}
		}
	}
}

// signal asks Run to re-evaluate the pool without blocking.
func (o *OpenAI) signal() {
	o.mu.Lock()
	wake := o.wake
	o.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// acquire checks out the warm session or, when none is ready, opens one.
func (o *OpenAI) acquire(ctx context.Context) (*session, error) {
	o.mu.Lock()
	s := o.idle
	o.idle = nil
	if s != nil && s.healthy() {
		o.inUse++
		o.mu.Unlock()
		return s, nil
	}
	o.mu.Unlock()
	if s != nil {
		s.Close()
		o.signal()
	}
	fresh, err := o.open(ctx)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.inUse++
	o.mu.Unlock()
	return fresh, nil
}

// release checks a session back in: a clean, healthy one becomes the warm
// session, anything else is closed and Run opens a replacement.
func (o *OpenAI) release(s *session, clean bool) {
	o.mu.Lock()
	o.inUse--
	keep := clean && s.healthy() && o.running && o.idle == nil
	if keep {
		o.idle = s
	}
	o.mu.Unlock()
	if !keep {
		s.Close()
	}
	o.signal()
}

// Run keeps one configured session warm until ctx ends: it reconnects with
// backoff, pings the idle session and replaces it before its maximum age.
func (o *OpenAI) Run(ctx context.Context) {
	o.mu.Lock()
	o.running = true
	if o.wake == nil {
		o.wake = make(chan struct{}, 1)
	}
	wake := o.wake
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		s := o.idle
		o.idle = nil
		o.running = false
		o.mu.Unlock()
		if s != nil {
			s.Close()
		}
	}()
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	backoff := time.Second
	for ctx.Err() == nil {
		o.mu.Lock()
		current := o.idle
		stale := current != nil && !current.healthy()
		if stale {
			o.idle = nil
		}
		o.mu.Unlock()
		if stale {
			current.Close()
		}
		o.mu.Lock()
		needed := o.idle == nil && o.inUse == 0
		o.mu.Unlock()
		if needed {
			openCtx, cancel := context.WithTimeout(ctx, openTimeout)
			started := time.Now()
			fresh, err := o.open(openCtx)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				o.logWarn("warm transcription session failed", "retry_ms", backoff.Milliseconds())
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, time.Minute)
				continue
			}
			backoff = time.Second
			o.logInfo("warm transcription session ready", "elapsed_ms", time.Since(started).Milliseconds())
			o.mu.Lock()
			if o.idle == nil && o.inUse == 0 {
				o.idle, fresh = fresh, nil
			}
			o.mu.Unlock()
			if fresh != nil {
				fresh.Close()
			}
		}
		var dropped <-chan struct{}
		o.mu.Lock()
		if o.idle != nil {
			dropped = o.idle.done
		}
		o.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-dropped:
		case <-wake:
		case <-ticker.C:
			o.mu.Lock()
			idle := o.idle
			o.mu.Unlock()
			if idle != nil && idle.ping() != nil {
				idle.Close()
			}
		}
	}
}

func (o *OpenAI) logInfo(message string, args ...any) {
	if o.Log != nil {
		o.Log.Info(message, args...)
	}
}

func (o *OpenAI) logWarn(message string, args ...any) {
	if o.Log != nil {
		o.Log.Warn(message, args...)
	}
}

type openAIPrepared struct {
	owner   *OpenAI
	session *session
	once    sync.Once
}

// Close returns an unused prepared session to the warm pool.
func (p *openAIPrepared) Close() error {
	p.once.Do(func() { p.owner.release(p.session, true) })
	return nil
}

func (o *OpenAI) Prepare(ctx context.Context) (Prepared, error) {
	s, err := o.acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &openAIPrepared{owner: o, session: s}, nil
}

func (o *OpenAI) Transcribe(parent context.Context, input io.ReadCloser, rate int, emit func(Update) error) error {
	// Keep draining the device upload while an upstream session is acquired.
	input = newPrefetch(input, PrefetchBytes)
	defer input.Close()
	s, err := o.acquire(parent)
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
	var s *session
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
func (o *OpenAI) finish(s *session, err error) error {
	o.release(s, err == nil)
	return err
}

// stream sends one recording over a configured session and returns after the
// final transcript of the item it committed.
func (o *OpenAI) stream(parent context.Context, s *session, input io.ReadCloser, rate int, emit func(Update) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { input.Close() })
	defer stopClose()
	if err := s.write(map[string]string{"type": "input_audio_buffer.clear"}); err != nil {
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
			return s.write(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(chunk)})
		})
		if err == nil {
			err = s.write(map[string]string{"type": "input_audio_buffer.commit"})
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
		case event, ok = <-s.events:
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
			if len(text)+len(event.Delta) > model.MaxTranscriptBytes || !utf8.ValidString(event.Delta) {
				return ErrUpstream
			}
			text += event.Delta
			if err := emit(Update{Text: text}); err != nil {
				return err
			}
		case "conversation.item.input_audio_transcription.completed":
			if len(event.Transcript) > model.MaxTranscriptBytes || !utf8.ValidString(event.Transcript) {
				return ErrUpstream
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
