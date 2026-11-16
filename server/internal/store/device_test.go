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

func TestSchemaTwoMigratesToThree(t *testing.T) {
	directory := t.TempDir()
	st, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	db, err := sql.Open("sqlite", filepath.Join(directory, "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE device_reports; PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.AddDeviceReport(context.Background(), DeviceReport{ID: "a", Trigger: "t", Firmware: "f", ResetReason: "r", Summary: "s", Body: "b"}); err != nil {
		t.Fatal("migrated store cannot hold reports", err)
	}
	var version int
	if err := st.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatal("version", version, err)
	}
}
