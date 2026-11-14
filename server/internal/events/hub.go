// Package events adapts voice-memo's bounded fan-out and reconnect behavior.
package events

import (
	"sync"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

type Event struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	SegmentID string `json:"segment_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
	Status    string `json:"status,omitempty"`
	Text      string `json:"text,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}

type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewHub() *Hub { return &Hub{subs: make(map[chan Event]struct{})} }

func (h *Hub) Subscribe() (<-chan Event, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= 64 {
		return nil, func() {}, false
	}
	channel := make(chan Event, 32)
	h.subs[channel] = struct{}{}
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if _, found := h.subs[channel]; found {
				delete(h.subs, channel)
				close(channel)
			}
		})
	}
	return channel, cancel, true
}

func (h *Hub) Publish(event Event) {
	event.Text = model.Prefix(event.Text, 64)
	h.mu.Lock()
	defer h.mu.Unlock()
	for channel := range h.subs {
		select {
		case channel <- event:
		default:
			delete(h.subs, channel)
			close(channel)
		}
	}
}
