package store

import (
	"context"
	"database/sql"
	"unicode/utf8"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

type rowsQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readSegments(ctx context.Context, query rowsQuery, sessionID string, limit, offset int) ([]model.Segment, error) {
	rows, err := query.QueryContext(ctx, `SELECT id,session_id,position,generation,state,length(CAST(text AS BLOB)),substr(text,1,20)
		FROM segments WHERE session_id=? AND message_id IS NULL AND state!='deleted' ORDER BY position LIMIT ? OFFSET ?`, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Segment, 0, limit)
	for rows.Next() {
		var item model.Segment
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Position, &item.Generation, &item.State, &item.Bytes, &item.Preview); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Draft(ctx context.Context, sessionID string, limit, offset int) (model.Session, []model.Segment, error) {
	var session model.Session
	var items []model.Segment
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		session, err = readSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		items, err = readSegments(ctx, tx, sessionID, limit, offset)
		return err
	})
	return session, items, err
}

func (s *Store) DraftSegment(ctx context.Context, sessionID, id string) (model.Segment, int64, error) {
	var segment model.Segment
	var revision int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		session, err := readSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		revision = session.Revision
		segment, err = readSegment(ctx, tx, sessionID, id)
		return err
	})
	return segment, revision, err
}

func readSegment(ctx context.Context, query rowQuery, sessionID, id string) (model.Segment, error) {
	var segment model.Segment
	err := query.QueryRowContext(ctx, `SELECT id,session_id,position,generation,state,text FROM segments
		WHERE id=? AND session_id=? AND message_id IS NULL AND state!='deleted'`, id, sessionID).
		Scan(&segment.ID, &segment.SessionID, &segment.Position, &segment.Generation, &segment.State, &segment.Text)
	segment.Bytes = len(segment.Text)
	return segment, missing(err)
}

func (s *Store) Segment(ctx context.Context, sessionID, id string) (model.Segment, error) {
	return readSegment(ctx, s.db, sessionID, id)
}

func draftFits(ctx context.Context, tx *sql.Tx, sessionID string, delta int) error {
	var size int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(text AS BLOB))),0) FROM segments
		WHERE session_id=? AND message_id IS NULL AND state!='deleted'`, sessionID).Scan(&size); err != nil {
		return err
	}
	if size+delta > model.MaxDraftBytes {
		return ErrLimit
	}
	return nil
}

func (s *Store) AddSegment(ctx context.Context, sessionID, id, text string, revision int64) (int64, error) {
	if !utf8.ValidString(text) || len(text) > model.MaxDraftBytes {
		return 0, ErrLimit
	}
	var next int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := checkDraft(ctx, tx, sessionID, revision); err != nil {
			return err
		}
		var position int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),0)+1 FROM segments WHERE session_id=?`, sessionID).Scan(&position); err != nil {
			return err
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segments WHERE session_id=? AND message_id IS NULL AND state!='deleted'`, sessionID).Scan(&live); err != nil {
			return err
		}
		if live >= model.MaxSegments {
			return ErrLimit
		}
		if err := draftFits(ctx, tx, sessionID, len(text)); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM segments WHERE id=?", id).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO segments(id,session_id,position,state,text) VALUES(?,?,?,'ready',?)", id, sessionID, position, text); err != nil {
			return err
		}
		var err error
		next, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	return next, err
}

func (s *Store) EditSegment(ctx context.Context, sessionID, id, text string, revision int64, deleted bool) (int64, error) {
	if !utf8.ValidString(text) {
		return 0, ErrLimit
	}
	state := "ready"
	if deleted {
		state, text = "deleted", ""
	}
	var next int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := checkDraft(ctx, tx, sessionID, revision); err != nil {
			return err
		}
		segment, err := readSegment(ctx, tx, sessionID, id)
		if err != nil {
			return err
		}
		if err := draftFits(ctx, tx, sessionID, len(text)-len(segment.Text)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET text=?,state=?,generation=generation+1 WHERE id=?", text, state, id); err != nil {
			return err
		}
		next, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	return next, err
}

func (s *Store) BeginCapture(ctx context.Context, sessionID, id string, revision int64) (int64, error) {
	var generation int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := checkDraft(ctx, tx, sessionID, revision); err != nil {
			return err
		}
		segment, err := readSegment(ctx, tx, sessionID, id)
		if err != nil {
			return err
		}
		if segment.State == "capturing" {
			return ErrBusy
		}
		if segment.Text != "" {
			return ErrConflict
		}
		if err := tx.QueryRowContext(ctx, "UPDATE segments SET state='capturing',generation=generation+1 WHERE id=? RETURNING generation", id).Scan(&generation); err != nil {
			return err
		}
		_, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	return generation, err
}

// A recognition update may only change the generation that started it.
func (s *Store) UpdateTranscript(ctx context.Context, sessionID, id string, generation int64, text, state string) (int64, error) {
	if !utf8.ValidString(text) {
		return 0, ErrLimit
	}
	if state != "capturing" && state != "ready" && state != "failed" {
		return 0, ErrConflict
	}
	var next int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		segment, err := readSegment(ctx, tx, sessionID, id)
		if err != nil {
			return err
		}
		if segment.Generation != generation || segment.State != "capturing" {
			return ErrStale
		}
		if err := draftFits(ctx, tx, sessionID, len(text)-len(segment.Text)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET text=?,state=? WHERE id=?", text, state, id); err != nil {
			return err
		}
		next, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	return next, err
}

func (s *Store) Recover(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET draft_revision=draft_revision+1 WHERE id IN
			(SELECT session_id FROM segments WHERE state='capturing' AND message_id IS NULL)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET state='failed',generation=generation+1 WHERE state='capturing' AND message_id IS NULL"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE messages SET state='unconfirmed',error='gateway_restarted',revision=revision+1
			WHERE state IN ('sending','waiting','receiving','attention')`)
		return err
	})
}
