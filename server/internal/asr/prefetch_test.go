package asr

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A cold upstream handshake must not stall the device upload: the gateway
// keeps reading audio while it waits for session.updated.
func TestSlowHandshakeStillDrainsUpload(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		var setup any
		_ = connection.ReadJSON(&setup)
		<-release
		_ = connection.WriteJSON(map[string]string{"type": "session.updated"})
		for {
			var event struct {
				Type string `json:"type"`
			}
			if err := connection.ReadJSON(&event); err != nil {
				return
			}
			if event.Type == "input_audio_buffer.commit" {
				_ = connection.WriteJSON(map[string]string{"type": "input_audio_buffer.committed", "item_id": "item"})
				_ = connection.WriteJSON(map[string]string{"type": "conversation.item.input_audio_transcription.completed",
					"item_id": "item", "transcript": "你好"})
				return
			}
		}
	}))
	defer upstream.Close()
	client := OpenAI{URL: "ws" + strings.TrimPrefix(upstream.URL, "http"), APIKey: "test", Model: "gpt-live-transcribe"}
	reader, writer := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	var final atomic.Bool
	go func() {
		done <- client.Transcribe(ctx, reader, 16000, func(update Update) error {
			if update.Final && update.Text == "你好" {
				final.Store(true)
			}
			return nil
		})
	}()
	// Three seconds of 16 kHz PCM16, more than a socket buffer would absorb.
	written := make(chan error, 1)
	go func() {
		_, err := writer.Write(make([]byte, 96000))
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upload stalled while the upstream session was opening")
	}
	close(release)
	writer.Close()
	if err := <-done; err != nil || !final.Load() {
		t.Fatalf("transcription after slow handshake: %v final=%v", err, final.Load())
	}
}

type countingReader struct{ total atomic.Int64 }

func (c *countingReader) Read(buffer []byte) (int, error) {
	c.total.Add(int64(len(buffer)))
	return len(buffer), nil
}

func TestPrefetchBoundsBufferedAudio(t *testing.T) {
	source := &countingReader{}
	p := newPrefetch(io.NopCloser(source), 8<<10)
	time.Sleep(50 * time.Millisecond)
	// Buffered slots plus the chunk the goroutine holds while blocked.
	if read := source.total.Load(); read > 8<<10+prefetchChunk {
		t.Fatalf("prefetch read %d bytes beyond its bound", read)
	}
	buffer := make([]byte, 1000)
	if n, err := p.Read(buffer); n != len(buffer) || err != nil {
		t.Fatalf("read buffered audio: %d %v", n, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Read(make([]byte, 1<<20)); err == nil {
		// Drain remaining buffered chunks; the stream must end with an error.
		for err == nil {
			_, err = p.Read(make([]byte, 1<<20))
		}
	}
}

func TestPrefetchCloseStopsBlockedSource(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	p := newPrefetch(reader, PrefetchBytes)
	closed := make(chan struct{})
	go func() { _ = p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not stop the reader goroutine")
	}
	if _, err := p.Read(make([]byte, 16)); err == nil {
		t.Fatal("read after close succeeded")
	}
}

// A session prepared during the device's preparing screen carries the upload
// without a second handshake.
func TestPreparedSessionTranscribesOnOneConnection(t *testing.T) {
	var connections atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connections.Add(1)
		defer connection.Close()
		var setup any
		_ = connection.ReadJSON(&setup)
		_ = connection.WriteJSON(map[string]string{"type": "session.updated"})
		for {
			var event struct {
				Type string `json:"type"`
			}
			if err := connection.ReadJSON(&event); err != nil {
				return
			}
			if event.Type == "input_audio_buffer.commit" {
				_ = connection.WriteJSON(map[string]string{"type": "input_audio_buffer.committed", "item_id": "item"})
				_ = connection.WriteJSON(map[string]string{"type": "conversation.item.input_audio_transcription.completed",
					"item_id": "item", "transcript": "准备好了"})
				return
			}
		}
	}))
	defer upstream.Close()
	client := OpenAI{URL: "ws" + strings.TrimPrefix(upstream.URL, "http"), APIKey: "test", Model: "gpt-live-transcribe"}
	prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), 2*time.Second)
	prepared, err := client.Prepare(prepareCtx)
	cancelPrepare() // The prepared session must outlive its preparation deadline.
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var text string
	err = client.TranscribePrepared(ctx, prepared, io.NopCloser(strings.NewReader(string(make([]byte, 6400)))), 16000,
		func(update Update) error {
			if update.Final {
				text = update.Text
			}
			return nil
		})
	if err != nil || text != "准备好了" || connections.Load() != 1 {
		t.Fatalf("prepared transcription: %v %q connections=%d", err, text, connections.Load())
	}
}
