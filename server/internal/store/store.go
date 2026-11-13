// Package store persists gateway state using the voice-memo SQLite lifecycle.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var (
	ErrNotFound = errors.New("not_found")
	ErrConflict = errors.New("conflict")
	ErrStale    = errors.New("stale_revision")
	ErrBusy     = errors.New("busy")
	ErrLimit    = errors.New("limit_exceeded")
)

type Store struct{ db *sql.DB }

func Open(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "gateway.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize() error {
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err := s.db.Exec(pragma); err != nil {
			return err
		}
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("database schema %d is newer than this server", version)
	}
	if version == 1 {
		return nil
	}
	return s.transaction(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(schema); err != nil {
			return err
		}
		_, err := tx.Exec("PRAGMA user_version=1")
		return err
	})
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) transaction(ctx context.Context, change func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := change(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func missing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

type rowQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readSession(ctx context.Context, query rowQuery, id string) (model.Session, error) {
	var session model.Session
	err := query.QueryRowContext(ctx, `SELECT id,title,state,draft_revision,created_at,updated_at,error FROM sessions WHERE id=? AND state!='deleted'`, id).
		Scan(&session.ID, &session.Title, &session.State, &session.Revision, &session.CreatedAt, &session.UpdatedAt, &session.Error)
	return session, missing(err)
}

func (s *Store) Session(ctx context.Context, id string) (model.Session, error) {
	return readSession(ctx, s.db, id)
}

func (s *Store) BeginSession(ctx context.Context, id, title string) (model.Session, error) {
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var existingTitle, state string
		err := tx.QueryRowContext(ctx, "SELECT title,state FROM sessions WHERE id=?", id).Scan(&existingTitle, &state)
		if err == nil {
			if state == "deleted" || state == "deleting" || existingTitle != title {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := time.Now().UnixMilli()
		_, err = tx.ExecContext(ctx, "INSERT INTO sessions(id,title,state,created_at,updated_at) VALUES(?,?,'creating',?,?)", id, title, now, now)
		return err
	})
	if err != nil {
		return model.Session{}, err
	}
	return s.Session(ctx, id)
}

func (s *Store) SetSessionReady(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE sessions SET state='ready',error='',updated_at=? WHERE id=? AND state='creating'", time.Now().UnixMilli(), id)
	return err
}

func (s *Store) SessionError(ctx context.Context, id, code string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE sessions SET error=? WHERE id=? AND state IN ('creating','deleting')", code, id)
	return err
}

func (s *Store) Rename(ctx context.Context, id, title string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE sessions SET title=?,updated_at=? WHERE id=? AND state='ready'", title, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrConflict
	}
	return err
}

func (s *Store) Sessions(ctx context.Context, limit, offset int) ([]model.Session, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,title,state,draft_revision,created_at,updated_at,error FROM sessions WHERE state!='deleted' ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Session, 0, limit)
	for rows.Next() {
		var item model.Session
		if err := rows.Scan(&item.ID, &item.Title, &item.State, &item.Revision, &item.CreatedAt, &item.UpdatedAt, &item.Error); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) PendingSessions(ctx context.Context) ([]model.Session, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,title,state,draft_revision,created_at,updated_at,error FROM sessions WHERE state IN ('creating','deleting') LIMIT 16")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.Session
	for rows.Next() {
		var item model.Session
		if err := rows.Scan(&item.ID, &item.Title, &item.State, &item.Revision, &item.CreatedAt, &item.UpdatedAt, &item.Error); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) BeginDelete(ctx context.Context, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := readSession(ctx, tx, id); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE session_id=? AND state IN ('queued','sending','waiting','receiving','attention','unconfirmed')", id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrBusy
		}
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET state='deleting',error='',updated_at=? WHERE id=?", time.Now().UnixMilli(), id)
		return err
	})
}

func (s *Store) FinishDelete(ctx context.Context, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", id).Scan(&state); err != nil {
			return missing(err)
		}
		if state != "deleting" {
			return ErrConflict
		}
		for _, statement := range []string{"DELETE FROM segments WHERE session_id=?", "DELETE FROM messages WHERE session_id=?"} {
			if _, err := tx.ExecContext(ctx, statement, id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET state='deleted',title='',error='' WHERE id=?", id)
		return err
	})
}

func bumpDraft(ctx context.Context, tx *sql.Tx, sessionID string) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, "UPDATE sessions SET draft_revision=draft_revision+1,updated_at=? WHERE id=? AND state IN ('ready','creating') RETURNING draft_revision", time.Now().UnixMilli(), sessionID).Scan(&revision)
	return revision, missing(err)
}

func checkDraft(ctx context.Context, tx *sql.Tx, sessionID string, revision int64) error {
	session, err := readSession(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if session.State != "ready" && session.State != "creating" {
		return ErrBusy
	}
	if session.Revision != revision {
		return ErrStale
	}
	return nil
}
