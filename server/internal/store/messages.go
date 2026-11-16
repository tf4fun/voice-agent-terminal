package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

const messageColumns = "id,session_id,model,upstream_scope,attempts,input,reply,state,finish_reason,error,revision,created_at,updated_at"

type scanner interface{ Scan(...any) error }

func scanMessage(row scanner) (model.Message, error) {
	var message model.Message
	err := row.Scan(&message.ID, &message.SessionID, &message.Model, &message.UpstreamScope, &message.Attempts, &message.Input, &message.Reply,
		&message.State, &message.FinishReason, &message.Error, &message.Revision, &message.CreatedAt, &message.UpdatedAt)
	message.InputBytes, message.ReplyBytes = len(message.Input), len(message.Reply)
	return message, missing(err)
}

func (s *Store) Message(ctx context.Context, sessionID, id string) (model.Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=? AND session_id=?", id, sessionID))
}

// Send stores one complete user input as a queued message. With create, an
// unknown session is created in the same transaction and titled after the
// input, so a session never exists without its first message. A repeated
// message ID with the same session and input returns the stored message.
func (s *Store) Send(ctx context.Context, sessionID, id, modelName, scope, input string, create bool) (model.Message, bool, error) {
	created := false
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		existing, err := scanMessage(tx.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=?", id))
		if err == nil {
			if existing.SessionID != sessionID || existing.Input != input {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if input == "" {
			return ErrConflict
		}
		if len(input) > model.MaxInputBytes {
			return ErrLimit
		}
		now := time.Now().UnixMilli()
		var state string
		err = tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", sessionID).Scan(&state)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if !create {
				return ErrNotFound
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO sessions(id,title,state,created_at,updated_at) VALUES(?,?,'creating',?,?)",
				sessionID, model.TitleFrom(input), now, now); err != nil {
				return err
			}
			created = true
		case err != nil:
			return err
		case state == "deleted" || state == "deleting":
			return ErrNotFound
		}
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE session_id=? AND state IN ('queued','sending','waiting','receiving','attention','unconfirmed')", sessionID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrBusy
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO messages(id,session_id,model,upstream_scope,input,state,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?)", id, sessionID, modelName, scope, input, now, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE sessions SET updated_at=? WHERE id=?", now, sessionID)
		return err
	})
	if err != nil {
		return model.Message{}, false, err
	}
	message, err := s.Message(ctx, sessionID, id)
	return message, created, err
}

// Claim takes the oldest queued message whose Hermes session exists; a session
// still being created keeps its first message queued.
func (s *Store) Claim(ctx context.Context) (model.Message, error) {
	return scanMessage(s.db.QueryRowContext(ctx, `UPDATE messages SET state='sending',error='',updated_at=?,revision=revision+1
		WHERE id=(SELECT m.id FROM messages m JOIN sessions s ON s.id=m.session_id
			WHERE m.state='queued' AND s.state='ready' ORDER BY m.rowid LIMIT 1) RETURNING `+messageColumns, time.Now().UnixMilli()))
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

// Retry requeues an unconfirmed or failed message after explicit user confirmation.
// The message adopts the current upstream scope, so a message stranded by a
// Hermes URL or key change can be resent instead of failing the scope check forever.
func (s *Store) Retry(ctx context.Context, sessionID, id, scope string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		session, err := readSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if session.State != "ready" {
			return ErrBusy
		}
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
		_, err = tx.ExecContext(ctx, "UPDATE messages SET state='queued',upstream_scope=?,attempts=attempts+1,finish_reason='',error='',updated_at=?,revision=revision+1 WHERE id=?", scope, time.Now().UnixMilli(), id)
		return err
	})
}

func (s *Store) Messages(ctx context.Context, sessionID string, limit, offset int) ([]model.Message, error) {
	// Metadata queries leave large inputs and replies inside SQLite.
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,model,upstream_scope,attempts,'','',state,finish_reason,error,revision,created_at,updated_at,
		length(CAST(input AS BLOB)),length(CAST(reply AS BLOB)) FROM messages WHERE session_id=? ORDER BY rowid DESC LIMIT ? OFFSET ?`, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Message, 0, limit)
	for rows.Next() {
		var item model.Message
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Model, &item.UpstreamScope, &item.Attempts, &item.Input, &item.Reply, &item.State,
			&item.FinishReason, &item.Error, &item.Revision, &item.CreatedAt, &item.UpdatedAt, &item.InputBytes, &item.ReplyBytes); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Recover marks messages interrupted by a gateway restart as unconfirmed, so
// their outcome is reconciled or retried explicitly.
func (s *Store) Recover(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET state='unconfirmed',error='gateway_restarted',revision=revision+1
		WHERE state IN ('sending','waiting','receiving','attention')`)
	return err
}
