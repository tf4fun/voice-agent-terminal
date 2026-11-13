package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func fixture(t *testing.T) (*Store, string) {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id := model.NewID()
	if _, err := st.BeginSession(context.Background(), id, "测试会话"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionReady(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	return st, id
}

func TestDraftSnapshotAndLateRecognition(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	first, second := model.NewID(), model.NewID()
	revision, err := st.AddSegment(ctx, session, first, "第一段", 1)
	if err != nil {
		t.Fatal(err)
	}
	revision, err = st.AddSegment(ctx, session, second, "", revision)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := st.BeginCapture(ctx, session, second, revision)
	if err != nil {
		t.Fatal(err)
	}
	revision, err = st.UpdateTranscript(ctx, session, second, generation, "还没说完", "capturing")
	if err != nil {
		t.Fatal(err)
	}
	stale := revision
	revision, err = st.EditSegment(ctx, session, second, "", revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTranscript(ctx, session, second, generation, "迟到结果", "ready"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late update: %v", err)
	}
	messageID := model.NewID()
	if _, err := st.Send(ctx, session, messageID, "hermes-agent", "scope", stale); !errors.Is(err, ErrStale) {
		t.Fatalf("stale send: %v", err)
	}
	message, err := st.Send(ctx, session, messageID, "hermes-agent", "scope", revision)
	if err != nil {
		t.Fatal(err)
	}
	if message.Input != "第一段" {
		t.Fatal(message.Input)
	}
	repeated, err := st.Send(ctx, session, messageID, "changed-model", "scope", revision)
	if err != nil || repeated.ID != message.ID || repeated.Model != "hermes-agent" {
		t.Fatalf("repeat changed snapshot: %+v %v", repeated, err)
	}
	if _, err := st.Send(ctx, session, messageID, "hermes-agent", "scope", revision+1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	current, parts, err := st.Draft(ctx, session, 4, 0)
	if err != nil || len(parts) != 0 {
		t.Fatalf("draft after send: %+v %v", parts, err)
	}
	if _, err := st.AddSegment(ctx, session, model.NewID(), "接下来再说", current.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentSendAdmitsOneSnapshot(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	revision, err := st.AddSegment(ctx, session, model.NewID(), "共同草稿", 1)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var workers sync.WaitGroup
	for index := 0; index < 12; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := st.Send(ctx, session, model.NewID(), "hermes-agent", "scope", revision); err == nil {
				accepted.Add(1)
			}
		}()
	}
	workers.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d sends", accepted.Load())
	}
}

func TestRestartKeepsInputAndRequiresSameMessageRecovery(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	st, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	session, segment := model.NewID(), model.NewID()
	_, _ = st.BeginSession(ctx, session, "恢复")
	_ = st.SetSessionReady(ctx, session)
	revision, err := st.AddSegment(ctx, session, segment, "保留文字", 1)
	if err != nil {
		t.Fatal(err)
	}
	message, err := st.Send(ctx, session, model.NewID(), "hermes-agent", "scope", revision)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := st.Retry(ctx, session, message.ID); err != nil {
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

func TestEditingRetiresRecognitionGenerationAndLimitsDraft(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	segment := model.NewID()
	revision, err := st.AddSegment(ctx, session, segment, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := st.BeginCapture(ctx, session, segment, revision)
	if err != nil {
		t.Fatal(err)
	}
	current, _, _ := st.Draft(ctx, session, 4, 0)
	revision, err = st.EditSegment(ctx, session, segment, "手动编辑", current.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTranscript(ctx, session, segment, generation, "迟到覆盖", "ready"); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if _, err := st.AddSegment(ctx, session, model.NewID(), strings.Repeat("a", model.MaxDraftBytes), revision); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}
