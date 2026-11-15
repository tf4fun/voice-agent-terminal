package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Device diagnostic reports: what a terminal uploads after a failure. Only the
// newest reports are kept; they hold event codes and log lines, never audio,
// transcripts or credentials.
const deviceReportsSchema = `CREATE TABLE device_reports (
    id TEXT PRIMARY KEY,
    received_at INTEGER NOT NULL,
    trigger TEXT NOT NULL,
    firmware TEXT NOT NULL,
    reset_reason TEXT NOT NULL,
    summary TEXT NOT NULL,
    body TEXT NOT NULL
);
CREATE INDEX device_reports_received ON device_reports(received_at);`

// MaxDeviceReports bounds retained reports; older ones are deleted on insert.
const MaxDeviceReports = 100

type DeviceReport struct {
	ID          string `json:"id"`
	ReceivedAt  int64  `json:"received_at"`
	Trigger     string `json:"trigger"`
	Firmware    string `json:"firmware"`
	ResetReason string `json:"reset_reason"`
	Summary     string `json:"summary"`
	Body        string `json:"-"`
}

func (s *Store) AddDeviceReport(ctx context.Context, report DeviceReport) error {
	if report.ReceivedAt == 0 {
		report.ReceivedAt = time.Now().UnixMilli()
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_reports(id,received_at,trigger,firmware,reset_reason,summary,body) VALUES(?,?,?,?,?,?,?)`,
			report.ID, report.ReceivedAt, report.Trigger, report.Firmware, report.ResetReason, report.Summary, report.Body); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM device_reports WHERE id NOT IN
			(SELECT id FROM device_reports ORDER BY received_at DESC, rowid DESC LIMIT ?)`, MaxDeviceReports)
		return err
	})
}

func (s *Store) DeviceReports(ctx context.Context, limit, offset int) ([]DeviceReport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,received_at,trigger,firmware,reset_reason,summary FROM device_reports
		ORDER BY received_at DESC, rowid DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DeviceReport, 0, limit)
	for rows.Next() {
		var item DeviceReport
		if err := rows.Scan(&item.ID, &item.ReceivedAt, &item.Trigger, &item.Firmware, &item.ResetReason, &item.Summary); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeviceReport(ctx context.Context, id string) (DeviceReport, error) {
	var item DeviceReport
	err := s.db.QueryRowContext(ctx, `SELECT id,received_at,trigger,firmware,reset_reason,summary,body FROM device_reports WHERE id=?`, id).
		Scan(&item.ID, &item.ReceivedAt, &item.Trigger, &item.Firmware, &item.ResetReason, &item.Summary, &item.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}
