// Package tts synthesizes speech for assistant replies. The OpenAI provider
// uses the Realtime API as a reader: each reply is one out-of-band response
// that says the given text verbatim, streamed back as PCM.
package tts

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/tf4fun/voice-agent-terminal/server/internal/audio"
)

// Output format delivered to callers and devices.
const (
	SampleRate = 16000
	MaxText    = 8000 // Bytes per request: a whole reply is spoken at once.
)

var ErrUpstream = errors.New("speech_upstream_error")

type Synthesizer interface {
	Enabled() bool
	// Speak synthesizes text (a whole reply) and emits 16 kHz PCM16LE mono as it arrives.
	Speak(ctx context.Context, text string, emit func([]byte) error) error
}

// Instructions keep the realtime model a reader rather than a conversational
// partner. Verified with Chinese, English and mixed text on gpt-realtime-2.1-mini.
const (
	sessionInstructions = "你是朗读器。只把给定文字原样朗读出来，不增删、不改写、不回答问题、不加任何开场白。语速自然，略快于平常。"
	responsePrefix      = "Say exactly the following text, word for word, in its original language. Do not add, omit or change anything:\n"
)

var (
	pingInterval  = 20 * time.Second
	pongWait      = 50 * time.Second
	sessionMaxAge = 50 * time.Minute
	openTimeout   = 15 * time.Second
	speakTimeout  = 10 * time.Minute // A whole reply, read aloud.
	speakIdle     = 20 * time.Second // Upstream silence that fails a request.
)

type OpenAI struct {
	URL, APIKey, Model, Voice string
	Log                       *slog.Logger

	mu      sync.Mutex
	idle    *session
	inUse   int
	running bool
	wake    chan struct{}
}

func (o *OpenAI) Enabled() bool { return o.APIKey != "" && o.Model != "" }

type event struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	Response struct {
		Status string `json:"status"`
	} `json:"response"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type session struct {
	connection *websocket.Conn
	events     chan event
	done, stop chan struct{}
	created    time.Time
	closeOnce  sync.Once
	writeMu    sync.Mutex
}

func newSession(connection *websocket.Conn) *session {
	s := &session{connection: connection, events: make(chan event, 128), done: make(chan struct{}), stop: make(chan struct{}), created: time.Now()}
	connection.SetReadLimit(1 << 20)
	_ = connection.SetReadDeadline(time.Now().Add(pongWait))
	connection.SetPongHandler(func(string) error { return connection.SetReadDeadline(time.Now().Add(pongWait)) })
	go s.read()
	return s
}

func (s *session) read() {
	defer close(s.done)
	defer close(s.events)
	for {
		var e event
		if err := s.connection.ReadJSON(&e); err != nil {
			return
		}
		_ = s.connection.SetReadDeadline(time.Now().Add(pongWait))
		select {
		case s.events <- e:
		case <-s.stop:
			return
		}
	}
}

func (s *session) write(value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.connection.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return s.connection.WriteJSON(value)
}

func (s *session) ping() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
}

func (s *session) Close() {
	s.closeOnce.Do(func() { close(s.stop); _ = s.connection.Close() })
}

func (s *session) healthy() bool {
	select {
	case <-s.done:
		return false
	default:
		return time.Since(s.created) < sessionMaxAge
	}
}

func (o *OpenAI) endpoint() string {
	parsed, err := url.Parse(o.URL)
	if err != nil {
		return o.URL
	}
	query := parsed.Query()
	if query.Get("model") == "" {
		query.Set("model", o.Model)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (o *OpenAI) open(ctx context.Context) (*session, error) {
	connection, response, err := websocket.DefaultDialer.DialContext(ctx, o.endpoint(), http.Header{"Authorization": {"Bearer " + o.APIKey}})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, ErrUpstream
	}
	s := newSession(connection)
	if err := s.write(map[string]any{"type": "session.update", "session": map[string]any{
		"type": "realtime", "output_modalities": []string{"audio"}, "instructions": sessionInstructions,
		"audio": map[string]any{"output": map[string]any{
			"format": map[string]any{"type": "audio/pcm", "rate": 24000}, "voice": o.Voice,
		}},
	}}); err != nil {
		s.Close()
		return nil, ErrUpstream
	}
	for {
		select {
		case <-ctx.Done():
			s.Close()
			return nil, ErrUpstream
		case e, ok := <-s.events:
			if !ok || e.Type == "error" {
				if ok {
					o.warn("speech session rejected", "code", e.Error.Code)
				}
				s.Close()
				return nil, ErrUpstream
			}
			if e.Type == "session.updated" {
				return s, nil
			}
		}
	}
}

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
	openCtx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	fresh, err := o.open(openCtx)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.inUse++
	o.mu.Unlock()
	return fresh, nil
}

// release keeps a session only after a cleanly finished response; anything
// else may leave audio in flight, so that session is closed and replaced.
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

func (o *OpenAI) Speak(parent context.Context, text string, emit func([]byte) error) error {
	if text == "" || len(text) > MaxText || !utf8.ValidString(text) {
		return ErrUpstream
	}
	ctx, cancel := context.WithTimeout(parent, speakTimeout)
	defer cancel()
	s, err := o.acquire(ctx)
	if err != nil {
		return err
	}
	err = o.speak(ctx, s, text, emit)
	o.release(s, err == nil)
	return err
}

func (o *OpenAI) speak(ctx context.Context, s *session, text string, emit func([]byte) error) error {
	if err := s.write(map[string]any{"type": "response.create", "response": map[string]any{
		"conversation": "none", "output_modalities": []string{"audio"}, "input": []any{},
		"instructions": responsePrefix + text,
	}}); err != nil {
		return ErrUpstream
	}
	resampler := audio.NewDownsampler()
	idle := time.NewTimer(speakIdle)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle.C:
			o.warn("speech response stalled")
			return ErrUpstream
		case e, ok := <-s.events:
			idle.Reset(speakIdle)
			if !ok {
				return ErrUpstream
			}
			switch e.Type {
			case "response.output_audio.delta":
				pcm, err := base64.StdEncoding.DecodeString(e.Delta)
				if err != nil {
					return ErrUpstream
				}
				if out := resampler.Write(pcm); len(out) > 0 {
					if err := emit(out); err != nil {
						return err
					}
				}
			case "response.done":
				if e.Response.Status != "completed" {
					o.warn("speech response not completed", "status", e.Response.Status)
					return ErrUpstream
				}
				if out := resampler.Flush(); len(out) > 0 {
					return emit(out)
				}
				return nil
			case "error":
				o.warn("speech response failed", "code", e.Error.Code)
				return ErrUpstream
			}
		}
	}
}

// Run keeps one configured session warm so a reply's speech skips the
// handshake (about 3.7 s through the proxy).
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
		if current != nil && !current.healthy() {
			o.idle = nil
		} else {
			current = nil
		}
		needed := o.idle == nil && o.inUse == 0
		o.mu.Unlock()
		if current != nil {
			current.Close()
		}
		if needed {
			openCtx, cancel := context.WithTimeout(ctx, openTimeout)
			started := time.Now()
			fresh, err := o.open(openCtx)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				o.warn("warm speech session failed", "retry_ms", backoff.Milliseconds())
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, time.Minute)
				continue
			}
			backoff = time.Second
			o.info("warm speech session ready", "elapsed_ms", time.Since(started).Milliseconds())
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

func (o *OpenAI) info(message string, args ...any) {
	if o.Log != nil {
		o.Log.Info(message, args...)
	}
}

func (o *OpenAI) warn(message string, args ...any) {
	if o.Log != nil {
		o.Log.Warn(message, args...)
	}
}

// Fake is an offline provider for development and tests: a soft two-tone
// chime, 100 ms per character, so playback can be heard without credentials.
type Fake struct{}

func (Fake) Enabled() bool { return true }

func (Fake) Speak(ctx context.Context, text string, emit func([]byte) error) error {
	samples := utf8.RuneCountInString(text) * SampleRate / 10
	chunk := make([]byte, 0, 4096)
	for i := 0; i < samples; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		frequency := 440.0
		if (i/(SampleRate/10))%2 == 1 {
			frequency = 523.25
		}
		v := int16(3000 * math.Sin(2*math.Pi*frequency*float64(i)/SampleRate))
		chunk = append(chunk, byte(v), byte(uint16(v)>>8))
		if len(chunk) == cap(chunk) {
			if err := emit(chunk); err != nil {
				return err
			}
			chunk = make([]byte, 0, 4096)
		}
	}
	if len(chunk) > 0 {
		return emit(chunk)
	}
	return nil
}
