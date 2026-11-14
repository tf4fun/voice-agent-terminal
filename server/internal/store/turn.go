package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

// Turn selects one complete user/assistant exchange. Neighbor IDs refer to turns.
type Turn struct {
	Message *model.Message `json:"message"`
	Before  string         `json:"before"`
	After   string         `json:"after"`
}

func (s *Store) Turn(ctx context.Context, session, mode, anchor string) (Turn, error) {
	var result Turn
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := readSession(ctx, tx, session); err != nil {
			return err
		}
		query := "SELECT " + messageColumns + " FROM messages WHERE session_id=? ORDER BY rowid DESC LIMIT 1"
		args := []any{session}
		if mode != "latest" {
			if !model.ValidID(anchor) {
				return ErrConflict
			}
			var row int64
			if err := tx.QueryRowContext(ctx, "SELECT rowid FROM messages WHERE session_id=? AND id=?", session, anchor).Scan(&row); err != nil {
				return missing(err)
			}
			switch mode {
			case "message":
				query = "SELECT " + messageColumns + " FROM messages WHERE session_id=? AND rowid=?"
				args = append(args, row)
			case "before":
				query = "SELECT " + messageColumns + " FROM messages WHERE session_id=? AND rowid<? ORDER BY rowid DESC LIMIT 1"
				args = append(args, row)
			case "after":
				query = "SELECT " + messageColumns + " FROM messages WHERE session_id=? AND rowid>? ORDER BY rowid ASC LIMIT 1"
				args = append(args, row)
			default:
				return ErrConflict
			}
		}
		message, err := scanMessage(tx.QueryRowContext(ctx, query, args...))
		if errors.Is(err, ErrNotFound) && mode == "latest" {
			return nil
		}
		if err != nil {
			return err
		}
		result.Message = &message
		for _, neighbor := range []struct {
			comparison, order string
			target            *string
		}{{"<", "DESC", &result.Before}, {">", "ASC", &result.After}} {
			err := tx.QueryRowContext(ctx, "SELECT id FROM messages WHERE session_id=? AND rowid "+neighbor.comparison+" (SELECT rowid FROM messages WHERE id=?) ORDER BY rowid "+neighbor.order+" LIMIT 1", session, message.ID).Scan(neighbor.target)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		return nil
	})
	return result, err
}
