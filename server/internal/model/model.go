package model

import (
	"crypto/rand"
	"fmt"
	"unicode/utf8"
)

const (
	MaxDraftBytes = 64 << 10
	MaxSegments   = 64
	MaxReplyBytes = 2 << 20
	WindowBytes   = 2048
)

type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Revision  int64  `json:"draft_revision"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Error     string `json:"error,omitempty"`
}

type Segment struct {
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	Position       int64  `json:"position"`
	Generation     int64  `json:"generation"`
	State          string `json:"state"`
	Text           string `json:"-"`
	MessageID      string `json:"-"`
	CaptureMode    int    `json:"-"`
	CommitRevision int64  `json:"-"`
	Bytes          int    `json:"bytes"`
	Preview        string `json:"preview"`
}

type Message struct {
	ID            string `json:"id"`
	SessionID     string `json:"session_id"`
	DraftRevision int64  `json:"draft_revision"`
	Model         string `json:"-"`
	UpstreamScope string `json:"-"`
	Attempts      int    `json:"attempts"`
	Input         string `json:"-"`
	Reply         string `json:"-"`
	State         string `json:"state"`
	FinishReason  string `json:"finish_reason,omitempty"`
	Error         string `json:"error,omitempty"`
	Revision      int64  `json:"revision"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	InputBytes    int    `json:"input_bytes"`
	ReplyBytes    int    `json:"reply_bytes"`
}

type Window struct {
	Text     string `json:"text"`
	Offset   int    `json:"offset"`
	End      int    `json:"end"`
	Total    int    `json:"total"`
	Revision int64  `json:"revision"`
}

func NewID() string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func ValidID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for index, character := range id {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func Prefix(text string, size int) string {
	if len(text) <= size {
		return text
	}
	for size > 0 && !utf8.RuneStart(text[size]) {
		size--
	}
	return text[:size]
}

func TextWindow(text string, offset, limit int, revision int64) (Window, error) {
	if offset < 0 || offset > len(text) || limit < 4 || limit > WindowBytes {
		return Window{}, fmt.Errorf("invalid text window")
	}
	if offset < len(text) && !utf8.RuneStart(text[offset]) {
		return Window{}, fmt.Errorf("offset must be a UTF-8 boundary")
	}
	part := Prefix(text[offset:], limit)
	return Window{Text: part, Offset: offset, End: offset + len(part), Total: len(text), Revision: revision}, nil
}
