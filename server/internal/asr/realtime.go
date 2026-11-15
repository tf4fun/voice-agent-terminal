package asr

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
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

type OpenAI struct{ URL, APIKey, Model string }

func (o *OpenAI) Enabled() bool { return o.APIKey != "" }

// Prepared is an upstream session opened before its audio upload arrives.
type Prepared interface{ Close() error }

// Preparer opens the upstream session while the device shows its preparing
// animation, so recording starts against a ready transcriber.
type Preparer interface {
	Prepare(context.Context) (Prepared, error)
	TranscribePrepared(context.Context, Prepared, io.ReadCloser, int, func(Update) error) error
}

type openAIPrepared struct{ connection *websocket.Conn }

func (p *openAIPrepared) Close() error { return p.connection.Close() }

func writeEvent(connection *websocket.Conn, value any) error {
	_ = connection.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return connection.WriteJSON(value)
}

// open dials, configures transcription and waits for session.updated.
func (o *OpenAI) open(ctx context.Context) (*websocket.Conn, error) {
	connection, response, err := websocket.DefaultDialer.DialContext(ctx, o.URL, http.Header{"Authorization": {"Bearer " + o.APIKey}})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, ErrUpstream
	}
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	fail := func() (*websocket.Conn, error) {
		stop()
		connection.Close()
		return nil, ErrUpstream
	}
	connection.SetReadLimit(512 << 10)
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetReadDeadline(deadline)
	}
	if err := writeEvent(connection, map[string]any{
		"type": "session.update", "session": map[string]any{
			"type": "transcription", "audio": map[string]any{"input": map[string]any{
				"format":         map[string]any{"type": "audio/pcm", "rate": 24000},
				"transcription":  map[string]any{"model": o.Model, "languages": []string{"zh"}, "delay": "low"},
				"turn_detection": nil,
			}},
		},
	}); err != nil {
		return fail()
	}
	for {
		var event struct {
			Type string `json:"type"`
		}
		if err := connection.ReadJSON(&event); err != nil || event.Type == "error" {
			return fail()
		}
		if event.Type == "session.updated" {
			break
		}
	}
	// A cancelled context has already closed the connection.
	if !stop() {
		connection.Close()
		return nil, ErrUpstream
	}
	_ = connection.SetReadDeadline(time.Time{})
	return connection, nil
}

func (o *OpenAI) Prepare(ctx context.Context) (Prepared, error) {
	connection, err := o.open(ctx)
	if err != nil {
		return nil, err
	}
	return &openAIPrepared{connection: connection}, nil
}

func (o *OpenAI) Transcribe(parent context.Context, input io.ReadCloser, rate int, emit func(Update) error) error {
	// Keep draining the device upload while the upstream session opens.
	input = newPrefetch(input, PrefetchBytes)
	defer input.Close()
	connection, err := o.open(parent)
	if err != nil {
		return err
	}
	return o.stream(parent, connection, input, rate, emit)
}

func (o *OpenAI) TranscribePrepared(parent context.Context, prepared Prepared, input io.ReadCloser, rate int, emit func(Update) error) error {
	session, ok := prepared.(*openAIPrepared)
	if !ok {
		_ = prepared.Close()
		return o.Transcribe(parent, input, rate, emit)
	}
	input = newPrefetch(input, PrefetchBytes)
	defer input.Close()
	return o.stream(parent, session.connection, input, rate, emit)
}

// stream sends audio over a configured session and returns the final transcript.
func (o *OpenAI) stream(parent context.Context, connection *websocket.Conn, input io.ReadCloser, rate int, emit func(Update) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer connection.Close()
	stopClose := context.AfterFunc(ctx, func() { connection.Close(); input.Close() })
	defer stopClose()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetReadDeadline(deadline)
	}
	write := func(value any) error { return writeEvent(connection, value) }
	writerResult := make(chan error, 1)
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		err := audio.StreamPCM(input, rate, func(chunk []byte) error {
			return write(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(chunk)})
		})
		if err == nil {
			err = write(map[string]string{"type": "input_audio_buffer.commit"})
		}
		writerResult <- err
		if err != nil {
			connection.Close()
		}
	}()
	defer func() { cancel(); input.Close(); connection.Close(); writer.Wait() }()
	itemID, text := "", ""
	for {
		var event struct {
			Type       string `json:"type"`
			ItemID     string `json:"item_id"`
			Delta      string `json:"delta"`
			Transcript string `json:"transcript"`
		}
		if err := connection.ReadJSON(&event); err != nil {
			select {
			case writeErr := <-writerResult:
				if writeErr != nil {
					return writeErr
				}
			default:
			}
			return ErrUpstream
		}
		switch event.Type {
		case "error", "conversation.item.input_audio_transcription.failed":
			return ErrUpstream
		case "input_audio_buffer.committed", "conversation.item.input_audio_transcription.delta", "conversation.item.input_audio_transcription.completed":
			if event.ItemID == "" {
				return ErrUpstream
			}
			if itemID != "" && itemID != event.ItemID {
				return ErrUpstream
			}
			itemID = event.ItemID
		default:
			continue
		}
		if event.Type == "conversation.item.input_audio_transcription.delta" {
			if len(text)+len(event.Delta) > model.MaxDraftBytes || !utf8.ValidString(event.Delta) {
				return ErrUpstream
			}
			text += event.Delta
			if err := emit(Update{Text: text}); err != nil {
				return err
			}
		}
		if event.Type == "conversation.item.input_audio_transcription.completed" {
			if len(event.Transcript) > model.MaxDraftBytes || !utf8.ValidString(event.Transcript) {
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
