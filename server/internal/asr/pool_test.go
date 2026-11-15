package asr

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeRealtime serves transcription sessions that commit one item per turn.
// It also sends a stale completion for an earlier item before each new one,
// since ordering across turns is not guaranteed.
type fakeRealtime struct {
	connections atomic.Int32
	turns       atomic.Int32
	dropIdle    atomic.Bool
	failNext    atomic.Bool
}

func (f *fakeRealtime) serve(w http.ResponseWriter, r *http.Request) {
	connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	f.connections.Add(1)
	defer connection.Close()
	var setup any
	if connection.ReadJSON(&setup) != nil {
		return
	}
	_ = connection.WriteJSON(map[string]string{"type": "session.updated"})
	if f.dropIdle.Swap(false) {
		return // The proxy dropped an idle connection.
	}
	for {
		var event struct {
			Type string `json:"type"`
		}
		if err := connection.ReadJSON(&event); err != nil {
			return
		}
		switch event.Type {
		case "input_audio_buffer.clear":
			_ = connection.WriteJSON(map[string]string{"type": "input_audio_buffer.cleared"})
		case "input_audio_buffer.commit":
			turn := f.turns.Add(1)
			item := fmt.Sprintf("item-%d", turn)
			if f.failNext.Swap(false) {
				_ = connection.WriteJSON(map[string]string{"type": "input_audio_buffer.committed", "item_id": item})
				_ = connection.WriteJSON(map[string]string{"type": "conversation.item.input_audio_transcription.failed", "item_id": item})
				continue
			}
			_ = connection.WriteJSON(map[string]string{"type": "input_audio_buffer.committed", "item_id": item})
			if turn > 1 {
				_ = connection.WriteJSON(map[string]string{"type": "conversation.item.input_audio_transcription.completed",
					"item_id": fmt.Sprintf("item-%d", turn-1), "transcript": "stale"})
			}
			_ = connection.WriteJSON(map[string]string{"type": "conversation.item.input_audio_transcription.delta", "item_id": item, "delta": "第"})
			_ = connection.WriteJSON(map[string]string{"type": "conversation.item.input_audio_transcription.completed",
				"item_id": item, "transcript": fmt.Sprintf("第%d段", turn)})
		}
	}
}

func startPool(t *testing.T, fake *fakeRealtime) (*OpenAI, context.CancelFunc) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(upstream.Close)
	client := &OpenAI{URL: "ws" + strings.TrimPrefix(upstream.URL, "http"), APIKey: "test", Model: "gpt-live-transcribe"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { client.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitWarm(t, client)
	return client, cancel
}

func waitWarm(t *testing.T, client *OpenAI) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		ready := client.idle != nil && client.idle.healthy()
		client.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no warm session")
}

func transcribeOnce(t *testing.T, client *OpenAI) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	final := ""
	err := client.Transcribe(ctx, io.NopCloser(strings.NewReader(string(make([]byte, 6400)))), 16000, func(update Update) error {
		if update.Final {
			final = update.Text
		}
		return nil
	})
	return final, err
}

func TestWarmSessionServesConsecutiveRecordings(t *testing.T) {
	fake := &fakeRealtime{}
	client, _ := startPool(t, fake)
	for turn := 1; turn <= 3; turn++ {
		text, err := transcribeOnce(t, client)
		if err != nil || text != fmt.Sprintf("第%d段", turn) {
			t.Fatalf("turn %d: %q %v", turn, text, err)
		}
		waitWarm(t, client)
	}
	if fake.connections.Load() != 1 {
		t.Fatalf("consecutive recordings used %d connections", fake.connections.Load())
	}
}

func TestWarmSessionReconnectsAfterIdleDrop(t *testing.T) {
	fake := &fakeRealtime{}
	fake.dropIdle.Store(true)
	client, _ := startPool(t, fake)
	// The first warm session was dropped; Run must notice and reconnect.
	deadline := time.Now().Add(3 * time.Second)
	for fake.connections.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	waitWarm(t, client)
	if text, err := transcribeOnce(t, client); err != nil || text != "第1段" {
		t.Fatalf("after reconnect: %q %v", text, err)
	}
}

func TestFailedTurnDiscardsSession(t *testing.T) {
	fake := &fakeRealtime{}
	client, _ := startPool(t, fake)
	fake.failNext.Store(true)
	if _, err := transcribeOnce(t, client); err == nil {
		t.Fatal("failed transcription succeeded")
	}
	waitWarm(t, client)
	if text, err := transcribeOnce(t, client); err != nil || text != "第2段" {
		t.Fatalf("after failure: %q %v", text, err)
	}
	if fake.connections.Load() != 2 {
		t.Fatalf("a failed session must be replaced, connections=%d", fake.connections.Load())
	}
}

func TestPreparedSessionReturnsToPoolWhenUnused(t *testing.T) {
	fake := &fakeRealtime{}
	client, _ := startPool(t, fake)
	prepared, err := client.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = prepared.Close()
	waitWarm(t, client)
	if text, err := transcribeOnce(t, client); err != nil || text != "第1段" || fake.connections.Load() != 1 {
		t.Fatalf("returned session: %q %v connections=%d", text, err, fake.connections.Load())
	}
}
