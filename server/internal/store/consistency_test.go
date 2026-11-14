package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func failedMessageFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	ctx := context.Background()
	st, session := fixture(t)
	id := model.NewID()
	revision, err := st.AddSegment(ctx, session, model.NewID(), "reviewed input", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, session, id, "model", "scope", revision); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageState(ctx, id, "failed", "error", "upstream_failure"); err != nil {
		t.Fatal(err)
	}
	return st, session, id
}

func TestDeleteBeforeRetryCannotQueueOrClaimMessage(t *testing.T) {
	ctx := context.Background()
	st, session, id := failedMessageFixture(t)
	if err := st.BeginDelete(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := st.Retry(ctx, session, id); !errors.Is(err, ErrBusy) {
		t.Fatalf("retry during delete=%v", err)
	}
	if _, err := st.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session queued work=%v", err)
	}
	message, err := st.Message(ctx, session, id)
	if err != nil || message.State != "failed" {
		t.Fatal(message, err)
	}
	if err := st.FinishDelete(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := st.Retry(ctx, session, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry deleted session=%v", err)
	}
}

func TestRetryBeforeDeleteKeepsSessionReady(t *testing.T) {
	ctx := context.Background()
	st, session, id := failedMessageFixture(t)
	if err := st.Retry(ctx, session, id); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginDelete(ctx, session); !errors.Is(err, ErrBusy) {
		t.Fatalf("delete queued retry=%v", err)
	}
	current, err := st.Session(ctx, session)
	if err != nil || current.State != "ready" {
		t.Fatal(current, err)
	}
	message, err := st.Claim(ctx)
	if err != nil || message.ID != id {
		t.Fatal(message, err)
	}
}

func TestConcurrentRetryAndDeleteHaveOneWinner(t *testing.T) {
	for iteration := 0; iteration < 16; iteration++ {
		ctx := context.Background()
		st, session, id := failedMessageFixture(t)
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		var retried, deleted error
		go func() {
			defer workers.Done()
			<-start
			retried = st.Retry(ctx, session, id)
		}()
		go func() {
			defer workers.Done()
			<-start
			deleted = st.BeginDelete(ctx, session)
		}()
		close(start)
		workers.Wait()
		current, err := st.Session(ctx, session)
		if err != nil {
			t.Fatal(err)
		}
		message, err := st.Message(ctx, session, id)
		if err != nil {
			t.Fatal(err)
		}
		if retried == nil {
			if !errors.Is(deleted, ErrBusy) || current.State != "ready" || message.State != "queued" {
				t.Fatalf("retry winner=%v %v %+v %+v", retried, deleted, current, message)
			}
		} else if deleted == nil {
			if !errors.Is(retried, ErrBusy) || current.State != "deleting" || message.State != "failed" {
				t.Fatalf("delete winner=%v %v %+v %+v", retried, deleted, current, message)
			}
		} else {
			t.Fatal("neither operation won", retried, deleted)
		}
	}
}

func TestAddSegmentAccountsForCanonicalJoinSeparators(t *testing.T) {
	for _, padding := range []int{3, 2, 1} {
		ctx := context.Background()
		st, session := fixture(t)
		first := strings.Repeat("a", model.MaxDraftBytes-padding)
		revision, err := st.AddSegment(ctx, session, model.NewID(), first, 1)
		if err != nil {
			t.Fatal(err)
		}
		id := model.NewID()
		next, err := st.AddSegment(ctx, session, id, "b", revision)
		if padding == 3 {
			if err != nil {
				t.Fatal(err)
			}
			text, observed, err := st.DraftText(ctx, session)
			if err != nil || len(text) != model.MaxDraftBytes || observed != next {
				t.Fatalf("boundary len=%d revision=%d err=%v", len(text), observed, err)
			}
			message, err := st.Send(ctx, session, model.NewID(), "model", "scope", next)
			if err != nil || message.Input != text {
				t.Fatal("accepted boundary could not send", err)
			}
		} else {
			if !errors.Is(err, ErrLimit) {
				t.Fatalf("over-budget insert=%v", err)
			}
			if _, err := st.Segment(ctx, session, id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("rejected insert persisted=%v", err)
			}
			text, observed, err := st.DraftText(ctx, session)
			if err != nil || text != first || observed != revision {
				t.Fatal("insert rollback", observed, err)
			}
		}
	}
}

func TestEditSegmentRollsBackJoinedBudgetOverflow(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	revision, err := st.AddSegment(ctx, session, model.NewID(), strings.Repeat("a", model.MaxDraftBytes-4), 1)
	if err != nil {
		t.Fatal(err)
	}
	id := model.NewID()
	revision, err = st.AddSegment(ctx, session, id, "b", revision)
	if err != nil {
		t.Fatal(err)
	}
	revision, err = st.EditSegment(ctx, session, id, "bc", revision, false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.Segment(ctx, session, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EditSegment(ctx, session, id, "bcd", revision, false); !errors.Is(err, ErrLimit) {
		t.Fatalf("over-budget edit=%v", err)
	}
	after, err := st.Segment(ctx, session, id)
	if err != nil || after.Text != before.Text || after.Generation != before.Generation {
		t.Fatalf("edit rollback=%+v %v", after, err)
	}
	text, observed, err := st.DraftText(ctx, session)
	if err != nil || len(text) != model.MaxDraftBytes || observed != revision {
		t.Fatal(len(text), observed, err)
	}
	if _, err := st.Send(ctx, session, model.NewID(), "model", "scope", revision); err != nil {
		t.Fatal("accepted edit could not send", err)
	}
}

func TestAppendTranscriptRollsBackJoinedBudgetOverflow(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	revision, err := st.AddSegment(ctx, session, model.NewID(), strings.Repeat("a", model.MaxDraftBytes-4), 1)
	if err != nil {
		t.Fatal(err)
	}
	id := model.NewID()
	revision, err = st.AddSegment(ctx, session, id, "", revision)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := st.BeginCaptureMode(ctx, session, id, revision, CaptureAppend)
	if err != nil {
		t.Fatal(err)
	}
	revision, err = st.UpdateTranscript(ctx, session, id, generation, "bc", "capturing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTranscript(ctx, session, id, generation, "bcd", "ready"); !errors.Is(err, ErrLimit) {
		t.Fatalf("over-budget transcript=%v", err)
	}
	segment, err := st.Segment(ctx, session, id)
	if err != nil || segment.Text != "bc" || segment.State != "capturing" {
		t.Fatalf("transcript rollback=%+v %v", segment, err)
	}
	current, err := st.Session(ctx, session)
	if err != nil || current.Revision != revision {
		t.Fatal(current, err)
	}
	revision, err = st.UpdateTranscript(ctx, session, id, generation, "bc", "failed")
	if err != nil {
		t.Fatal(err)
	}
	message, err := st.Send(ctx, session, model.NewID(), "model", "scope", revision)
	if err != nil || len(message.Input) != model.MaxDraftBytes {
		t.Fatal(len(message.Input), err)
	}
}

func TestUnicodeWhitespaceStillConsumesRawDraftBudget(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	white := strings.Repeat("\u2003", model.MaxDraftBytes/3)
	whiteID, id := model.NewID(), model.NewID()
	revision, err := st.AddSegment(ctx, session, whiteID, white, 1)
	if err != nil {
		t.Fatal(err)
	}
	revision, err = st.AddSegment(ctx, session, id, "x", revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSegment(ctx, session, model.NewID(), "y", revision); !errors.Is(err, ErrLimit) {
		t.Fatal("blank segments bypassed raw budget", err)
	}
	if _, err := st.EditSegment(ctx, session, whiteID, white+" ", revision, false); !errors.Is(err, ErrLimit) {
		t.Fatal("blank edit bypassed raw budget", err)
	}
	text, observed, err := st.DraftText(ctx, session)
	if err != nil || text != "x" || observed != revision {
		t.Fatal(text, observed, err)
	}
	if _, err := st.Send(ctx, session, model.NewID(), "model", "scope", revision); err != nil {
		t.Fatal(err)
	}
}

func TestAppendWhitespaceRawBudgetRemainsBounded(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	revision, err := st.AddSegment(ctx, session, model.NewID(), strings.Repeat("\u2003", model.MaxDraftBytes/3), 1)
	if err != nil {
		t.Fatal(err)
	}
	id := model.NewID()
	revision, err = st.AddSegment(ctx, session, id, "", revision)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := st.BeginCaptureMode(ctx, session, id, revision, CaptureAppend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTranscript(ctx, session, id, generation, "x", "capturing"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTranscript(ctx, session, id, generation, "xy", "capturing"); !errors.Is(err, ErrLimit) {
		t.Fatal("transcript bypassed raw budget", err)
	}
	segment, err := st.Segment(ctx, session, id)
	if err != nil || segment.Text != "x" {
		t.Fatal(segment, err)
	}
}
