package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

// fixture opens a store with one ready session that has no messages yet.
func fixture(t *testing.T) (*Store, string) {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id := model.NewID()
	now := time.Now().UnixMilli()
	if _, err := st.db.Exec("INSERT INTO sessions(id,title,state,created_at,updated_at) VALUES(?,?,'ready',?,?)", id, "测试会话", now, now); err != nil {
		t.Fatal(err)
	}
	return st, id
}

func TestFirstSendCreatesTitledSessionWithItsMessage(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session, id := model.NewID(), model.NewID()
	if _, _, err := st.Send(ctx, session, id, "m", "scope", "", "你好", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("send to an unknown session without create=%v", err)
	}
	message, created, err := st.Send(ctx, session, id, "m", "scope", "", "\n  推荐一部好电影，理由在三十个字以内。\n第二行", true)
	if err != nil || !created || message.State != "queued" {
		t.Fatal(message, created, err)
	}
	current, err := st.Session(ctx, session)
	if err != nil || current.State != "creating" || current.Title != "推荐一部好电影，理由在三十个字以内。" {
		t.Fatalf("session=%+v %v", current, err)
	}
	// The first message waits until Hermes holds its session.
	if _, err := st.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claimed before the session was ready: %v", err)
	}
	if err := st.SetSessionReady(ctx, session); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.Claim(ctx)
	if err != nil || claimed.ID != id {
		t.Fatal(claimed, err)
	}
}

func TestSendIsIdempotentByMessageID(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	id := model.NewID()
	first, created, err := st.Send(ctx, session, id, "m", "scope", "", "同一条", true)
	if err != nil || created {
		t.Fatal(first, created, err)
	}
	again, created, err := st.Send(ctx, session, id, "m", "scope", "", "同一条", true)
	if err != nil || created || again.ID != first.ID || again.Revision != first.Revision {
		t.Fatalf("lost-response retry: %+v %v %v", again, created, err)
	}
	if _, _, err := st.Send(ctx, session, id, "m", "scope", "", "改过的文字", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused ID with other input=%v", err)
	}
	other := model.NewID()
	if _, _, err := st.Send(ctx, other, id, "m", "scope", "", "同一条", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused ID in another session=%v", err)
	}
	if _, err := st.Session(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a rejected send left a session behind: %v", err)
	}
}

func TestSendRejectsEmptyOversizedBusyAndDeleted(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	if _, _, err := st.Send(ctx, session, model.NewID(), "m", "scope", "", "", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("empty input=%v", err)
	}
	if _, _, err := st.Send(ctx, session, model.NewID(), "m", "scope", "", string(make([]byte, model.MaxInputBytes+1)), false); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized input=%v", err)
	}
	if _, _, err := st.Send(ctx, session, model.NewID(), "m", "scope", "", "第一条", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Send(ctx, session, model.NewID(), "m", "scope", "", "第二条", false); !errors.Is(err, ErrBusy) {
		t.Fatalf("second active message=%v", err)
	}
	empty, other := fixture(t)
	if err := empty.BeginDelete(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, _, err := empty.Send(ctx, other, model.NewID(), "m", "scope", "", "迟到", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("send to a deleting session=%v", err)
	}
}

func TestConcurrentSendAdmitsOneMessage(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session := model.NewID() // Concurrent first sends race to create it.
	var accepted, created atomic.Int32
	var workers sync.WaitGroup
	for index := 0; index < 12; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, made, err := st.Send(ctx, session, model.NewID(), "hermes-agent", "scope", "", "共同输入", true); err == nil {
				accepted.Add(1)
				if made {
					created.Add(1)
				}
			}
		}()
	}
	workers.Wait()
	if accepted.Load() != 1 || created.Load() != 1 {
		t.Fatalf("accepted %d sends, created %d sessions", accepted.Load(), created.Load())
	}
}

func TestRestartKeepsInputAndRequiresSameMessageRecovery(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	st, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	session := model.NewID()
	message, _, err := st.Send(ctx, session, model.NewID(), "hermes-agent", "scope", "", "保留文字", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetSessionReady(ctx, session)
	if _, err := st.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendReply(ctx, message.ID, "已有部分回复"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := st.Message(ctx, session, message.ID)
	if err != nil || recovered.State != "unconfirmed" || recovered.Input != "保留文字" {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	if err := st.Retry(ctx, session, message.ID, "scope"); err != nil {
		t.Fatal(err)
	}
	retained, err := st.Message(ctx, session, message.ID)
	if err != nil || retained.Reply != "已有部分回复" {
		t.Fatalf("partial reply lost during retry: %+v %v", retained, err)
	}
	claimed, err := st.Claim(ctx)
	if err != nil || claimed.ID != message.ID {
		t.Fatalf("retry: %+v %v", claimed, err)
	}
}
