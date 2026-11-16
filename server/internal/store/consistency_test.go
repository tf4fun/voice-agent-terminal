package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func failedMessageFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	ctx := context.Background()
	st, session := fixture(t)
	id := model.NewID()
	if _, _, err := st.Send(ctx, session, id, "model", "scope", "", "reviewed input", false); err != nil {
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
	if err := st.Retry(ctx, session, id, "scope"); !errors.Is(err, ErrBusy) {
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
	if err := st.Retry(ctx, session, id, "scope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry deleted session=%v", err)
	}
}

func TestRetryBeforeDeleteKeepsSessionReady(t *testing.T) {
	ctx := context.Background()
	st, session, id := failedMessageFixture(t)
	if err := st.Retry(ctx, session, id, "scope"); err != nil {
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
			retried = st.Retry(ctx, session, id, "scope")
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

// A session Hermes never created can be deleted with its unsent first message,
// and a late create success cannot revive it.
func TestCreatingSessionDeletesWithItsQueuedMessage(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session, id := model.NewID(), model.NewID()
	if _, _, err := st.Send(ctx, session, id, "m", "scope", "", "首条", true); err != nil {
		t.Fatal(err)
	}
	if err := st.Retry(ctx, session, id, "scope"); !errors.Is(err, ErrBusy) {
		t.Fatalf("retry while creating=%v", err)
	}
	if err := st.BeginDelete(ctx, session); err != nil {
		t.Fatalf("delete while creating=%v", err)
	}
	if err := st.SetSessionReady(ctx, session); err != nil {
		t.Fatal(err)
	}
	if current, err := st.Session(ctx, session); err != nil || current.State != "deleting" {
		t.Fatalf("late create: %+v %v", current, err)
	}
	if _, err := st.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claimed a deleting session's message: %v", err)
	}
	if err := st.FinishDelete(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Message(ctx, session, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message after delete: %v", err)
	}
}
