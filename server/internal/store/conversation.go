package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

const ConversationBytes = 256

// Four bounded blocks form a chronological viewport with one overlapping anchor.
type ConversationBlock struct {
	model.Window
	MessageID string `json:"message_id"`
	Part      string `json:"part"`
	State     string `json:"state"`
}

type ConversationPage struct {
	Items   []ConversationBlock `json:"items"`
	Before  string              `json:"before"`
	After   string              `json:"after"`
	Start   string              `json:"start"`
	Message *model.Message      `json:"message,omitempty"`
}

func blockCursor(block ConversationBlock) string {
	return fmt.Sprintf("%s:%s:%d:%d", block.MessageID, block.Part, block.Offset, block.End)
}

func parseBlockCursor(value string) (ConversationBlock, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 4 || !model.ValidID(parts[0]) || (parts[1] != "input" && parts[1] != "reply") {
		return ConversationBlock{}, ErrConflict
	}
	start, e1 := strconv.Atoi(parts[2])
	end, e2 := strconv.Atoi(parts[3])
	if e1 != nil || e2 != nil || start < 0 || end < start || end-start > ConversationBytes {
		return ConversationBlock{}, ErrConflict
	}
	return ConversationBlock{MessageID: parts[0], Part: parts[1], Window: model.Window{Offset: start, End: end}}, nil
}

type conversationReader struct {
	ctx     context.Context
	tx      *sql.Tx
	session string
	message model.Message
}

func (r *conversationReader) load(id string) error {
	if r.message.ID == id {
		return nil
	}
	message, err := scanMessage(r.tx.QueryRowContext(r.ctx, "SELECT "+messageColumns+" FROM messages WHERE session_id=? AND id=?", r.session, id))
	if err == nil {
		r.message = message
	}
	return err
}

func (r *conversationReader) neighbor(older bool) error {
	comparison, order := ">", "ASC"
	if older {
		comparison, order = "<", "DESC"
	}
	message, err := scanMessage(r.tx.QueryRowContext(r.ctx, "SELECT "+messageColumns+" FROM messages WHERE session_id=? AND rowid "+comparison+" (SELECT rowid FROM messages WHERE id=? AND session_id=?) ORDER BY rowid "+order+" LIMIT 1", r.session, r.message.ID, r.session))
	if err == nil {
		r.message = message
	}
	return err
}

func (r *conversationReader) block(part string, offset, end int) (ConversationBlock, error) {
	text := r.message.Input
	if part == "reply" {
		text = r.message.Reply
	}
	if offset < 0 || end < offset || end > len(text) || end-offset > ConversationBytes ||
		(offset < len(text) && !utf8.RuneStart(text[offset])) || (end < len(text) && !utf8.RuneStart(text[end])) {
		return ConversationBlock{}, ErrStale
	}
	return ConversationBlock{MessageID: r.message.ID, Part: part, State: r.message.State,
		Window: model.Window{Text: text[offset:end], Offset: offset, End: end, Total: len(text), Revision: r.message.Revision}}, nil
}

func (r *conversationReader) edge(part string, offset int, backward bool) (ConversationBlock, error) {
	text := r.message.Input
	if part == "reply" {
		text = r.message.Reply
	}
	start, end := offset, min(len(text), offset+ConversationBytes)
	if backward {
		start, end = max(0, offset-ConversationBytes), offset
		for start < end && !utf8.RuneStart(text[start]) {
			start++
		}
	} else {
		for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
	}
	return r.block(part, start, end)
}

func (r *conversationReader) step(block ConversationBlock, backward bool) (ConversationBlock, error) {
	if err := r.load(block.MessageID); err != nil {
		return ConversationBlock{}, err
	}
	if backward {
		if block.Offset > 0 {
			return r.edge(block.Part, block.Offset, true)
		}
		if block.Part == "reply" {
			return r.edge("input", len(r.message.Input), true)
		}
		if err := r.neighbor(true); err != nil {
			return ConversationBlock{}, err
		}
		return r.edge("reply", len(r.message.Reply), true)
	}
	size := len(r.message.Input)
	if block.Part == "reply" {
		size = len(r.message.Reply)
	}
	if block.End < size {
		return r.edge(block.Part, block.End, false)
	}
	if block.Part == "input" {
		return r.edge("reply", 0, false)
	}
	if err := r.neighbor(false); err != nil {
		return ConversationBlock{}, err
	}
	return r.edge("input", 0, false)
}

func (s *Store) Conversation(ctx context.Context, session, mode, cursor string) (ConversationPage, error) {
	page := ConversationPage{Items: make([]ConversationBlock, 0, 4)}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := readSession(ctx, tx, session); err != nil {
			return err
		}
		reader := conversationReader{ctx: ctx, tx: tx, session: session}
		var anchor ConversationBlock
		var err error
		backward := mode == "before" || mode == "tail"
		if mode == "tail" {
			reader.message, err = scanMessage(tx.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE session_id=? ORDER BY rowid DESC LIMIT 1", session))
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			anchor, err = reader.edge("reply", len(reader.message.Reply), true)
		} else {
			anchor, err = parseBlockCursor(cursor)
			if err != nil {
				return err
			}
			if err = reader.load(anchor.MessageID); err != nil {
				return err
			}
			anchor, err = reader.block(anchor.Part, anchor.Offset, anchor.End)
			// Refresh the newest visible partial block as its reply grows.
			if err == nil && mode == "from" && anchor.End-anchor.Offset < ConversationBytes {
				anchor, err = reader.edge(anchor.Part, anchor.Offset, false)
			}
		}
		if err != nil {
			return err
		}
		page.Items = append(page.Items, anchor)
		current := anchor
		for len(page.Items) < 4 {
			next, err := reader.step(current, backward)
			if errors.Is(err, ErrNotFound) {
				break
			}
			if err != nil {
				return err
			}
			page.Items = append(page.Items, next)
			current = next
		}
		if backward {
			for left, right := 0, len(page.Items)-1; left < right; left, right = left+1, right-1 {
				page.Items[left], page.Items[right] = page.Items[right], page.Items[left]
			}
		}
		first, last := page.Items[0], page.Items[len(page.Items)-1]
		page.Start = blockCursor(first)
		if _, err := reader.step(first, true); err == nil {
			page.Before = blockCursor(first)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err := reader.step(last, false); err == nil {
			page.After = blockCursor(last)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := reader.load(last.MessageID); err != nil {
			return err
		}
		message := reader.message
		message.Input, message.Reply = "", ""
		page.Message = &message
		return nil
	})
	return page, err
}
