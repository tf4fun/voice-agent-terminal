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

func (s *Store) Draft(ctx context.Context, sessionID string, limit, offset int) (model.Session, []model.Segment, int, error) {
	var session model.Session
	var items []model.Segment
	var total int
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		session, err = readSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		items, err = readSegments(ctx, tx, sessionID, limit, offset)
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM segments WHERE session_id=? AND message_id IS NULL AND state!='deleted'", sessionID).Scan(&total)
	})
	return session, items, total, err
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

func readDraftText(ctx context.Context, tx *sql.Tx, sessionID string) (string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT text FROM segments WHERE session_id=? AND message_id IS NULL AND state!='deleted' AND capture_mode=0 ORDER BY position", sessionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return "", err
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), rows.Err()
}

func (s *Store) DraftText(ctx context.Context, sessionID string) (string, int64, error) {
	var text string
	var revision int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		session, err := readSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		revision = session.Revision
		text, err = readDraftText(ctx, tx, sessionID)
		return err
	})
	return text, revision, err
}

func readSegment(ctx context.Context, query rowQuery, sessionID, id string) (model.Segment, error) {
	var segment model.Segment
	err := query.QueryRowContext(ctx, `SELECT id,session_id,position,generation,state,text,capture_mode,commit_revision FROM segments
		WHERE id=? AND session_id=? AND message_id IS NULL AND state!='deleted'`, id, sessionID).
		Scan(&segment.ID, &segment.SessionID, &segment.Position, &segment.Generation, &segment.State, &segment.Text, &segment.CaptureMode, &segment.CommitRevision)
	segment.Bytes = len(segment.Text)
	return segment, missing(err)
}

func (s *Store) Segment(ctx context.Context, sessionID, id string) (model.Segment, error) {
	return readSegment(ctx, s.db, sessionID, id)
}

func draftFits(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var size int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(text AS BLOB))),0) FROM segments
		WHERE session_id=? AND message_id IS NULL AND state!='deleted' AND capture_mode=0`, sessionID).Scan(&size); err != nil {
		return err
	}
	if size > model.MaxDraftBytes {
		return ErrLimit
	}
	text, err := readDraftText(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if len(text) > model.MaxDraftBytes {
		return ErrLimit
	}
	return nil
}

func (s *Store) AddSegment(ctx context.Context, sessionID, id, text string, revision int64) (int64, error) {
	return s.addSegment(ctx, sessionID, id, text, revision, false)
}

// ReserveReplacement owns the one temporary slot allowed beyond a full draft.
// Its ID remains the capture/commit/abort identity if a response is lost.
func (s *Store) ReserveReplacement(ctx context.Context, sessionID, id string, revision int64) (int64, error) {
	return s.addSegment(ctx, sessionID, id, "", revision, true)
}

func (s *Store) addSegment(ctx context.Context, sessionID, id, text string, revision int64, replacement bool) (int64, error) {
	if !utf8.ValidString(text) || len(text) > model.MaxDraftBytes {
		return 0, ErrLimit
	}
	var next int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if replacement {
			segment, err := readCaptureSegment(ctx, tx, sessionID, id)
			if err == nil {
				if segment.CaptureMode != CaptureStaged || segment.State != "ready" ||
					segment.Generation != 1 || segment.Text != "" || segment.MessageID != "" {
					return ErrConflict
				}
				if err := checkDraft(ctx, tx, sessionID, revision+1); err != nil {
					return err
				}
				next = revision + 1
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		if err := checkDraft(ctx, tx, sessionID, revision); err != nil {
			return err
		}
		if err := checkNoReplacement(ctx, tx, sessionID, ""); err != nil {
			return err
		}
		limit, mode, stagedAt := model.MaxSegments, CaptureAppend, int64(0)
		if replacement {
			if err := checkOtherCaptures(ctx, tx, sessionID, ""); err != nil {
				return err
			}
			limit++
			mode, stagedAt = CaptureStaged, time.Now().UnixMilli()
		}
		var position int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),0)+1 FROM segments WHERE session_id=?`, sessionID).Scan(&position); err != nil {
			return err
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segments WHERE session_id=? AND message_id IS NULL AND state!='deleted'`, sessionID).Scan(&live); err != nil {
			return err
		}
		if live >= limit {
			return ErrLimit
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM segments WHERE id=?", id).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO segments(id,session_id,position,state,text,capture_mode,staged_at) VALUES(?,?,?,'ready',?,?,?)", id, sessionID, position, text, mode, stagedAt); err != nil {
			return err
		}
		if err := draftFits(ctx, tx, sessionID); err != nil {
			return err
		}
		var err error
		next, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	return next, err
}

func (s *Store) EditSegment(ctx context.Context, sessionID, id, text string, revision int64, deleted bool) (int64, error) {
	next, _, err := s.EditSegmentWithGeneration(ctx, sessionID, id, text, revision, deleted)
	return next, err
}

// EditSegmentWithGeneration returns the capture generation retired by this edit
// from the same transaction that changes the draft revision.
func (s *Store) EditSegmentWithGeneration(ctx context.Context, sessionID, id, text string, revision int64, deleted bool) (int64, int64, error) {
	if !utf8.ValidString(text) {
		return 0, 0, ErrLimit
	}
	state := "ready"
	if deleted {
		state, text = "deleted", ""
	}
	if len(text) > model.MaxDraftBytes {
		return 0, 0, ErrLimit
	}
	var next, retiredGeneration int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := checkDraft(ctx, tx, sessionID, revision); err != nil {
			return err
		}
		segment, err := readSegment(ctx, tx, sessionID, id)
		if err != nil {
			return err
		}
		retiredGeneration = segment.Generation
		if segment.CaptureMode != CaptureAppend && !deleted {
			return ErrBusy
		}
		if err := checkNoReplacement(ctx, tx, sessionID, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET text=?,state=?,generation=generation+1 WHERE id=?", text, state, id); err != nil {
			return err
		}
		if segment.CaptureMode == CaptureAppend {
			if err := draftFits(ctx, tx, sessionID); err != nil {
				return err
			}
		}
		next, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	return next, retiredGeneration, err
}

// A recognition update may only change the generation that started it.
func (s *Store) UpdateTranscript(ctx context.Context, sessionID, id string, generation int64, text, state string) (int64, error) {
	if !utf8.ValidString(text) || len(text) > model.MaxDraftBytes {
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

		mode := segment.CaptureMode
		stagedAt := int64(0)
		if mode != CaptureAppend {
			switch state {
			case "ready":
				if strings.TrimSpace(text) == "" {
					return ErrConflict
				}
				if mode == CaptureStaged {
					state = "staged"
					stagedAt = time.Now().UnixMilli()
				} else {
					if err := retireReplacementBase(ctx, tx, sessionID, id); err != nil {
						return err
					}
					mode = CaptureAppend
				}
			case "failed":
				var others int
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM segments WHERE session_id=? AND id!=? AND message_id IS NULL AND state!='deleted'", sessionID, id).Scan(&others); err != nil {
					return err
				}
				if mode == CaptureStaged || others > 0 {
					state, text = "deleted", ""
				} else {
					mode = CaptureAppend
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET text=?,state=?,capture_mode=?,staged_at=? WHERE id=?", text, state, mode, stagedAt, id); err != nil {
			return err
		}
		if mode == CaptureAppend {
			if err := draftFits(ctx, tx, sessionID); err != nil {
				return err
			}
		}
		next, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	return next, err
}

func (s *Store) Recover(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET draft_revision=draft_revision+1 WHERE id IN
			(SELECT session_id FROM segments WHERE (state='capturing' OR state='staged' OR (capture_mode=2 AND state='ready')) AND message_id IS NULL)`); err != nil {
			return err
		}
		// A pending staged recording was never accepted. Retain its tombstone for abort retries.
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET state='deleted',text='',staged_at=0,generation=generation+1 WHERE capture_mode=2 AND state IN ('ready','capturing','staged') AND message_id IS NULL"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE segments AS pending SET state='deleted',text='',generation=generation+1
			WHERE capture_mode=1 AND state='capturing' AND message_id IS NULL AND EXISTS
			(SELECT 1 FROM segments AS original WHERE original.session_id=pending.session_id AND original.id!=pending.id AND original.message_id IS NULL AND original.state!='deleted')`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET state='failed',capture_mode=0,generation=generation+1 WHERE state='capturing' AND message_id IS NULL"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE messages SET state='unconfirmed',error='gateway_restarted',revision=revision+1
			WHERE state IN ('sending','waiting','receiving','attention')`)
		return err
	})
}
