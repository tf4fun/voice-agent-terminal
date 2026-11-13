package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

const messageColumns = "id,session_id,draft_revision,model,upstream_scope,attempts,input,reply,state,finish_reason,error,revision,created_at,updated_at"

type scanner interface{ Scan(...any) error }

func scanMessage(row scanner) (model.Message, error) {
	var message model.Message
	err := row.Scan(&message.ID, &message.SessionID, &message.DraftRevision, &message.Model, &message.UpstreamScope, &message.Attempts, &message.Input, &message.Reply,
		&message.State, &message.FinishReason, &message.Error, &message.Revision, &message.CreatedAt, &message.UpdatedAt)
	message.InputBytes, message.ReplyBytes = len(message.Input), len(message.Reply)
	return message, missing(err)
}

func (s *Store) Message(ctx context.Context, sessionID, id string) (model.Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=? AND session_id=?", id, sessionID))
}

func (s *Store) Send(ctx context.Context, sessionID, id, modelName, scope string, revision int64) (model.Message, error) {
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		existing, err := scanMessage(tx.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=?", id))
		if err == nil {
			if existing.SessionID != sessionID || existing.DraftRevision != revision {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := checkDraft(ctx, tx, sessionID, revision); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE session_id=? AND state IN ('queued','sending','waiting','receiving','attention','unconfirmed')", sessionID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrBusy
		}
		rows, err := tx.QueryContext(ctx, "SELECT text FROM segments WHERE session_id=? AND message_id IS NULL AND state!='deleted' ORDER BY position", sessionID)
		if err != nil {
			return err
		}
		var parts []string
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				rows.Close()
				return err
			}
			if strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		input := strings.Join(parts, "\n\n")
		if input == "" {
			return ErrConflict
		}
		if len(input) > model.MaxDraftBytes {
			return ErrLimit
		}
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx, "INSERT INTO messages(id,session_id,draft_revision,model,upstream_scope,input,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'queued',?,?)", id, sessionID, revision, modelName, scope, input, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET message_id=?,generation=generation+1 WHERE session_id=? AND message_id IS NULL AND state!='deleted'", id, sessionID); err != nil {
			return err
		}
		_, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	if err != nil {
		return model.Message{}, err
	}
	return s.Message(ctx, sessionID, id)
}

func (s *Store) Claim(ctx context.Context) (model.Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx, `UPDATE messages SET state='sending',error='',updated_at=?,revision=revision+1
		WHERE id=(SELECT id FROM messages WHERE state='queued' ORDER BY rowid LIMIT 1) RETURNING `+messageColumns, time.Now().UnixMilli()))
}

func (s *Store) SetMessageState(ctx context.Context, id, state, reason, code string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE messages SET state=?,finish_reason=?,error=?,revision=revision+1,updated_at=? WHERE id=?", state, reason, code, time.Now().UnixMilli(), id)
	return err
}

func (s *Store) AppendReply(ctx context.Context, id, text string) (int, error) {
	if !utf8.ValidString(text) {
		return 0, ErrLimit
	}
	var offset int
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT length(CAST(reply AS BLOB)),state FROM messages WHERE id=?", id).Scan(&offset, &state); err != nil {
			return missing(err)
		}
		if state != "sending" && state != "waiting" && state != "receiving" && state != "attention" {
			return ErrConflict
		}
		if offset+len(text) > model.MaxReplyBytes {
			return ErrLimit
		}
		_, err := tx.ExecContext(ctx, "UPDATE messages SET reply=reply||?,state='receiving',updated_at=?,revision=revision+1 WHERE id=?", text, time.Now().UnixMilli(), id)
		return err
	})
	return offset, err
}

func (s *Store) Retry(ctx context.Context, sessionID, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		message, err := scanMessage(tx.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=? AND session_id=?", id, sessionID))
		if err != nil {
			return err
		}
		if message.State != "unconfirmed" && message.State != "failed" {
			return ErrConflict
		}
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id=? AND id!=? AND
			(state IN ('queued','sending','waiting','receiving','attention','unconfirmed') OR rowid>(SELECT rowid FROM messages WHERE id=?))`, sessionID, id, id).Scan(&others); err != nil {
			return err
		}
		if others > 0 {
			return ErrBusy
		}
		_, err = tx.ExecContext(ctx, "UPDATE messages SET state='queued',attempts=attempts+1,finish_reason='',error='',updated_at=?,revision=revision+1 WHERE id=?", time.Now().UnixMilli(), id)
		return err
	})
}

func (s *Store) Messages(ctx context.Context, sessionID string, limit, offset int) ([]model.Message, error) {
	// Metadata queries leave large inputs and replies inside SQLite.
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,draft_revision,model,upstream_scope,attempts,'','',state,finish_reason,error,revision,created_at,updated_at,
		length(CAST(input AS BLOB)),length(CAST(reply AS BLOB)) FROM messages WHERE session_id=? ORDER BY rowid DESC LIMIT ? OFFSET ?`, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Message, 0, limit)
	for rows.Next() {
		var item model.Message
		if err := rows.Scan(&item.ID, &item.SessionID, &item.DraftRevision, &item.Model, &item.UpstreamScope, &item.Attempts, &item.Input, &item.Reply, &item.State,
			&item.FinishReason, &item.Error, &item.Revision, &item.CreatedAt, &item.UpdatedAt, &item.InputBytes, &item.ReplyBytes); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
