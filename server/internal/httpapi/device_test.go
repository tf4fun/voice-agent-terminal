package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func TestDevicePagesReportAnExactNextPage(t *testing.T) {
	e := setup(t)
	var selected string
	for index := 0; index < 5; index++ {
		selected = model.NewID()
		e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": selected, "title": selected}, 201)
	}
	var page struct {
		Items   []model.Session `json:"items"`
		HasMore bool            `json:"has_more"`
	}
	if err := json.Unmarshal(e.request(t, "GET", "/api/v1/sessions?limit=4", nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 || !page.HasMore {
		t.Fatal(page)
	}
	if err := json.Unmarshal(e.request(t, "GET", "/api/v1/sessions?limit=4&offset=4", nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.HasMore {
		t.Fatal(page)
	}
	base := "/api/v1/sessions/" + selected
	for revision := 1; revision <= 5; revision++ {
		e.request(t, "POST", base+"/segments", map[string]any{"id": model.NewID(), "revision": revision, "text": "片段"}, 201)
	}
	var draft struct {
		Items   []model.Segment `json:"items"`
		HasMore bool            `json:"has_more"`
	}
	_ = json.Unmarshal(e.request(t, "GET", base+"/draft?limit=4", nil, 200), &draft)
	if len(draft.Items) != 4 || !draft.HasMore {
		t.Fatal(draft)
	}
	_ = json.Unmarshal(e.request(t, "GET", base+"/draft?limit=4&offset=4", nil, 200), &draft)
	if len(draft.Items) != 1 || draft.HasMore {
		t.Fatal(draft)
	}
}

func TestDeviceTextBeforeAndTailRespectUTF8AndBounds(t *testing.T) {
	text := strings.Repeat("你好", 300)
	for _, test := range []struct {
		query       string
		status      int
		offset, end int
	}{
		{"?limit=512&before=1020", 200, 510, 1020},
		{"?limit=512&tail=1", 200, 1290, 1800},
		{"?limit=512&before=0", 200, 0, 0},
		{"?limit=512&before=1021", 400, 0, 0},
		{"?limit=512&before=1020&offset=0", 400, 0, 0},
		{"?limit=512&tail=1&offset=0", 400, 0, 0},
		{"?limit=512&tail=2", 400, 0, 0},
		{"?limit=3&tail=1", 400, 0, 0},
		{"?limit=512&tail=1&revision=6", 409, 0, 0},
	} {
		t.Run(test.query, func(t *testing.T) {
			response := httptest.NewRecorder()
			textWindow(response, httptest.NewRequest("GET", "/text"+test.query, nil), text, 7)
			if response.Code != test.status {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			if test.status != 200 {
				return
			}
			var window model.Window
			if err := json.Unmarshal(response.Body.Bytes(), &window); err != nil {
				t.Fatal(err)
			}
			if window.Offset != test.offset || window.End != test.end || window.Text != text[test.offset:test.end] {
				t.Fatal(window)
			}
		})
	}
}

func TestDeviceDraftReviewAndRerecordSendSnapshot(t *testing.T) {
	e := setup(t)
	session, old, next := model.NewID(), model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "重录测试"}, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": old, "revision": 1, "text": strings.Repeat("原稿", 100)}, 201)
	var window model.Window
	if err := json.Unmarshal(e.request(t, "GET", base+"/draft/text?limit=512", nil, 200), &window); err != nil {
		t.Fatal(err)
	}
	if len(window.Text) > 512 || window.Revision != 2 || window.Total != 600 {
		t.Fatal(window)
	}
	e.request(t, "POST", base+"/segments", map[string]any{"id": next, "revision": 2}, 201)
	request := httptest.NewRequest("POST", base+"/segments/"+next+"/audio?sample_rate=16000&revision=3&replace=1", strings.NewReader(strings.Repeat("\x00", 6400)))
	request.Header.Set("Content-Type", "audio/pcm")
	request.Header.Set("Authorization", "Bearer device-test")
	response := httptest.NewRecorder()
	New(e.service).ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("capture %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(e.request(t, "GET", base+"/draft/text?limit=512", nil, 200), &window); err != nil {
		t.Fatal(err)
	}
	if window.Text != "测试转写" {
		t.Fatal(window)
	}
	id := model.NewID()
	body := map[string]any{"id": id, "revision": window.Revision}
	e.request(t, "POST", base+"/messages", body, 202)
	e.request(t, "POST", base+"/messages", body, 202)
	select {
	case text := <-e.input:
		if text != window.Text {
			t.Fatalf("reviewed %q, sent %q", window.Text, text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("message was not sent")
	}
	if e.calls.Load() != 1 {
		t.Fatalf("duplicate send count %d", e.calls.Load())
	}
}

func TestDeviceConversationEndpointPairsQuestionAndReply(t *testing.T) {
	e := setup(t)
	session, segment, message := model.NewID(), model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "聊天窗口"}, 201)
	var empty struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(e.request(t, "GET", base+"/conversation", nil, 200), &empty); err != nil || len(empty.Items) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	e.request(t, "POST", base+"/segments", map[string]any{"id": segment, "revision": 1, "text": "我的提问"}, 201)
	e.request(t, "POST", base+"/messages", map[string]any{"id": message, "revision": 2}, 202)
	deadline := time.Now().Add(3 * time.Second)
	for {
		var state model.Message
		_ = json.Unmarshal(e.request(t, "GET", base+"/messages/"+message, nil, 200), &state)
		if state.State == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reply timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var page struct {
		Items []struct{ Text, Part, MessageID string } `json:"items"`
		Start string                                   `json:"start"`
	}
	if err := json.Unmarshal(e.request(t, "GET", base+"/conversation", nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Part != "input" || page.Items[0].Text != "我的提问" || page.Items[1].Part != "reply" || page.Items[1].Text == "" {
		t.Fatalf("roles: %+v", page)
	}
	e.request(t, "GET", base+"/conversation?from="+page.Start, nil, 200)
	e.request(t, "GET", base+"/conversation?from="+page.Start+"&before="+page.Start, nil, 400)
	e.request(t, "GET", base+"/conversation?from=bad", nil, 409)
}
