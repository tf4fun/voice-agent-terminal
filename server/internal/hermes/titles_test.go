package hermes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func TestSessionTitlesAreForwardedAndRenameChecksOwnership(t *testing.T) {
	id := model.NewID()
	created, renamed := "", ""
	source := "api_server"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer title-test" {
			t.Error("missing upstream authorization")
		}
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(map[string]any{"session": map[string]string{"id": id, "source": source}})
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if r.Method == "POST" {
			created = body["title"]
		} else if r.Method == "PATCH" {
			renamed = body["title"]
		} else {
			t.Error("unexpected mutation")
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	client := Client{BaseURL: upstream.URL, APIKey: "title-test"}
	if err := client.CreateSession(context.Background(), id, "新会话 01234567"); err != nil {
		t.Fatal(err)
	}
	if created != "新会话 01234567" {
		t.Fatal(created)
	}
	if err := client.RenameSession(context.Background(), id, "语音内容标题"); err != nil || renamed != "语音内容标题" {
		t.Fatalf("rename %q: %v", renamed, err)
	}
	source = "cli"
	renamed = ""
	if err := client.RenameSession(context.Background(), id, "foreign"); err == nil || renamed != "" {
		t.Fatal("foreign session was renamed")
	}
}

func TestCreateSessionRetriesRejectedTitleWithoutChangingIdentity(t *testing.T) {
	for _, reason := range []string{"invalid_title", "invalid_source"} {
		t.Run(reason, func(t *testing.T) {
			id := model.NewID()
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.Method != "POST" || body["id"] != id || body["source"] != "api_server" {
					t.Error("session identity changed during title recovery")
				}
				if calls == 1 {
					if body["title"] != "新会话" {
						t.Error("initial title was not forwarded")
					}
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": reason}})
					return
				}
				if _, present := body["title"]; present {
					t.Error("rejected title was retried")
				}
				w.WriteHeader(http.StatusCreated)
			}))
			defer upstream.Close()
			client := Client{BaseURL: upstream.URL, APIKey: "title-test"}
			err := client.CreateSession(context.Background(), id, "新会话")
			if reason == "invalid_title" {
				if err != nil || calls != 2 {
					t.Fatalf("title recovery: calls=%d err=%v", calls, err)
				}
			} else if err == nil || calls != 1 {
				t.Fatalf("unrelated failure was retried: calls=%d err=%v", calls, err)
			}
		})
	}
}
