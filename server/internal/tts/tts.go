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
	"github.com/tf4fun/voice-agent-terminal/server/internal/realtime"
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
	speakTimeout = 10 * time.Minute // A whole reply, read aloud.
	speakIdle    = 20 * time.Second // Upstream silence that fails a request.
)

type OpenAI struct {
	URL, APIKey, Model, Voice string
	Log                       *slog.Logger

	once sync.Once
	pool *realtime.Pool[event]
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

func (o *OpenAI) getPool() *realtime.Pool[event] {
	o.once.Do(func() {
		o.pool = &realtime.Pool[event]{
			Name: "speech",
			Open: o.open,
			Log:  o.Log,
		}
	})
	return o.pool
}

func (o *OpenAI) hasWarmSession() bool { return o.getPool().HasIdle() }

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

func (o *OpenAI) open(ctx context.Context) (*realtime.Conn[event], error) {
	connection, response, err := websocket.DefaultDialer.DialContext(ctx, o.endpoint(), http.Header{"Authorization": {"Bearer " + o.APIKey}})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, ErrUpstream
	}
	s := realtime.NewConn[event](connection, 1<<20, 128, realtime.DefaultPongWait, realtime.DefaultMaxAge)
	if err := s.Write(map[string]any{"type": "session.update", "session": map[string]any{
		"type": "realtime", "output_modalities": []string{"audio"}, "instructions": sessionInstructions,
		"audio": map[string]any{"output": map[string]any{
			"format": map[string]any{"type": "audio/pcm", "rate": 24000}, "voice": o.Voice,
		}},
	}}); err != nil {
		_ = s.Close()
		return nil, ErrUpstream
	}
	for {
		select {
		case <-ctx.Done():
			_ = s.Close()
			return nil, ErrUpstream
		case e, ok := <-s.Events:
			if !ok || e.Type == "error" {
				if ok {
					o.warn("speech session rejected", "code", e.Error.Code)
				}
				_ = s.Close()
				return nil, ErrUpstream
			}
			if e.Type == "session.updated" {
				return s, nil
			}
		}
	}
}

func (o *OpenAI) Speak(parent context.Context, text string, emit func([]byte) error) error {
	if text == "" || len(text) > MaxText || !utf8.ValidString(text) {
		return ErrUpstream
	}
	ctx, cancel := context.WithTimeout(parent, speakTimeout)
	defer cancel()
	s, err := o.getPool().Acquire(ctx)
	if err != nil {
		return err
	}
	err = o.speak(ctx, s, text, emit)
	o.getPool().Release(s, err == nil)
	return err
}

func (o *OpenAI) speak(ctx context.Context, s *realtime.Conn[event], text string, emit func([]byte) error) error {
	if err := s.Write(map[string]any{"type": "response.create", "response": map[string]any{
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
		case e, ok := <-s.Events:
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
func (o *OpenAI) Run(ctx context.Context) { o.getPool().Run(ctx) }

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
