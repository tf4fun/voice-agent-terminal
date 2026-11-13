package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func TestChatContinuationMediaFilteringAndFinish(t *testing.T) {
	message := model.Message{ID: model.NewID(), SessionID: model.NewID(), Model: "hermes-agent", Input: "整理想法"}
	var keys []string
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("X-Hermes-Session-Id") != message.SessionID || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("invalid Hermes request")
		}
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 1 || request.Messages[0].Content != message.Input {
			t.Error("new message body changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keepalive\n\nevent: hermes.tool.progress\ndata: {\"tool\":\"search\"}\n\n")
		for _, text := range []string{"前文 data:ima", "ge/png;base64,AAAA", "AAAA) 后文"} {
			data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": text}}}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hidden thought\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := Client{BaseURL: upstream.URL, APIKey: "test-secret"}
	for iteration := 0; iteration < 2; iteration++ {
		var reply strings.Builder
		finish, err := client.Chat(context.Background(), message, func(update Update) error { reply.WriteString(update.Text); return nil })
		if err != nil || finish != "stop" || reply.String() != "前文 [media omitted]) 后文" {
			t.Fatalf("reply %q finish %s error %v", reply.String(), finish, err)
		}
	}
	if len(keys) != 2 || keys[0] != keys[1] || keys[0] == "" {
		t.Fatal("retry identity changed")
	}
}

func TestPrematureEOFIsUnconfirmedEvenWithPartialText(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"部分回答\"}}]}\n\n")
	}))
	defer upstream.Close()
	client := Client{BaseURL: upstream.URL, APIKey: "test"}
	_, err := client.Chat(context.Background(), model.Message{ID: model.NewID(), SessionID: model.NewID()}, func(Update) error { return nil })
	var problem *UpstreamError
	if !errors.As(err, &problem) || !problem.Uncertain || problem.Code != "hermes_incomplete_stream" {
		t.Fatal(err)
	}
}

func TestApprovalIsVisibleAndErrorFinishIsPreserved(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: approval.request\ndata: {\"request_id\":\"private-request\"}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"error\"}],\"error\":{\"message\":\"private tool arguments\"}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := Client{BaseURL: upstream.URL, APIKey: "test"}
	attention := false
	finish, err := client.Chat(context.Background(), model.Message{ID: model.NewID()}, func(update Update) error { attention = attention || update.Attention; return nil })
	if err != nil || finish != "error" || !attention {
		t.Fatalf("%s %v %v", finish, err, attention)
	}
}

func TestRecoveryRequestsWholeReplyWithSameIdentity(t *testing.T) {
	message := model.Message{ID: model.NewID(), SessionID: model.NewID(), Model: "hermes-agent", Input: "same input", Attempts: 1}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Stream || len(body.Messages) != 1 || body.Messages[0].Content != message.Input || r.Header.Get("Idempotency-Key") != "voice-agent-"+message.ID {
			t.Error("recovery identity changed")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"完整回复"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	client := Client{BaseURL: upstream.URL, APIKey: "test"}
	var update Update
	finish, err := client.Chat(context.Background(), message, func(value Update) error { update = value; return nil })
	if err != nil || finish != "stop" || !update.Reset || update.Text != "完整回复" {
		t.Fatalf("%+v %s %v", update, finish, err)
	}
}

func TestForeignSessionIsNeverDeleted(t *testing.T) {
	id := model.NewID()
	deleted := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deleted = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"session": map[string]string{"id": id, "source": "cli"}})
	}))
	defer upstream.Close()
	client := Client{BaseURL: upstream.URL, APIKey: "test"}
	if err := client.DeleteSession(context.Background(), id); err == nil || deleted {
		t.Fatalf("foreign session delete: %v %v", deleted, err)
	}
}
