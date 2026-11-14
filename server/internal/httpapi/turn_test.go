package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/asr"
	"github.com/tf4fun/voice-agent-terminal/server/internal/config"
	"github.com/tf4fun/voice-agent-terminal/server/internal/events"
	"github.com/tf4fun/voice-agent-terminal/server/internal/gateway"
	"github.com/tf4fun/voice-agent-terminal/server/internal/hermes"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

func wholeMessageEnvironment(t *testing.T) (*environment, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKey = "device-test"
	service := gateway.New(st, cfg, &hermes.Client{}, &asr.Fake{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := &environment{service: service, server: httptest.NewServer(New(service))}
	t.Cleanup(func() { e.server.Close(); st.Close() })
	session := model.NewID()
	if _, err := st.BeginSession(context.Background(), session, "whole messages"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionReady(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	return e, session
}

func addWholeMessage(t *testing.T, e *environment, session, input, reply string) model.Message {
	t.Helper()
	ctx := context.Background()
	state, err := e.service.Store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := e.service.Store.AddSegment(ctx, session, model.NewID(), input, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	message, err := e.service.Store.Send(ctx, session, model.NewID(), "test", "scope", revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.service.Store.ReplaceReply(ctx, message.ID, reply); err != nil {
		t.Fatal(err)
	}
	if err := e.service.Store.SetMessageState(ctx, message.ID, "completed", "stop", ""); err != nil {
		t.Fatal(err)
	}
	message, err = e.service.Store.Message(ctx, session, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestWholeMessageHistoryAndUncutContent(t *testing.T) {
	e, session := wholeMessageEnvironment(t)
	base := "/api/v1/sessions/" + session
	var turn store.Turn
	if err := json.Unmarshal(e.request(t, "GET", base+"/turn", nil, 200), &turn); err != nil || turn.Message != nil {
		t.Fatalf("empty turn: %+v %v", turn, err)
	}
	input := strings.Repeat("完整输入，保留换行。\n", 400)
	reply := "\n\n" + strings.Repeat("完整回复渥太华 😀 \"escaped\"\\\n", 600)
	first := addWholeMessage(t, e, session, input, reply)
	second := addWholeMessage(t, e, session, "second input", "second reply")
	if err := json.Unmarshal(e.request(t, "GET", base+"/turn", nil, 200), &turn); err != nil || turn.Message.ID != second.ID || turn.Before != first.ID || turn.After != "" {
		t.Fatalf("latest: %+v %v", turn, err)
	}
	if err := json.Unmarshal(e.request(t, "GET", base+"/turn?before="+second.ID, nil, 200), &turn); err != nil || turn.Message.ID != first.ID || turn.Before != "" || turn.After != second.ID {
		t.Fatalf("before: %+v %v", turn, err)
	}
	e.request(t, "GET", base+"/turn?after="+first.ID, nil, 200)
	e.request(t, "GET", base+"/turn?message="+first.ID, nil, 200)
	for part, expected := range map[string]string{"input": input, "reply": reply} {
		path := base + "/messages/" + first.ID + "/content?part=" + part
		request, _ := http.NewRequest("GET", e.server.URL+path, nil)
		request.Header.Set("Authorization", "Bearer device-test")
		response, err := e.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(data) != expected {
			t.Fatalf("%s content missing: status=%d bytes=%d err=%v", part, response.StatusCode, len(data), err)
		}
		if response.ContentLength != int64(len(expected)) || response.Header.Get("X-Revision") != strconv.FormatInt(first.Revision, 10) || response.Header.Get("X-Message-State") != "completed" {
			t.Fatalf("content metadata: %v", response.Header)
		}
		e.request(t, "GET", path+"&revision="+strconv.FormatInt(first.Revision, 10), nil, 200)
		e.request(t, "GET", path+"&revision=1", nil, 409)
		e.request(t, "GET", path+"&revision=", nil, 400)
	}
	for _, query := range []string{"?message=bad", "?before=", "?message=" + first.ID + "&after=" + first.ID, "?message=" + first.ID + "&message=" + second.ID} {
		e.request(t, "GET", base+"/turn"+query, nil, 400)
	}
	e.request(t, "GET", base+"/turn?before="+first.ID, nil, 404)
	other := model.NewID()
	if _, err := e.service.Store.BeginSession(context.Background(), other, "other session"); err != nil {
		t.Fatal(err)
	}
	if err := e.service.Store.SetSessionReady(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	foreign := addWholeMessage(t, e, other, "foreign question", "foreign answer")
	e.request(t, "GET", base+"/turn?message="+foreign.ID, nil, 404)
	e.request(t, "GET", base+"/turn?before="+foreign.ID, nil, 404)
	e.request(t, "GET", base+"/messages/"+foreign.ID+"/content?part=reply", nil, 404)
	e.request(t, "GET", base+"/messages/"+first.ID+"/content?part=invalid", nil, 400)
}

func TestWholeDraftContentMatchesReviewedRevision(t *testing.T) {
	e, session := wholeMessageEnvironment(t)
	input := strings.Repeat("完整待发送内容。\n", 700)
	revision, err := e.service.Store.AddSegment(context.Background(), session, model.NewID(), input, 1)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/sessions/" + session + "/draft/content"
	data := e.request(t, "GET", base+"?revision="+strconv.FormatInt(revision, 10), nil, 200)
	if string(data) != input {
		t.Fatal("draft content was split or changed")
	}
	e.request(t, "GET", base+"?revision=1", nil, 409)
}

func TestSSEReconnectRecoversWholeMessageAndKeepsDeltas(t *testing.T) {
	e, session := wholeMessageEnvironment(t)
	message := addWholeMessage(t, e, session, "question", "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connect := func() (*http.Response, *bufio.Reader) {
		request, _ := http.NewRequestWithContext(ctx, "GET", e.server.URL+"/api/v1/events?session_id="+session, nil)
		request.Header.Set("Authorization", "Bearer device-test")
		response, err := e.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response, bufio.NewReader(response.Body)
	}
	read := func(reader *bufio.Reader) events.Event {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				var event events.Event
				if err := json.Unmarshal([]byte(strings.TrimSpace(line[6:])), &event); err != nil {
					t.Fatal(err)
				}
				return event
			}
		}
	}
	response, reader := connect()
	if event := read(reader); event.Type != "resync" || event.SessionID != session {
		t.Fatal(event)
	}
	appendText := func(text string) {
		if err := e.service.Store.SetMessageState(ctx, message.ID, "waiting", "", ""); err != nil {
			t.Fatal(err)
		}
		offset, err := e.service.Store.AppendReply(ctx, message.ID, text)
		if err != nil {
			t.Fatal(err)
		}
		e.service.Events.Publish(events.Event{Type: "chat.delta", SessionID: session, MessageID: message.ID, Text: text, Offset: offset})
	}
	appendText("first ")
	event := read(reader)
	if event.Type != "chat.delta" || event.Offset != 0 || event.Text != "first " {
		t.Fatal(event)
	}
	response.Body.Close()
	appendText("离线追加")
	response, reader = connect()
	defer response.Body.Close()
	if event := read(reader); event.Type != "resync" {
		t.Fatal(event)
	}
	data := e.request(t, "GET", "/api/v1/sessions/"+session+"/messages/"+message.ID+"/content?part=reply", nil, 200)
	if string(data) != "first 离线追加" {
		t.Fatal("reconnect snapshot incomplete")
	}
	appendText(" resumed")
	event = read(reader)
	if event.Offset != len("first 离线追加") || event.Text != " resumed" {
		t.Fatal(event)
	}
}
