package tts

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeRealtime struct {
	connections atomic.Int32
	failNext    atomic.Bool
	lastModel   atomic.Value
}

func (f *fakeRealtime) serve(w http.ResponseWriter, r *http.Request) {
	f.lastModel.Store(r.URL.Query().Get("model"))
	connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	f.connections.Add(1)
	defer connection.Close()
	var setup map[string]any
	if connection.ReadJSON(&setup) != nil || setup["type"] != "session.update" {
		return
	}
	_ = connection.WriteJSON(map[string]string{"type": "session.updated"})
	for {
		var request map[string]any
		if connection.ReadJSON(&request) != nil {
			return
		}
		if request["type"] != "response.create" {
			continue
		}
		if f.failNext.Swap(false) {
			_ = connection.WriteJSON(map[string]any{"type": "error", "error": map[string]string{"code": "boom"}})
			continue
		}
		// 0.5 s of 24 kHz audio in three deltas.
		pcm := make([]byte, 24000)
		for _, part := range [][]byte{pcm[:7001], pcm[7001:16000], pcm[16000:]} {
			_ = connection.WriteJSON(map[string]string{"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(part)})
		}
		_ = connection.WriteJSON(map[string]any{"type": "response.done", "response": map[string]string{"status": "completed"}})
	}
}

func start(t *testing.T, fake *fakeRealtime) *OpenAI {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(upstream.Close)
	client := &OpenAI{URL: "ws" + strings.TrimPrefix(upstream.URL, "http"), APIKey: "k", Model: "gpt-realtime-2.1-mini", Voice: "marin"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { client.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		ready := client.idle != nil
		client.mu.Unlock()
		if ready {
			return client
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no warm speech session")
	return nil
}

func speak(client *OpenAI) (int, error) {
	total := 0
	err := client.Speak(context.Background(), "你好。", func(pcm []byte) error { total += len(pcm); return nil })
	return total, err
}

func TestWarmSessionSpeaksSentencesAt16kHz(t *testing.T) {
	fake := &fakeRealtime{}
	client := start(t, fake)
	for i := 0; i < 3; i++ {
		total, err := speak(client)
		if err != nil {
			t.Fatal(err)
		}
		// 0.5 s at 16 kHz PCM16 = 16000 bytes.
		if total < 15900 || total > 16100 {
			t.Fatalf("speech bytes %d", total)
		}
	}
	if fake.connections.Load() != 1 || fake.lastModel.Load() != "gpt-realtime-2.1-mini" {
		t.Fatalf("connections=%d model=%v", fake.connections.Load(), fake.lastModel.Load())
	}
}

func TestFailedResponseReplacesSession(t *testing.T) {
	fake := &fakeRealtime{}
	client := start(t, fake)
	fake.failNext.Store(true)
	if _, err := speak(client); err == nil {
		t.Fatal("failed response succeeded")
	}
	if _, err := speak(client); err != nil {
		t.Fatal(err)
	}
	if fake.connections.Load() < 2 {
		t.Fatal("failed session was reused")
	}
}

func TestFakeChimeLength(t *testing.T) {
	total := 0
	if err := (Fake{}).Speak(context.Background(), "一二三", func(pcm []byte) error { total += len(pcm); return nil }); err != nil {
		t.Fatal(err)
	}
	if total != 3*SampleRate/10*2 {
		t.Fatalf("fake speech bytes %d", total)
	}
}
