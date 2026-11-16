package asr

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func TestRealtimeTranscribesBeforeAudioEOFAndCommitsOneItem(t *testing.T) {
	serverErrors := make(chan error, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authentication")
		}
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer connection.Close()
		var setup struct {
			Type    string `json:"type"`
			Session struct {
				Type  string `json:"type"`
				Audio struct {
					Input struct {
						Format struct {
							Rate int `json:"rate"`
						} `json:"format"`
						Transcription struct {
							Model string `json:"model"`
						} `json:"transcription"`
					} `json:"input"`
				} `json:"audio"`
			} `json:"session"`
		}
		if err := connection.ReadJSON(&setup); err != nil {
			serverErrors <- err
			return
		}
		if setup.Type != "session.update" || setup.Session.Type != "transcription" || setup.Session.Audio.Input.Format.Rate != 24000 || setup.Session.Audio.Input.Transcription.Model != "gpt-live-transcribe" {
			t.Error("invalid transcription setup")
		}
		_ = connection.WriteJSON(map[string]string{"type": "session.updated"})
		bytesReceived := 0
		for {
			var event struct {
				Type  string `json:"type"`
				Audio string `json:"audio"`
			}
			if err := connection.ReadJSON(&event); err != nil {
				serverErrors <- err
				return
			}
			if event.Type == "input_audio_buffer.append" {
				pcm, err := base64.StdEncoding.DecodeString(event.Audio)
				if err != nil {
					serverErrors <- err
					return
				}
				bytesReceived += len(pcm)
				if bytesReceived == len(pcm) {
					_ = connection.WriteJSON(map[string]string{"type": "conversation.item.input_audio_transcription.delta", "item_id": "item-1", "delta": "你好"})
				}
			}
			if event.Type == "input_audio_buffer.commit" {
				if bytesReceived != 9600 {
					t.Errorf("resampled bytes %d", bytesReceived)
				}
				_ = connection.WriteJSON(map[string]string{"type": "input_audio_buffer.committed", "item_id": "item-1"})
				_ = connection.WriteJSON(map[string]string{"type": "conversation.item.input_audio_transcription.completed", "item_id": "item-1", "transcript": "你好，世界"})
				serverErrors <- nil
				return
			}
		}
	}))
	defer upstream.Close()
	client := OpenAI{URL: "ws" + strings.TrimPrefix(upstream.URL, "http"), APIKey: "test-key", Model: "gpt-live-transcribe"}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	updates := make(chan Update, 4)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		done <- client.Transcribe(ctx, reader, 16000, func(update Update) error { updates <- update; return nil })
	}()
	if _, err := writer.Write(make([]byte, 6400)); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-updates:
		if update.Final || update.Text != "你好" {
			t.Fatal(update)
		}
	case <-ctx.Done():
		t.Fatal("partial transcription waited for EOF")
	}
	_ = writer.Close()
	select {
	case update := <-updates:
		if !update.Final || update.Text != "你好，世界" {
			t.Fatal(update)
		}
	case <-ctx.Done():
		t.Fatal("missing final transcription")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
}

func TestCancellationClosesBlockedAudioRead(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		var setup any
		_ = connection.ReadJSON(&setup)
		_ = connection.WriteJSON(map[string]string{"type": "session.updated"})
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	client := OpenAI{URL: "ws" + strings.TrimPrefix(upstream.URL, "http"), APIKey: "test", Model: "gpt-live-transcribe"}
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Transcribe(ctx, reader, 24000, func(Update) error { return nil }) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded unexpectedly")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("audio worker did not stop")
	}
}

func TestOversizedTranscriptReturnsErrLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(*websocket.Conn)
	}{
		{
			name: "delta_overflow",
			send: func(c *websocket.Conn) {
				_ = c.WriteJSON(map[string]string{
					"type":    "conversation.item.input_audio_transcription.delta",
					"item_id": "item-1",
					"delta":   strings.Repeat("a", model.MaxTranscriptBytes+1),
				})
			},
		},
		{
			name: "completed_overflow",
			send: func(c *websocket.Conn) {
				_ = c.WriteJSON(map[string]string{
					"type":       "conversation.item.input_audio_transcription.completed",
					"item_id":    "item-1",
					"transcript": strings.Repeat("b", model.MaxTranscriptBytes+1),
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				var setup any
				_ = conn.ReadJSON(&setup)
				_ = conn.WriteJSON(map[string]string{"type": "session.updated"})
				for {
					var event struct {
						Type string `json:"type"`
					}
					if conn.ReadJSON(&event) != nil {
						return
					}
					if event.Type == "input_audio_buffer.commit" {
						tc.send(conn)
						return
					}
				}
			}))
			defer upstream.Close()
			client := OpenAI{URL: "ws" + strings.TrimPrefix(upstream.URL, "http"), APIKey: "test", Model: "gpt-live-transcribe"}
			err := client.Transcribe(context.Background(), io.NopCloser(strings.NewReader(strings.Repeat("\x00", 6400))), 16000, func(Update) error { return nil })
			if !errors.Is(err, ErrLimit) {
				t.Fatalf("got err=%v, want ErrLimit", err)
			}
		})
	}
}
