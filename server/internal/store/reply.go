package store

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

// A recovered non-streaming response replaces the old partial reply atomically.
func (s *Store) ReplaceReply(ctx context.Context, id, text string) error {
	if !utf8.ValidString(text) || len(text) > model.MaxReplyBytes {
		return ErrLimit
	}
	_, err := s.db.ExecContext(ctx, "UPDATE messages SET reply=?,state='receiving',revision=revision+1,updated_at=? WHERE id=?", text, time.Now().UnixMilli(), id)
	return err
}
