package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/asr"
	"github.com/tf4fun/voice-agent-terminal/server/internal/config"
	"github.com/tf4fun/voice-agent-terminal/server/internal/gateway"
	"github.com/tf4fun/voice-agent-terminal/server/internal/hermes"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

type environment struct {
	server     *httptest.Server
	service    *gateway.Service
	calls      atomic.Int32
	input      chan string
	keys       chan string
	abortFirst atomic.Bool
}

func setup(t *testing.T) *environment {
	t.Helper()
	env := &environment{input: make(chan string, 4), keys: make(chan string, 4)}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hermes-test" {
			t.Error("invalid upstream authentication")
		}
		if r.URL.Path == "/v1/chat/completions" {
			env.calls.Add(1)
			var body struct {
				Stream   bool `json:"stream"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			// The configured default system prompt leads the stored user input.
			if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[0].Content != config.DefaultSystemPrompt {
				t.Error("invalid prompt")
				w.WriteHeader(400)
				return
			}
			// Tests read at most a few; extra chats must not block the fake.
			select {
			case env.input <- body.Messages[1].Content:
			default:
			}
			select {
			case env.keys <- r.Header.Get("Idempotency-Key"):
			default:
			}
			if !body.Stream {
				writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "完整回复"}, "finish_reason": "stop"}}})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if env.abortFirst.Swap(false) {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"部分\"}}]}\n\n")
				return
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"回复 ![图](data:image/png;base64,AAAA) 后文\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		status := 201
		if r.Method == "GET" {
			status = 200
		}
		writeJSON(w, status, map[string]any{"session": map[string]string{"id": id, "source": "api_server"}})
	}))
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	cfg.ASRProvider = "fake"
	cfg.APIKey = "device-test"
	cfg.HermesKey = "hermes-test"
	cfg.HermesURL = upstream.URL
	cfg.Workers = 2
	cfg.ChatTimeout = 2 * time.Second
	cfg.CaptureTimeout = 2 * time.Second
	env.service = gateway.New(st, cfg, &hermes.Client{BaseURL: upstream.URL, APIKey: cfg.HermesKey}, &asr.Fake{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	env.service.Start(ctx)
	env.server = httptest.NewServer(New(env.service))
	t.Cleanup(func() { cancel(); env.server.Close(); env.service.Close(); st.Close(); upstream.Close() })
	return env
}

func TestConnectionRecoveryReplacesPartialReply(t *testing.T) {
	e := setup(t)
	e.abortFirst.Store(true)
	session, message := model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", base+"/messages", map[string]any{"id": message, "text": "恢复请求", "new_session": true}, 202)
	waitState := func(expected string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			var result model.Message
			_ = json.Unmarshal(e.request(t, "GET", base+"/messages/"+message, nil, 200), &result)
			if result.State == expected {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("wanted %s, got %+v", expected, result)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitState("unconfirmed")
	e.request(t, "POST", base+"/messages/"+message+"/retry", nil, 200)
	waitState("completed")
	var window model.Window
	_ = json.Unmarshal(e.request(t, "GET", base+"/messages/"+message+"/text", nil, 200), &window)
	if window.Text != "完整回复" {
		t.Fatalf("recovery duplicated or lost text: %q", window.Text)
	}
	if first, second := <-e.keys, <-e.keys; first == "" || first != second {
		t.Fatal("recovery changed request identity")
	}
}

func (e *environment) request(t *testing.T, method, path string, payload any, expected int) []byte {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, e.server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer device-test")
	request.Header.Set("Content-Type", "application/json")
	response, err := e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != expected {
		t.Fatalf("%s %s returned %d, expected %d: %s", method, path, response.StatusCode, expected, data)
	}
	return data
}

// upload posts raw PCM for one transcription and returns status and body.
func (e *environment) upload(t *testing.T, id string, pcm []byte) (int, string) {
	t.Helper()
	request, _ := http.NewRequest("POST", e.server.URL+"/api/v1/transcriptions/"+id+"/audio?sample_rate=16000", bytes.NewReader(pcm))
	request.Header.Set("Authorization", "Bearer device-test")
	request.Header.Set("Content-Type", "audio/pcm")
	response, err := e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(data)
}

func TestTranscribeThenFirstSendCreatesTitledSession(t *testing.T) {
	e := setup(t)
	recording := model.NewID()
	e.request(t, "POST", "/api/v1/transcriptions", map[string]string{"id": recording}, 201)
	if status, text := e.upload(t, recording, make([]byte, 6400)); status != 200 || text != "测试转写" {
		t.Fatalf("audio: %d %q", status, text)
	}
	var live model.Window
	_ = json.Unmarshal(e.request(t, "GET", "/api/v1/transcriptions/"+recording+"/text?limit=512&tail=1", nil, 200), &live)
	if live.Text != "测试转写" {
		t.Fatalf("live text: %+v", live)
	}
	var list struct {
		Items []model.Session `json:"items"`
	}
	_ = json.Unmarshal(e.request(t, "GET", "/api/v1/sessions", nil, 200), &list)
	if len(list.Items) != 0 || e.calls.Load() != 0 {
		t.Fatal("transcription created a session or sent chat", list.Items, e.calls.Load())
	}
	sessionID, messageID := model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + sessionID
	e.request(t, "POST", base+"/messages", map[string]any{"id": messageID, "text": "改过的问题"}, 404)
	send := map[string]any{"id": messageID, "text": "\n改过的问题\n测试转写", "new_session": true}
	var accepted struct {
		model.Message
		Session model.Session `json:"session"`
	}
	_ = json.Unmarshal(e.request(t, "POST", base+"/messages", send, 202), &accepted)
	if accepted.ID != messageID || accepted.Session.Title != "改过的问题" || accepted.Session.State != "ready" {
		t.Fatalf("first send: %+v", accepted)
	}
	e.request(t, "POST", base+"/messages", send, 202) // A lost response is retried with the same IDs.
	e.request(t, "POST", base+"/messages", map[string]any{"id": messageID, "text": "别的文字"}, 409)
	deadline := time.Now().Add(3 * time.Second)
	for {
		var message model.Message
		_ = json.Unmarshal(e.request(t, "GET", base+"/messages/"+messageID, nil, 200), &message)
		if message.State == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("message stayed %+v", message)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e.calls.Load() != 1 {
		t.Fatalf("Hermes calls %d", e.calls.Load())
	}
	if input := <-e.input; input != "\n改过的问题\n测试转写" {
		t.Fatalf("snapshot %q", input)
	}
	var window model.Window
	_ = json.Unmarshal(e.request(t, "GET", base+"/messages/"+messageID+"/text", nil, 200), &window)
	if !strings.Contains(window.Text, "[media omitted]") || !strings.HasSuffix(window.Text, "后文") || strings.Contains(window.Text, "AAAA") {
		t.Fatal(window)
	}
	e.request(t, "PATCH", base, map[string]string{"title": "新标题"}, 200)
	e.request(t, "DELETE", base, nil, 200)
	e.request(t, "GET", base, nil, 404)
}

func TestAuthIDsPaginationAndReadLimits(t *testing.T) {
	e := setup(t)
	response, err := http.Get(e.server.URL + "/api/v1/server")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal(response.StatusCode)
	}
	e.request(t, "GET", "/api/v1/sessions/invalid", nil, 400)
	e.request(t, "GET", "/api/v1/sessions?limit=1000", nil, 400)
	e.request(t, "GET", "/api/v1/sessions?offset=-1", nil, 400)
	base := "/api/v1/sessions/" + model.NewID()
	e.request(t, "POST", base+"/messages", map[string]any{"id": model.NewID(), "text": " \n ", "new_session": true}, 400)
	e.request(t, "POST", base+"/messages", map[string]any{"id": model.NewID(), "text": strings.Repeat("a", model.MaxInputBytes+1), "new_session": true}, 413)
	e.request(t, "POST", base+"/messages", map[string]any{"id": "bad", "text": "x", "new_session": true}, 400)
	e.request(t, "GET", base, nil, 404) // Rejected sends leave no session behind.
	e.request(t, "GET", "/api/v1/transcriptions/"+model.NewID()+"/text", nil, 404)
	e.request(t, "GET", "/api/v1/transcriptions/bad/text", nil, 400)
	e.request(t, "POST", "/api/v1/transcriptions", map[string]string{"id": "bad"}, 400)
	recording := model.NewID()
	if status, _ := e.upload(t, recording, make([]byte, 6400)); status != 200 {
		t.Fatal(status)
	}
	if status, _ := e.upload(t, recording, make([]byte, 6400)); status != 409 {
		t.Fatalf("reused transcription ID: %d", status)
	}
	e.request(t, "GET", "/api/v1/transcriptions/"+recording+"/text?offset=1", nil, 400)
	e.request(t, "GET", "/api/v1/transcriptions/"+recording+"/text?limit=10000", nil, 400)
}

func TestTranscriptionWorksWhileHermesIsUnconfigured(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKey = "device-test"
	cfg.ASRProvider = "fake"
	svc := gateway.New(st, cfg, &hermes.Client{BaseURL: cfg.HermesURL}, &asr.Fake{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := &environment{service: svc, server: httptest.NewServer(New(svc))}
	t.Cleanup(func() { e.server.Close(); svc.Close(); st.Close() })
	if status, text := e.upload(t, model.NewID(), make([]byte, 6400)); status != 200 || text != "测试转写" {
		t.Fatalf("transcription without Hermes: %d %q", status, text)
	}
	base := "/api/v1/sessions/" + model.NewID()
	e.request(t, "POST", base+"/messages", map[string]any{"id": model.NewID(), "text": "先记录想法", "new_session": true}, 503)
	e.request(t, "GET", base, nil, 404)
}

type failingASR struct{ err error }

func (*failingASR) Enabled() bool { return true }
func (a *failingASR) Transcribe(_ context.Context, input io.ReadCloser, _ int, _ func(asr.Update) error) error {
	_, _ = io.Copy(io.Discard, input)
	return a.err
}

func TestTranscriptLimitMapsTo413Not502(t *testing.T) {
	e := setup(t)
	e.service.ASR = &failingASR{err: asr.ErrLimit}
	status, body := e.upload(t, model.NewID(), make([]byte, 6400))
	if status != 413 || !strings.Contains(body, `"limit_exceeded"`) {
		t.Fatalf("got status=%d body=%s, want 413 limit_exceeded", status, body)
	}
	e.service.ASR = &failingASR{err: asr.ErrUpstream}
	status, body = e.upload(t, model.NewID(), make([]byte, 6400))
	if status != 502 || !strings.Contains(body, `"transcription_failed"`) {
		t.Fatalf("got status=%d body=%s, want 502 transcription_failed", status, body)
	}
}
