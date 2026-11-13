package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func TestBoundedPayloadAndSlowSubscriberRecovery(t *testing.T) {
	hub := NewHub()
	channel, cancel, ok := hub.Subscribe()
	if !ok {
		t.Fatal("subscription refused")
	}
	defer cancel()
	hub.Publish(Event{Type: "chat.delta", SessionID: model.NewID(), SegmentID: model.NewID(), MessageID: model.NewID(), Text: strings.Repeat("\x00<你好", 1000)})
	payload, err := json.Marshal(<-channel)
	if err != nil || len(payload)+8 > 1024 {
		t.Fatalf("payload %d: %v", len(payload), err)
	}
	for index := 0; index < 33; index++ {
		hub.Publish(Event{Type: "draft.updated"})
	}
	count := 0
	for range channel {
		count++
	}
	if count != 32 {
		t.Fatalf("buffered events %d", count)
	}
}
