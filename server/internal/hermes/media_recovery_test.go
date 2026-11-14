package hermes

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func TestRecoveryFiltersEscapedMediaBeforeReplyBudget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"before \u0064ata:image\/png;base64,AAAA\/`)
		fmt.Fprint(w, strings.Repeat(`BBBB\u002F`, 300<<10))
		fmt.Fprint(w, `) after C:\\file"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	client := Client{BaseURL: upstream.URL, APIKey: "test"}
	var update Update
	finish, err := client.Chat(context.Background(), model.Message{ID: model.NewID(), SessionID: model.NewID(), Attempts: 1}, func(value Update) error { update = value; return nil })
	if err != nil || finish != "stop" || !update.Reset || update.Text != `before [media omitted]) after C:\file` {
		t.Fatalf("reply %+v finish %q error %v", update, finish, err)
	}
}
