package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func stagedFixture(t *testing.T, mode int, old string) (*Store, string, string, int64, int64) {
	t.Helper()
	ctx := context.Background()
	st, session := fixture(t)
	revision := int64(1)
	var err error
	if old != "" {
		revision, err = st.AddSegment(ctx, session, model.NewID(), old, revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	id := model.NewID()
	revision, err = st.AddSegment(ctx, session, id, "", revision)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := st.BeginCaptureMode(ctx, session, id, revision, mode)
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	return st, session, id, generation, current.Revision
}

func TestReplacementRecoveryPreservesReviewedDraftAndAppendPartial(t *testing.T) {
	for _, mode := range []int{CaptureAppend, CaptureReplace, CaptureStaged} {
		t.Run(map[int]string{CaptureAppend: "append", CaptureReplace: "immediate", CaptureStaged: "staged"}[mode], func(t *testing.T) {
			ctx := context.Background()
			st, session, id, generation, _ := stagedFixture(t, mode, "original draft")
			if _, err := st.UpdateTranscript(ctx, session, id, generation, "unfinished replacement", "capturing"); err != nil {
				t.Fatal(err)
			}
			if err := st.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			text, revision, err := st.DraftText(ctx, session)
			if err != nil {
				t.Fatal(err)
			}
			want := "original draft"
			if mode == CaptureAppend {
				want += "\n\nunfinished replacement"
			}
			if text != want {
				t.Fatalf("recovered=%q want=%q", text, want)
			}
			message, err := st.Send(ctx, session, model.NewID(), "model", "scope", revision)
			if err != nil || message.Input != want {
				t.Fatalf("send=%q %v", message.Input, err)
			}
		})
	}
}

func TestReplacementHasIndependentBoundedBudget(t *testing.T) {
	for _, mode := range []int{CaptureReplace, CaptureStaged} {
		t.Run(map[int]string{CaptureReplace: "immediate", CaptureStaged: "staged"}[mode], func(t *testing.T) {
			ctx := context.Background()
			old := strings.Repeat("a", model.MaxDraftBytes)
			st, session, id, generation, _ := stagedFixture(t, mode, old)
			if _, err := st.UpdateTranscript(ctx, session, id, generation, "short replacement", "capturing"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.UpdateTranscript(ctx, session, id, generation, strings.Repeat("b", model.MaxDraftBytes+1), "capturing"); !errors.Is(err, ErrLimit) {
				t.Fatalf("oversize=%v", err)
			}
			text, _, err := st.DraftText(ctx, session)
			if err != nil || text != old {
				t.Fatalf("visible draft changed: len=%d %v", len(text), err)
			}
			revision, err := st.UpdateTranscript(ctx, session, id, generation, "short replacement", "ready")
			if err != nil {
				t.Fatal(err)
			}
			if mode == CaptureStaged {
				revision, err = st.CommitReplacement(ctx, session, id, revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			text, _, err = st.DraftText(ctx, session)
			if err != nil || text != "short replacement" {
				t.Fatalf("final=%q %v", text, err)
			}
			if _, err := st.Send(ctx, session, model.NewID(), "model", "scope", revision); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStagedCommitAndAbortAreIdempotentAcrossLaterEdits(t *testing.T) {
	ctx := context.Background()
	st, session, id, generation, _ := stagedFixture(t, CaptureStaged, "old")
	revision, err := st.UpdateTranscript(ctx, session, id, generation, "new", "ready")
	if err != nil {
		t.Fatal(err)
	}
	segment, err := st.Segment(ctx, session, id)
	if err != nil || segment.State != "staged" {
		t.Fatalf("stage=%+v %v", segment, err)
	}
	if _, err := st.Send(ctx, session, model.NewID(), "model", "scope", revision); !errors.Is(err, ErrBusy) {
		t.Fatalf("pending send=%v", err)
	}
	committed, err := st.CommitReplacement(ctx, session, id, revision)
	if err != nil || committed != revision+1 {
		t.Fatalf("commit=%d %v", committed, err)
	}
	repeated, err := st.CommitReplacement(ctx, session, id, revision)
	if err != nil || repeated != committed {
		t.Fatalf("lost response=%d %v", repeated, err)
	}
	if _, err := st.CommitReplacement(ctx, session, id, revision+1); !errors.Is(err, ErrConflict) {
		t.Fatalf("different body accepted=%v", err)
	}
	current, err := st.AddSegment(ctx, session, model.NewID(), "later", committed)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err = st.CommitReplacement(ctx, session, id, revision)
	if err != nil || repeated != committed {
		t.Fatal("retry changed operation", repeated, err)
	}
	status, observed, err := st.AbortReplacement(ctx, session, id)
	if err != nil || status != "committed" || observed != current {
		t.Fatalf("abort after commit=%s %d %v", status, observed, err)
	}
	text, _, err := st.DraftText(ctx, session)
	if err != nil || text != "new\n\nlater" {
		t.Fatalf("later draft=%q %v", text, err)
	}
	if _, err := st.Send(ctx, session, model.NewID(), "model", "scope", current); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitReplacement(ctx, session, id, revision); err != nil {
		t.Fatalf("retry after send=%v", err)
	}
}

func TestStagedAbortWinsCommitAndRejectsLateRecognition(t *testing.T) {
	ctx := context.Background()
	st, session, id, generation, _ := stagedFixture(t, CaptureStaged, "old")
	revision, err := st.UpdateTranscript(ctx, session, id, generation, "new", "ready")
	if err != nil {
		t.Fatal(err)
	}
	status, next, err := st.AbortReplacement(ctx, session, id)
	if err != nil || status != "cancelled" {
		t.Fatal(status, err)
	}
	repeatedStatus, repeatedRevision, err := st.AbortReplacement(ctx, session, id)
	if err != nil || repeatedStatus != status || repeatedRevision != next {
		t.Fatalf("repeat abort=%s %d %v", repeatedStatus, repeatedRevision, err)
	}
	if _, err := st.CommitReplacement(ctx, session, id, revision); err == nil {
		t.Fatal("cancelled stage committed")
	}
	if _, err := st.UpdateTranscript(ctx, session, id, generation, "late", "ready"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late=%v", err)
	}
	text, _, err := st.DraftText(ctx, session)
	if err != nil || text != "old" {
		t.Fatal(text, err)
	}
}

func TestStagedCommitAbortRaceHasOneOutcome(t *testing.T) {
	for iteration := 0; iteration < 16; iteration++ {
		ctx := context.Background()
		st, session, id, generation, _ := stagedFixture(t, CaptureStaged, "old")
		revision, err := st.UpdateTranscript(ctx, session, id, generation, "new", "ready")
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		var commitErr, abortErr error
		var status string
		go func() { defer workers.Done(); <-start; _, commitErr = st.CommitReplacement(ctx, session, id, revision) }()
		go func() { defer workers.Done(); <-start; status, _, abortErr = st.AbortReplacement(ctx, session, id) }()
		close(start)
		workers.Wait()
		if abortErr != nil {
			t.Fatal(abortErr)
		}
		text, _, err := st.DraftText(ctx, session)
		if err != nil {
			t.Fatal(err)
		}
		if status == "committed" {
			if commitErr != nil || text != "new" {
				t.Fatal("commit outcome", commitErr, text)
			}
		} else if status == "cancelled" {
			if commitErr == nil || text != "old" {
				t.Fatal("abort outcome", commitErr, text)
			}
		} else {
			t.Fatal(status)
		}
	}
}

func TestPendingReplacementSerializesSessionEditsAndCaptures(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	old, stage, other := model.NewID(), model.NewID(), model.NewID()
	revision, _ := st.AddSegment(ctx, session, old, "old", 1)
	revision, _ = st.AddSegment(ctx, session, stage, "", revision)
	revision, _ = st.AddSegment(ctx, session, other, "", revision)
	if _, err := st.BeginCaptureMode(ctx, session, stage, revision, CaptureStaged); err != nil {
		t.Fatal(err)
	}
	current, _ := st.Session(ctx, session)
	revision = current.Revision
	if _, err := st.AddSegment(ctx, session, model.NewID(), "concurrent", revision); !errors.Is(err, ErrBusy) {
		t.Fatal("add", err)
	}
	if _, err := st.EditSegment(ctx, session, old, "concurrent", revision, false); !errors.Is(err, ErrBusy) {
		t.Fatal("edit", err)
	}
	if _, err := st.EditSegment(ctx, session, old, "", revision, true); !errors.Is(err, ErrBusy) {
		t.Fatal("delete base", err)
	}
	if _, err := st.BeginCaptureMode(ctx, session, other, revision, CaptureAppend); !errors.Is(err, ErrBusy) {
		t.Fatal("capture", err)
	}
	if _, err := st.EditSegment(ctx, session, stage, "", revision, true); err != nil {
		t.Fatal("cancel via old DELETE", err)
	}
	status, _, err := st.AbortReplacement(ctx, session, stage)
	if err != nil || status != "cancelled" {
		t.Fatal(status, err)
	}
	current, _ = st.Session(ctx, session)
	if _, err := st.BeginCaptureMode(ctx, session, other, current.Revision, CaptureAppend); err != nil {
		t.Fatal("released capture", err)
	}
}

func TestStagedExpiresButCommittedResultSurvives(t *testing.T) {
	ctx := context.Background()
	st, session, id, generation, _ := stagedFixture(t, CaptureStaged, "old")
	revision, err := st.UpdateTranscript(ctx, session, id, generation, "new", "ready")
	if err != nil {
		t.Fatal(err)
	}
	var readyAt int64
	if err := st.db.QueryRow("SELECT staged_at FROM segments WHERE id=?", id).Scan(&readyAt); err != nil || readyAt == 0 {
		t.Fatal(readyAt, err)
	}
	changed, err := st.ExpireStaged(ctx, time.UnixMilli(readyAt).Add(59*time.Second))
	if err != nil || len(changed) != 0 {
		t.Fatal(changed, err)
	}
	changed, err = st.ExpireStaged(ctx, time.UnixMilli(readyAt).Add(61*time.Second))
	if err != nil || len(changed) != 1 {
		t.Fatal(changed, err)
	}
	if _, err := st.CommitReplacement(ctx, session, id, revision); err == nil {
		t.Fatal("expired stage committed")
	}
	if status, _, err := st.AbortReplacement(ctx, session, id); err != nil || status != "cancelled" {
		t.Fatal(status, err)
	}
	text, current, err := st.DraftText(ctx, session)
	if err != nil || text != "old" {
		t.Fatal(text, err)
	}
	next := model.NewID()
	current, err = st.AddSegment(ctx, session, next, "", current)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := st.BeginCaptureMode(ctx, session, next, current, CaptureStaged)
	if err != nil {
		t.Fatal(err)
	}
	current, err = st.UpdateTranscript(ctx, session, next, gen, "accepted", "ready")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitReplacement(ctx, session, next, current); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.ExpireStaged(ctx, time.Now().Add(2*time.Minute)); err != nil || len(changed) != 0 {
		t.Fatal(changed, err)
	}
	if status, _, err := st.AbortReplacement(ctx, session, next); err != nil || status != "committed" {
		t.Fatal(status, err)
	}
}

func TestStagedReadyRecoveryAfterReopen(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	st, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	session, old, id := model.NewID(), model.NewID(), model.NewID()
	if _, err := st.BeginSession(ctx, session, "persisted"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionReady(ctx, session); err != nil {
		t.Fatal(err)
	}
	revision, _ := st.AddSegment(ctx, session, old, "old", 1)
	revision, _ = st.AddSegment(ctx, session, id, "", revision)
	gen, err := st.BeginCaptureMode(ctx, session, id, revision, CaptureStaged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTranscript(ctx, session, id, gen, "new", "ready"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if status, _, err := st.AbortReplacement(ctx, session, id); err != nil || status != "cancelled" {
		t.Fatal(status, err)
	}
	text, _, err := st.DraftText(ctx, session)
	if err != nil || text != "old" {
		t.Fatal(text, err)
	}
}

func TestUnsupportedSchemaIsRejectedWithoutMigration(t *testing.T) {
	for _, version := range []int{1, 3} {
		directory := t.TempDir()
		path := filepath.Join(directory, "gateway.db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if opened, err := Open(directory); err == nil {
			opened.Close()
			t.Fatal("unsupported schema accepted")
		}
		db, err = sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		var observed int
		err = db.QueryRow("PRAGMA user_version").Scan(&observed)
		db.Close()
		if err != nil || observed != version {
			t.Fatal("unsupported schema was changed", observed, err)
		}
	}
}

func TestCaptureFailureKeepsAppendAndImmediateSemantics(t *testing.T) {
	for _, mode := range []int{CaptureAppend, CaptureReplace, CaptureStaged} {
		for _, old := range []string{"", "reviewed"} {
			ctx := context.Background()
			st, session, id, generation, _ := stagedFixture(t, mode, old)
			if _, err := st.UpdateTranscript(ctx, session, id, generation, "partial", "capturing"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.UpdateTranscript(ctx, session, id, generation, "partial", "failed"); err != nil {
				t.Fatal(err)
			}
			expected := old
			if mode == CaptureAppend {
				if old != "" {
					expected += "\n\n"
				}
				expected += "partial"
			}
			if mode == CaptureReplace && old == "" {
				expected = "partial"
			}
			text, _, err := st.DraftText(ctx, session)
			if err != nil || text != expected {
				t.Fatalf("mode=%d old=%q got=%q want=%q err=%v", mode, old, text, expected, err)
			}
		}
	}
}

func TestAbortUnstartedEmptySegmentAndRejectOrdinaryDraft(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	draft, empty := model.NewID(), model.NewID()
	revision, err := st.AddSegment(ctx, session, draft, "reviewed", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AbortReplacement(ctx, session, draft); !errors.Is(err, ErrConflict) {
		t.Fatal("ordinary draft aborted", err)
	}
	revision, err = st.AddSegment(ctx, session, empty, "", revision)
	if err != nil {
		t.Fatal(err)
	}
	status, next, err := st.AbortReplacement(ctx, session, empty)
	if err != nil || status != "cancelled" || next != revision+1 {
		t.Fatal(status, next, err)
	}
	if status, repeated, err := st.AbortReplacement(ctx, session, empty); err != nil || status != "cancelled" || repeated != next {
		t.Fatal(status, repeated, err)
	}
	text, _, err := st.DraftText(ctx, session)
	if err != nil || text != "reviewed" {
		t.Fatal(text, err)
	}
}

func TestStagedCaptureRequiresFreshOperationID(t *testing.T) {
	ctx := context.Background()
	st, session, id, generation, _ := stagedFixture(t, CaptureStaged, "old")
	input, err := st.UpdateTranscript(ctx, session, id, generation, "accepted", "ready")
	if err != nil {
		t.Fatal(err)
	}
	output, err := st.CommitReplacement(ctx, session, id, input)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := st.EditSegment(ctx, session, id, "", output, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginCaptureMode(ctx, session, id, revision, CaptureStaged); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused staged ID=%v", err)
	}
	status, _, err := st.AbortReplacement(ctx, session, id)
	if err != nil || status != "committed" {
		t.Fatal(status, err)
	}
	repeated, err := st.CommitReplacement(ctx, session, id, input)
	if err != nil || repeated != output {
		t.Fatal(repeated, err)
	}
}

func TestAppendCaptureReusePreservesStagedCommitReceipt(t *testing.T) {
	for _, mode := range []int{CaptureAppend, CaptureReplace} {
		ctx := context.Background()
		st, session, id, generation, _ := stagedFixture(t, CaptureStaged, "old")
		input, err := st.UpdateTranscript(ctx, session, id, generation, "accepted", "ready")
		if err != nil {
			t.Fatal(err)
		}
		output, err := st.CommitReplacement(ctx, session, id, input)
		if err != nil {
			t.Fatal(err)
		}
		revision, err := st.EditSegment(ctx, session, id, "", output, false)
		if err != nil {
			t.Fatal(err)
		}
		nextGeneration, err := st.BeginCaptureMode(ctx, session, id, revision, mode)
		if err != nil {
			t.Fatalf("append reuse=%v", err)
		}
		if nextGeneration <= generation {
			t.Fatal("generation not advanced")
		}
		if _, err := st.UpdateTranscript(ctx, session, id, nextGeneration, "later draft", "capturing"); err != nil {
			t.Fatal(err)
		}
		status, _, err := st.AbortReplacement(ctx, session, id)
		if err != nil || status != "committed" {
			t.Fatal(status, err)
		}
		segment, err := st.Segment(ctx, session, id)
		if err != nil || segment.State != "capturing" || segment.Text != "later draft" {
			t.Fatalf("old abort touched later capture=%+v %v", segment, err)
		}
		repeated, err := st.CommitReplacement(ctx, session, id, input)
		if err != nil || repeated != output {
			t.Fatalf("old receipt lost=%d %v", repeated, err)
		}
		if _, err := st.UpdateTranscript(ctx, session, id, nextGeneration, "later draft", "ready"); err != nil {
			t.Fatal(err)
		}
		text, _, err := st.DraftText(ctx, session)
		if err != nil || text != "later draft" {
			t.Fatal(text, err)
		}
	}
}

func TestAbortUnstartedExceptionRejectsReusedAppendGeneration(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	id := model.NewID()
	revision, err := st.AddSegment(ctx, session, id, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := st.BeginCaptureMode(ctx, session, id, revision, CaptureAppend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTranscript(ctx, session, id, generation, "", "ready"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AbortReplacement(ctx, session, id); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused empty result treated as unstarted=%v", err)
	}
	if _, err := st.Segment(ctx, session, id); err != nil {
		t.Fatal("existing append generation removed", err)
	}
}

func TestFullDraftReplacementReservationLifecycle(t *testing.T) {
	for _, outcome := range []string{"commit", "abort", "expire", "recover"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			st, session := fixture(t)
			revision := int64(1)
			for index := 0; index < model.MaxSegments; index++ {
				var err error
				revision, err = st.AddSegment(ctx, session, model.NewID(), "old part", revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			original, _, err := st.DraftText(ctx, session)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddSegment(ctx, session, model.NewID(), "", revision); !errors.Is(err, ErrLimit) {
				t.Fatalf("ordinary append: %v", err)
			}
			id := model.NewID()
			reserved, err := st.ReserveReplacement(ctx, session, id, revision)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := st.ReserveReplacement(ctx, session, id, revision)
			if err != nil || replay != reserved {
				t.Fatalf("reservation replay %d %v", replay, err)
			}
			_, _, total, err := st.Draft(ctx, session, 4, 0)
			if err != nil || total != model.MaxSegments+1 {
				t.Fatalf("temporary entries %d %v", total, err)
			}
			if _, err := st.ReserveReplacement(ctx, session, model.NewID(), reserved); !errors.Is(err, ErrBusy) {
				t.Fatalf("second slot: %v", err)
			}
			if _, err := st.AddSegment(ctx, session, model.NewID(), "extra", reserved); !errors.Is(err, ErrBusy) {
				t.Fatalf("append while reserved: %v", err)
			}
			if _, err := st.Send(ctx, session, model.NewID(), "model", "scope", reserved); !errors.Is(err, ErrBusy) {
				t.Fatalf("send while reserved: %v", err)
			}
			if _, err := st.BeginCaptureMode(ctx, session, id, reserved, CaptureAppend); !errors.Is(err, ErrConflict) {
				t.Fatalf("reservation reused as append: %v", err)
			}
			expected := original
			switch outcome {
			case "commit":
				generation, err := st.BeginCaptureMode(ctx, session, id, reserved, CaptureStaged)
				if err != nil {
					t.Fatal(err)
				}
				staged, err := st.UpdateTranscript(ctx, session, id, generation, "replacement text", "ready")
				if err != nil {
					t.Fatal(err)
				}
				committed, err := st.CommitReplacement(ctx, session, id, staged)
				if err != nil {
					t.Fatal(err)
				}
				repeated, err := st.CommitReplacement(ctx, session, id, staged)
				if err != nil || repeated != committed {
					t.Fatal("commit replay changed", err)
				}
				expected = "replacement text"
			case "abort":
				status, _, err := st.AbortReplacement(ctx, session, id)
				if err != nil || status != "cancelled" {
					t.Fatal(status, err)
				}
			case "expire":
				expired, err := st.ExpireStaged(ctx, time.Now().Add(2*time.Minute))
				if err != nil || len(expired) != 1 {
					t.Fatal(expired, err)
				}
			case "recover":
				if err := st.Recover(ctx); err != nil {
					t.Fatal(err)
				}
			}
			actual, current, err := st.DraftText(ctx, session)
			if err != nil || actual != expected {
				t.Fatalf("draft changed incorrectly: %v", err)
			}
			_, _, total, err = st.Draft(ctx, session, 4, 0)
			expectedTotal := model.MaxSegments
			if outcome == "commit" {
				expectedTotal = 1
			}
			if err != nil || total != expectedTotal {
				t.Fatalf("final entries %d %v", total, err)
			}
			// Every terminal outcome frees the one reservation slot.
			if _, err := st.ReserveReplacement(ctx, session, model.NewID(), current); err != nil {
				t.Fatal("slot was not released", err)
			}
		})
	}
}
