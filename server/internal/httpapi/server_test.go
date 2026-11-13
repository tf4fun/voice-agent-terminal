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
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Messages) != 1 {
				t.Error("invalid prompt")
				w.WriteHeader(400)
				return
			}
			env.input <- body.Messages[0].Content
			env.keys <- r.Header.Get("Idempotency-Key")
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
	session, segment, message := model.NewID(), model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "恢复测试"}, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": segment, "revision": 1, "text": "恢复请求"}, 201)
	e.request(t, "POST", base+"/messages", map[string]any{"id": message, "revision": 2}, 202)
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

func TestDraftAudioEditDeleteSendAndReadback(t *testing.T) {
	e := setup(t)
	sessionID := model.NewID()
	base := "/api/v1/sessions/" + sessionID
	e.request(t, "POST", "/api/v1/sessions", map[string]any{"id": sessionID, "title": "想法"}, 201)
	first, voice, discard := model.NewID(), model.NewID(), model.NewID()
	e.request(t, "POST", base+"/segments", map[string]any{"id": first, "revision": 1, "text": "第一段"}, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": voice, "revision": 2}, 201)
	request, _ := http.NewRequest("POST", e.server.URL+base+"/segments/"+voice+"/audio?sample_rate=16000&revision=3", bytes.NewReader(make([]byte, 6400)))
	request.Header.Set("Authorization", "Bearer device-test")
	request.Header.Set("Content-Type", "audio/pcm")
	response, err := e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("audio: %d %s", response.StatusCode, data)
	}
	if e.calls.Load() != 0 {
		t.Fatal("recognition sent chat automatically")
	}
	var draft struct {
		Revision int64           `json:"revision"`
		Items    []model.Segment `json:"items"`
	}
	_ = json.Unmarshal(e.request(t, "GET", base+"/draft", nil, 200), &draft)
	if len(draft.Items) != 2 {
		t.Fatal(draft)
	}
	var mutation struct {
		Revision int64 `json:"revision"`
	}
	_ = json.Unmarshal(e.request(t, "POST", base+"/segments", map[string]any{"id": discard, "revision": draft.Revision, "text": "删掉这一段"}, 201), &mutation)
	_ = json.Unmarshal(e.request(t, "DELETE", base+"/segments/"+discard, map[string]any{"revision": mutation.Revision}, 200), &mutation)
	_ = json.Unmarshal(e.request(t, "PATCH", base+"/segments/"+first, map[string]any{"revision": mutation.Revision, "text": "修改第一段"}, 200), &mutation)
	messageID := model.NewID()
	e.request(t, "POST", base+"/messages", map[string]any{"id": messageID, "revision": draft.Revision}, 409)
	send := map[string]any{"id": messageID, "revision": mutation.Revision}
	e.request(t, "POST", base+"/messages", send, 202)
	e.request(t, "POST", base+"/messages", send, 202)
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
	if input := <-e.input; input != "修改第一段\n\n测试转写" {
		t.Fatalf("snapshot %q", input)
	}
	var window model.Window
	_ = json.Unmarshal(e.request(t, "GET", base+"/messages/"+messageID+"/text", nil, 200), &window)
	if !strings.Contains(window.Text, "[media omitted]") || !strings.HasSuffix(window.Text, "后文") || strings.Contains(window.Text, "AAAA") {
		t.Fatal(window)
	}
	_ = json.Unmarshal(e.request(t, "GET", base+"/draft", nil, 200), &draft)
	if len(draft.Items) != 0 {
		t.Fatal("sent segments remained editable")
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
	session := model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "长文字"}, 201)
	segment := model.NewID()
	e.request(t, "POST", base+"/segments", map[string]any{"id": segment, "revision": 1, "text": strings.Repeat("中", 2000)}, 201)
	data := e.request(t, "GET", base+"/draft", nil, 200)
	if len(data) > 1024 {
		t.Fatal("list included full text")
	}
	e.request(t, "GET", base+"/segments/"+segment+"/text?offset=1", nil, 400)
	e.request(t, "GET", base+"/segments/"+segment+"/text?limit=10000", nil, 400)
}

func TestDraftsWorkWhileHermesIsUnconfigured(t *testing.T) {
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
	session, segment := model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "本地草稿"}, 202)
	e.request(t, "POST", base+"/segments", map[string]any{"id": segment, "revision": 1, "text": "先记录想法"}, 201)
	voice := model.NewID()
	e.request(t, "POST", base+"/segments", map[string]any{"id": voice, "revision": 2}, 201)
	if err := svc.Capture(context.Background(), session, voice, 3, io.NopCloser(bytes.NewReader(make([]byte, 6400))), 16000); err != nil {
		t.Fatal(err)
	}
	e.request(t, "POST", base+"/messages", map[string]any{"id": model.NewID(), "revision": 2}, 503)
	var draft struct {
		Items []model.Segment `json:"items"`
	}
	_ = json.Unmarshal(e.request(t, "GET", base+"/draft", nil, 200), &draft)
	if len(draft.Items) != 2 || draft.Items[0].Preview != "先记录想法" || draft.Items[1].Preview != "测试转写" {
		t.Fatalf("draft lost without upstream: %+v", draft)
	}
}
