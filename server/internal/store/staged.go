package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

// These values are persisted in SQLite and must retain their meanings.
const (
	CaptureAppend  = 0
	CaptureReplace = 1
	CaptureStaged  = 2
)

func checkNoReplacement(ctx context.Context, tx *sql.Tx, sessionID, exceptID string) error {
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM segments WHERE session_id=? AND id!=? AND message_id IS NULL AND capture_mode IN (1,2) AND state IN ('ready','capturing','staged')", sessionID, exceptID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return ErrBusy
	}
	return nil
}

func checkOtherCaptures(ctx context.Context, tx *sql.Tx, sessionID, exceptID string) error {
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM segments WHERE session_id=? AND id!=? AND message_id IS NULL AND state IN ('capturing','staged')", sessionID, exceptID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return ErrBusy
	}
	return nil
}

func retireReplacementBase(ctx context.Context, tx *sql.Tx, sessionID, id string) error {
	_, err := tx.ExecContext(ctx, "UPDATE segments SET state='deleted',text='',generation=generation+1 WHERE session_id=? AND id!=? AND message_id IS NULL AND state!='deleted'", sessionID, id)
	return err
}

func (s *Store) BeginCaptureMode(ctx context.Context, sessionID, id string, revision int64, mode int) (int64, error) {
	if mode < CaptureAppend || mode > CaptureStaged {
		return 0, ErrConflict
	}
	var generation int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := checkDraft(ctx, tx, sessionID, revision); err != nil {
			return err
		}
		if err := checkNoReplacement(ctx, tx, sessionID, id); err != nil {
			return err
		}
		segment, err := readSegment(ctx, tx, sessionID, id)
		if err != nil {
			return err
		}
		if segment.CaptureMode == CaptureStaged && mode != CaptureStaged {
			return ErrConflict
		}
		if segment.State == "capturing" || segment.State == "staged" {
			return ErrBusy
		}
		if segment.Text != "" {
			return ErrConflict
		}
		// A staged segment ID is also its commit/abort operation identity.
		if mode == CaptureStaged && (segment.Generation != 1 || segment.CommitRevision != 0) {
			return ErrConflict
		}
		if mode != CaptureAppend {
			if err := checkOtherCaptures(ctx, tx, sessionID, id); err != nil {
				return err
			}
		}
		if err := tx.QueryRowContext(ctx, "UPDATE segments SET state='capturing',capture_mode=?,staged_at=0,generation=generation+1 WHERE id=? RETURNING generation", mode, id).Scan(&generation); err != nil {
			return err
		}
		_, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	return generation, err
}

func readCaptureSegment(ctx context.Context, tx *sql.Tx, sessionID, id string) (model.Segment, error) {
	var segment model.Segment
	err := tx.QueryRowContext(ctx, "SELECT id,session_id,position,generation,state,text,capture_mode,commit_revision,COALESCE(message_id,'') FROM segments WHERE id=? AND session_id=?", id, sessionID).Scan(&segment.ID, &segment.SessionID, &segment.Position, &segment.Generation, &segment.State, &segment.Text, &segment.CaptureMode, &segment.CommitRevision, &segment.MessageID)
	segment.Bytes = len(segment.Text)
	return segment, missing(err)
}

// CommitReplacement commits exactly one reviewed staged revision. Its recorded
// output revision makes retries safe after a lost response or later draft edits.
func (s *Store) CommitReplacement(ctx context.Context, sessionID, id string, revision int64) (int64, error) {
	var next int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := readSession(ctx, tx, sessionID); err != nil {
			return err
		}
		segment, err := readCaptureSegment(ctx, tx, sessionID, id)
		if err != nil {
			return err
		}
		if segment.CommitRevision > 0 {
			if revision+1 != segment.CommitRevision {
				return ErrConflict
			}
			next = segment.CommitRevision
			return nil
		}
		if err := checkDraft(ctx, tx, sessionID, revision); err != nil {
			return err
		}
		if segment.CaptureMode != CaptureStaged || segment.State != "staged" {
			return ErrConflict
		}
		if err := checkOtherCaptures(ctx, tx, sessionID, id); err != nil {
			return err
		}
		if err := retireReplacementBase(ctx, tx, sessionID, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET state='ready',capture_mode=0,staged_at=0 WHERE id=?", id); err != nil {
			return err
		}
		next, err = bumpDraft(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE segments SET commit_revision=? WHERE id=?", next, id)
		return err
	})
	return next, err
}

// AbortReplacement reports a committed operation without removing its result.
// A deleted staged tombstone makes cancellation idempotent across retries.
func (s *Store) AbortReplacement(ctx context.Context, sessionID, id string) (string, int64, error) {
	var status string
	var revision int64
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		session, err := readSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		revision = session.Revision
		segment, err := readCaptureSegment(ctx, tx, sessionID, id)
		if err != nil {
			return err
		}
		if segment.CommitRevision > 0 {
			status = "committed"
			return nil
		}
		unstarted := segment.Generation == 1 && (segment.CaptureMode == CaptureAppend || segment.CaptureMode == CaptureStaged) && segment.State == "ready" && segment.Text == "" && segment.MessageID == ""
		if segment.CaptureMode != CaptureStaged && !unstarted {
			return ErrConflict
		}
		status = "cancelled"
		if segment.State == "deleted" {
			return nil
		}
		if segment.State != "capturing" && segment.State != "staged" && !unstarted {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "UPDATE segments SET state='deleted',text='',capture_mode=2,staged_at=0,generation=generation+1 WHERE id=?", id); err != nil {
			return err
		}
		revision, err = bumpDraft(ctx, tx, sessionID)
		return err
	})
	return status, revision, err
}

// ExpireStaged retires reservations and confirmations pending for one minute. Each
// pass owns a limited batch, independent of the total session count.
func (s *Store) ExpireStaged(ctx context.Context, now time.Time) ([]model.Session, error) {
	changed := make([]model.Session, 0, 64)
	cutoff := now.Add(-time.Minute).UnixMilli()
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM sessions WHERE state IN ('ready','creating') AND id IN
   (SELECT session_id FROM segments WHERE capture_mode=2 AND state IN ('ready','staged') AND message_id IS NULL AND staged_at<=?) LIMIT 64`, cutoff)
		if err != nil {
			return err
		}
		for rows.Next() {
			var session model.Session
			if err := rows.Scan(&session.ID); err != nil {
				rows.Close()
				return err
			}
			changed = append(changed, session)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for i := range changed {
			id := changed[i].ID
			if _, err := tx.ExecContext(ctx, "UPDATE segments SET state='deleted',text='',staged_at=0,generation=generation+1 WHERE session_id=? AND capture_mode=2 AND state IN ('ready','staged') AND message_id IS NULL AND staged_at<=?", id, cutoff); err != nil {
				return err
			}
			changed[i].Revision, err = bumpDraft(ctx, tx, id)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}
