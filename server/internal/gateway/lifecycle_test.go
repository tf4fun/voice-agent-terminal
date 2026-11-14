package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/asr"
	"github.com/tf4fun/voice-agent-terminal/server/internal/config"
	"github.com/tf4fun/voice-agent-terminal/server/internal/hermes"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

type lifecycleTransport func(*http.Request) (*http.Response, error)

func (f lifecycleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func lifecycleResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func lifecycleFixture(t *testing.T, transport http.RoundTripper) (*Service, model.Session) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	session, err := st.BeginSession(context.Background(), model.NewID(), "lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	client := &hermes.Client{BaseURL: "http://lifecycle.invalid", APIKey: "test", HTTP: &http.Client{Transport: transport}}
	service := New(st, config.Config{}, client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return service, session
}
func requireLifecycleReferences(t *testing.T, s *Service, id string, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		s.lifecycleMu.Lock()
		entry := s.lifecycles[id]
		got := 0
		if entry != nil {
			got = entry.references
		}
		s.lifecycleMu.Unlock()
		if got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("lifecycle references=%d want=%d", got, want)
		case <-ticker.C:
		}
	}
}
func requireNoLifecycleEntries(t *testing.T, s *Service) {
	t.Helper()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if len(s.lifecycles) != 0 {
		t.Fatalf("retained lifecycle keys=%d", len(s.lifecycles))
	}
}

func TestReconcileSerializesCreateDeleteAndIgnoresStaleCreatingSnapshot(t *testing.T) {
	ctx := context.Background()
	entered, resume := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	exists := false
	posts := 0
	transport := lifecycleTransport(func(r *http.Request) (*http.Response, error) {
		id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		switch r.Method {
		case "POST":
			mu.Lock()
			posts++
			mu.Unlock()
			close(entered)
			select {
			case <-resume:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			mu.Lock()
			exists = true
			mu.Unlock()
			return lifecycleResponse(201, `{}`), nil
		case "GET":
			mu.Lock()
			present := exists
			mu.Unlock()
			if !present {
				return lifecycleResponse(404, `{}`), nil
			}
			return lifecycleResponse(200, `{"session":{"id":"`+id+`","source":"api_server"}}`), nil
		case "DELETE":
			mu.Lock()
			exists = false
			mu.Unlock()
			return lifecycleResponse(200, `{}`), nil
		default:
			t.Errorf("unexpected method %s", r.Method)
			return lifecycleResponse(500, `{}`), nil
		}
	})
	service, snapshot := lifecycleFixture(t, transport)
	created := make(chan error, 1)
	go func() { created <- service.Reconcile(ctx, snapshot) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("create did not start")
	}
	if err := service.Store.BeginDelete(ctx, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	deleting, err := service.Store.Session(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- service.Reconcile(ctx, deleting) }()
	requireLifecycleReferences(t, service, snapshot.ID, 2)
	close(resume)
	if err := <-created; err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.Session(ctx, snapshot.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("session not deleted", err)
	}
	// A background worker may still hold either old snapshot after deletion.
	if err := service.Reconcile(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(ctx, deleting); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	present, postCount := exists, posts
	mu.Unlock()
	if present || postCount != 1 {
		t.Fatalf("orphan=%v create calls=%d", present, postCount)
	}
	requireNoLifecycleEntries(t, service)
}

func TestRenameUsesTheSameSessionLifecycleLock(t *testing.T) {
	ctx := context.Background()
	entered, resume := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	deletes := 0
	transport := lifecycleTransport(func(r *http.Request) (*http.Response, error) {
		id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		switch r.Method {
		case "GET":
			return lifecycleResponse(200, `{"session":{"id":"`+id+`","source":"api_server"}}`), nil
		case "PATCH":
			close(entered)
			select {
			case <-resume:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return lifecycleResponse(200, `{}`), nil
		case "DELETE":
			mu.Lock()
			deletes++
			mu.Unlock()
			return lifecycleResponse(200, `{}`), nil
		default:
			return lifecycleResponse(500, `{}`), nil
		}
	})
	service, session := lifecycleFixture(t, transport)
	if err := service.Store.SetSessionReady(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	renamed := make(chan error, 1)
	go func() { renamed <- service.RenameSession(ctx, session.ID, "new title") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("rename did not start")
	}
	if err := service.Store.BeginDelete(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	deleting, err := service.Store.Session(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- service.Reconcile(ctx, deleting) }()
	requireLifecycleReferences(t, service, session.ID, 2)
	mu.Lock()
	earlyDeletes := deletes
	mu.Unlock()
	if earlyDeletes != 0 {
		t.Fatal("delete interleaved with rename")
	}
	close(resume)
	if err := <-renamed; !errors.Is(err, store.ErrConflict) {
		t.Fatalf("delete should supersede local rename=%v", err)
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	deleteCount := deletes
	mu.Unlock()
	if deleteCount != 1 {
		t.Fatal(deleteCount)
	}
	requireNoLifecycleEntries(t, service)
}

func TestLifecycleLocksArePerSessionAndReleaseCancelledWaiters(t *testing.T) {
	service, _ := lifecycleFixture(t, lifecycleTransport(func(*http.Request) (*http.Response, error) {
		t.Error("unexpected network call")
		return nil, errors.New("unexpected network call")
	}))
	owner, err := service.lockSession(context.Background(), "held")
	if err != nil {
		t.Fatal(err)
	}
	other := make(chan error, 1)
	go func() {
		unlock, err := service.lockSession(context.Background(), "other")
		if err == nil {
			unlock()
		}
		other <- err
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated session blocked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() {
		unlock, err := service.lockSession(ctx, "held")
		if err == nil {
			unlock()
		}
		waiting <- err
	}()
	requireLifecycleReferences(t, service, "held", 2)
	cancel()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter blocked")
	}
	requireLifecycleReferences(t, service, "held", 1)
	owner()
	requireNoLifecycleEntries(t, service)
	for i := 0; i < 64; i++ {
		unlock, err := service.lockSession(context.Background(), model.NewID())
		if err != nil {
			t.Fatal(err)
		}
		unlock()
	}
	requireNoLifecycleEntries(t, service)
}

type timedCaptureASR struct {
	started       chan struct{}
	setup, finish time.Duration
}

func (*timedCaptureASR) Enabled() bool { return true }
func (a *timedCaptureASR) Transcribe(ctx context.Context, in io.ReadCloser, rate int, emit func(asr.Update) error) error {
	close(a.started)
	select {
	case <-time.After(a.setup):
	case <-ctx.Done():
		return ctx.Err()
	}
	if _, err := io.Copy(io.Discard, in); err != nil {
		return err
	}
	select {
	case <-time.After(a.finish):
	case <-ctx.Done():
		return ctx.Err()
	}
	return emit(asr.Update{Text: "complete transcript", Final: true})
}
func TestCaptureHasSetupAndFinalizationBudget(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		setup, audio, finish time.Duration
	}{
		{"maximum_audio_plus_setup", 30 * time.Millisecond, 120 * time.Millisecond, 0},
		{"below_maximum_audio_plus_finalization", 0, 100 * time.Millisecond, 40 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, session := lifecycleFixture(t, nil)
			id := model.NewID()
			rev, err := svc.Store.AddSegment(context.Background(), session.ID, id, "", session.Revision)
			if err != nil {
				t.Fatal(err)
			}
			svc.Config.CaptureTimeout = 120 * time.Millisecond
			provider := &timedCaptureASR{started: make(chan struct{}), setup: tc.setup, finish: tc.finish}
			svc.ASR = provider
			input, producer := io.Pipe()
			defer input.Close()
			defer producer.Close()
			done := make(chan error, 1)
			go func() { done <- svc.Capture(context.Background(), session.ID, id, rev, input, 16000, true, true) }()
			<-provider.started
			go func() {
				time.Sleep(tc.setup)
				_, _ = producer.Write(make([]byte, 3200))
				time.Sleep(tc.audio)
				_ = producer.Close()
			}()
			err = <-done
			if err != nil {
				t.Fatalf("supported audio duration %s failed with request budget %s: %v", tc.audio, svc.Config.CaptureTimeout, err)
			}
		})
	}
}

type blockingCaptureASR struct {
	entered chan struct{}
	ctx     context.Context
}

func (*blockingCaptureASR) Enabled() bool { return true }
func (a *blockingCaptureASR) Transcribe(ctx context.Context, _ io.ReadCloser, _ int, _ func(asr.Update) error) error {
	a.ctx = ctx
	close(a.entered)
	<-ctx.Done()
	return ctx.Err()
}

func TestDelayedEditCancellationPreservesNewCapture(t *testing.T) {
	ctx := context.Background()
	service, session := lifecycleFixture(t, nil)
	id := model.NewID()
	revision, err := service.Store.AddSegment(ctx, session.ID, id, "", session.Revision)
	if err != nil {
		t.Fatal(err)
	}
	revision, retired, err := service.Store.EditSegmentWithGeneration(ctx, session.ID, id, "", revision, false)
	if err != nil {
		t.Fatal(err)
	}
	provider := &blockingCaptureASR{entered: make(chan struct{})}
	service.ASR = provider
	captureCtx, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan error, 1)
	go func() {
		finished <- service.Capture(captureCtx, session.ID, id, revision, io.NopCloser(strings.NewReader("pcm")), 16000, false, false)
	}()
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("capture did not enter recognition")
	}
	service.CancelCaptureGeneration(id, retired)
	if provider.ctx.Err() != nil {
		t.Fatalf("old edit cancelled new capture: %v", provider.ctx.Err())
	}
	service.mu.Lock()
	generation := service.captures[id].generation
	service.mu.Unlock()
	if generation <= retired {
		t.Fatalf("generation did not advance: %d %d", generation, retired)
	}
	service.CancelCaptureGeneration(id, generation)
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("matching cancellation did not stop capture")
	}
}

func TestCaptureRegistrationRejectsRetiredGeneration(t *testing.T) {
	service, _ := lifecycleFixture(t, nil)
	id := model.NewID()
	current, currentCancel := context.WithCancel(context.Background())
	defer currentCancel()
	if !service.registerCapture(id, 4, currentCancel) {
		t.Fatal("current registration failed")
	}
	stale, staleCancel := context.WithCancel(context.Background())
	defer staleCancel()
	if service.registerCapture(id, 2, staleCancel) {
		t.Fatal("older registration accepted")
	}
	if service.registerCapture(id, 4, staleCancel) {
		t.Fatal("duplicate registration accepted")
	}
	if current.Err() != nil || stale.Err() != nil || service.captures[id].generation != 4 {
		t.Fatal("stale registration changed active capture")
	}
	newest, newestCancel := context.WithCancel(context.Background())
	defer newestCancel()
	if !service.registerCapture(id, 6, newestCancel) || current.Err() != context.Canceled || newest.Err() != nil {
		t.Fatal("newer registration did not replace older capture")
	}
}

func TestRetryRescopesMessageAfterHermesIdentityChange(t *testing.T) {
	ctx := context.Background()
	transport := lifecycleTransport(func(r *http.Request) (*http.Response, error) {
		return lifecycleResponse(500, `{}`), nil
	})
	service, session := lifecycleFixture(t, transport)
	if err := service.Store.SetSessionReady(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	revision, err := service.Store.AddSegment(ctx, session.ID, model.NewID(), "旧身份消息", 1)
	if err != nil {
		t.Fatal(err)
	}
	id := model.NewID()
	if _, err := service.Store.Send(ctx, session.ID, id, "hermes-agent", "retired-scope", revision); err != nil {
		t.Fatal(err)
	}
	claimed, err := service.Store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	service.complete(ctx, claimed)
	stranded, err := service.Store.Message(ctx, session.ID, id)
	if err != nil || stranded.State != "unconfirmed" || stranded.Error != "hermes_identity_changed" {
		t.Fatalf("identity change: %+v %v", stranded, err)
	}
	if err := service.Store.Retry(ctx, session.ID, id, service.Hermes.Scope()); err != nil {
		t.Fatal(err)
	}
	requeued, err := service.Store.Claim(ctx)
	if err != nil || requeued.ID != id || requeued.UpstreamScope != service.Hermes.Scope() {
		t.Fatalf("retry kept the retired scope: %+v %v", requeued, err)
	}
}
