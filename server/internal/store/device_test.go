package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestDeviceReportsAreBoundedAndOrdered(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for i := 0; i < MaxDeviceReports+5; i++ {
		if err := st.AddDeviceReport(ctx, DeviceReport{ID: fmt.Sprintf("r%03d", i), ReceivedAt: int64(1000 + i), Trigger: "record_failed",
			Firmware: "0.2.0", ResetReason: "sw", Summary: "s", Body: fmt.Sprintf("body %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.DeviceReports(ctx, 200, 0)
	if err != nil || len(items) != MaxDeviceReports || items[0].ID != "r104" || items[len(items)-1].ID != "r005" {
		t.Fatalf("retained reports: %d %v", len(items), err)
	}
	report, err := st.DeviceReport(ctx, "r104")
	if err != nil || report.Body != "body 104" {
		t.Fatal(report, err)
	}
	if _, err := st.DeviceReport(ctx, "r000"); err != ErrNotFound {
		t.Fatal("pruned report still readable", err)
	}
}

// Schema 5 stores message prompts; an earlier database is refused rather than
// migrated, and a fresh one starts at the current version.
func TestEarlierSchemaIsRefused(t *testing.T) {
	directory := t.TempDir()
	st, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := st.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatal("version", version, err)
	}
	st.Close()
	db, err := sql.Open("sqlite", filepath.Join(directory, "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if st, err = Open(directory); err == nil {
		st.Close()
		t.Fatal("schema 4 opened")
	}
}

// Each message keeps the system prompt it was sent with.
func TestSendStoresTheSystemPrompt(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	session, id := "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2"
	if _, _, err := st.Send(ctx, session, id, "m", "scope", "简短回答", "新消息", true); err != nil {
		t.Fatal(err)
	}
	if message, err := st.Message(ctx, session, id); err != nil || message.SystemPrompt != "简短回答" {
		t.Fatalf("stored prompt %+v %v", message, err)
	}
}
