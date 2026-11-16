package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	// A session is created by its first message. Failing that message leaves a
	// still-creating session that create, delete and rename can race over.
	id, first := model.NewID(), model.NewID()
	if _, _, err := st.Send(context.Background(), id, first, "m", "scope", "", "lifecycle", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageState(context.Background(), first, "failed", "", "test"); err != nil {
		t.Fatal(err)
	}
	session, err := st.Session(context.Background(), id)
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
func TestTranscriptionHasSetupAndFinalizationBudget(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		setup, audio, finish time.Duration
	}{
		{"maximum_audio_plus_setup", 30 * time.Millisecond, 120 * time.Millisecond, 0},
		{"below_maximum_audio_plus_finalization", 0, 100 * time.Millisecond, 40 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := lifecycleFixture(t, nil)
			svc.Config.CaptureTimeout = 120 * time.Millisecond
			provider := &timedCaptureASR{started: make(chan struct{}), setup: tc.setup, finish: tc.finish}
			svc.ASR = provider
			input, producer := io.Pipe()
			defer input.Close()
			defer producer.Close()
			type result struct {
				text string
				err  error
			}
			done := make(chan result, 1)
			go func() {
				text, err := svc.Transcribe(context.Background(), model.NewID(), input, 16000)
				done <- result{text, err}
			}()
			<-provider.started
			go func() {
				time.Sleep(tc.setup)
				_, _ = producer.Write(make([]byte, 3200))
				time.Sleep(tc.audio)
				_ = producer.Close()
			}()
			got := <-done
			if got.err != nil || got.text != "complete transcript" {
				t.Fatalf("supported audio duration %s failed with request budget %s: %q %v", tc.audio, svc.Config.CaptureTimeout, got.text, got.err)
			}
		})
	}
}

// scriptedASR emits a live partial, waits for release, then emits final.
type scriptedASR struct {
	partial, final string
	release        chan struct{}
}

func (*scriptedASR) Enabled() bool { return true }
func (a *scriptedASR) Transcribe(ctx context.Context, in io.ReadCloser, _ int, emit func(asr.Update) error) error {
	if err := emit(asr.Update{Text: a.partial}); err != nil {
		return err
	}
	select {
	case <-a.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	_, _ = io.Copy(io.Discard, in)
	return emit(asr.Update{Text: a.final, Final: true})
}

func TestTranscriptionLiveTextIsMemoryOnlyAndExpires(t *testing.T) {
	previous := transcriptKeep
	transcriptKeep = 30 * time.Millisecond
	defer func() { transcriptKeep = previous }()
	svc, _ := lifecycleFixture(t, nil)
	svc.Config.CaptureTimeout = time.Second
	provider := &scriptedASR{partial: "正在", final: "  正在说话  ", release: make(chan struct{})}
	svc.ASR = provider
	id := model.NewID()
	done := make(chan string, 1)
	go func() {
		text, _ := svc.Transcribe(context.Background(), id, io.NopCloser(strings.NewReader("pcm")), 16000)
		done <- text
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if text, revision, ok := svc.TranscriptText(id); ok && text == "正在" && revision == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live text not readable while recording")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := svc.Transcribe(context.Background(), id, io.NopCloser(strings.NewReader("pcm")), 16000); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a transcription ID is single use: %v", err)
	}
	close(provider.release)
	if text := <-done; text != "正在说话" {
		t.Fatalf("final text=%q", text)
	}
	if text, _, ok := svc.TranscriptText(id); !ok || text != "  正在说话  " {
		t.Fatalf("final text must stay readable briefly: %q %v", text, ok)
	}
	for deadline = time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, _, ok := svc.TranscriptText(id); !ok {
			return
		}
	}
	t.Fatal("finished transcript was not released")
}

func TestTranscriptionWithoutSpeechReportsNoSpeech(t *testing.T) {
	svc, _ := lifecycleFixture(t, nil)
	svc.Config.CaptureTimeout = time.Second
	release := make(chan struct{})
	close(release)
	svc.ASR = &scriptedASR{final: " ", release: release}
	if _, err := svc.Transcribe(context.Background(), model.NewID(), io.NopCloser(strings.NewReader("pcm")), 16000); !errors.Is(err, ErrNoSpeech) {
		t.Fatalf("blank transcript=%v", err)
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
	id := model.NewID()
	if _, _, err := service.Store.Send(ctx, session.ID, id, "hermes-agent", "retired-scope", "", "旧身份消息", false); err != nil {
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

type preparedStub struct{ closed atomic.Int32 }

func (p *preparedStub) Close() error { p.closed.Add(1); return nil }

type preparingASR struct {
	sessions []*preparedStub
}

func (*preparingASR) Enabled() bool { return true }
func (*preparingASR) Transcribe(context.Context, io.ReadCloser, int, func(asr.Update) error) error {
	return nil
}
func (a *preparingASR) Prepare(context.Context) (asr.Prepared, error) {
	session := &preparedStub{}
	a.sessions = append(a.sessions, session)
	return session, nil
}
func (*preparingASR) TranscribePrepared(context.Context, asr.Prepared, io.ReadCloser, int, func(asr.Update) error) error {
	return nil
}

func TestPreparedCaptureIsTakenOnceAndExpires(t *testing.T) {
	service, _ := lifecycleFixture(t, lifecycleTransport(func(*http.Request) (*http.Response, error) {
		return lifecycleResponse(500, `{}`), nil
	}))
	fake := &preparingASR{}
	service.ASR = fake
	recording := model.NewID()
	service.PrepareCapture(recording, time.Second)
	if service.takePrepared(recording) != fake.sessions[0] || service.takePrepared(recording) != nil {
		t.Fatal("a prepared session must be handed to exactly one upload")
	}
	previous := preparedTTL
	preparedTTL = 20 * time.Millisecond
	defer func() { preparedTTL = previous }()
	service.PrepareCapture(recording, time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for fake.sessions[1].closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fake.sessions[1].closed.Load() != 1 || service.takePrepared(recording) != nil {
		t.Fatal("an unused prepared session must expire and close")
	}
	preparedTTL = time.Minute
	service.PrepareCapture(model.NewID(), time.Second)
	service.Close()
	if fake.sessions[2].closed.Load() != 1 {
		t.Fatal("closing the service must close prepared sessions")
	}
}

// A retry repeats the stored request even after the configured prompt changes,
// so the idempotency key never stands for two different requests.
func TestRetryRepeatsTheStoredSystemPrompt(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var prompts, keys []string
	transport := lifecycleTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		prompt := ""
		if len(body.Messages) > 0 && body.Messages[0].Role == "system" {
			prompt = body.Messages[0].Content
		}
		prompts, keys = append(prompts, prompt), append(keys, r.Header.Get("Idempotency-Key"))
		if len(prompts) == 1 {
			return nil, errors.New("connection lost")
		}
		response := lifecycleResponse(200, `{"choices":[{"message":{"content":"完整回复"},"finish_reason":"stop"}]}`)
		response.Header.Set("Content-Type", "application/json")
		return response, nil
	})
	service, session := lifecycleFixture(t, transport)
	service.Config.ChatTimeout = time.Minute
	if err := service.Store.SetSessionReady(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	id := model.NewID()
	if _, _, err := service.Store.Send(ctx, session.ID, id, "hermes-agent", service.Hermes.Scope(), "旧提示", "问题", false); err != nil {
		t.Fatal(err)
	}
	claimed, err := service.Store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	service.complete(ctx, claimed)
	if lost, err := service.Store.Message(ctx, session.ID, id); err != nil || lost.State != "unconfirmed" {
		t.Fatalf("lost connection: %+v %v", lost, err)
	}
	service.Config.HermesPrompt = "新提示" // Changed and restarted while the message was unconfirmed.
	if err := service.Store.Retry(ctx, session.ID, id, service.Hermes.Scope()); err != nil {
		t.Fatal(err)
	}
	if claimed, err = service.Store.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	service.complete(ctx, claimed)
	done, err := service.Store.Message(ctx, session.ID, id)
	if err != nil || done.State != "completed" || done.Reply != "完整回复" {
		t.Fatalf("recovered: %+v %v", done, err)
	}
	if len(prompts) != 2 || prompts[0] != "旧提示" || prompts[1] != "旧提示" || keys[0] != keys[1] {
		t.Fatalf("prompts %q keys %q", prompts, keys)
	}
}

// Finished transcriptions are kept for a while, but never more than
// transcriptsKept at once: the oldest finished one gives way.
func TestTranscriptionTableIsBounded(t *testing.T) {
	svc, _ := lifecycleFixture(t, nil)
	svc.Config.CaptureTimeout = time.Second
	release := make(chan struct{})
	close(release)
	svc.ASR = &scriptedASR{final: "一句话", release: release}
	ids := make([]string, transcriptsKept+10)
	for i := range ids {
		ids[i] = model.NewID()
		if text, err := svc.Transcribe(context.Background(), ids[i], io.NopCloser(strings.NewReader("pcm")), 16000); err != nil || text != "一句话" {
			t.Fatalf("transcription %d: %q %v", i, text, err)
		}
	}
	svc.mu.Lock()
	kept := len(svc.transcripts)
	svc.mu.Unlock()
	if kept != transcriptsKept {
		t.Fatalf("kept %d transcriptions", kept)
	}
	if _, _, ok := svc.TranscriptText(ids[0]); ok {
		t.Fatal("the oldest finished transcription was kept")
	}
	if text, _, ok := svc.TranscriptText(ids[len(ids)-1]); !ok || text != "一句话" {
		t.Fatalf("the newest transcription was evicted: %q %v", text, ok)
	}
}

func TestPendingSessionReconcileDoesNotStarveBeyondPageLimit(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// First 16 sessions always fail upstream; sessions 17..19 succeed (17 creating, 18 creating, 19 deleting).
	failing := make(map[string]bool)
	var mu sync.Mutex
	callsPerRound := make([]int, 0, 4)
	var roundCalls atomic.Int32
	transport := lifecycleTransport(func(r *http.Request) (*http.Response, error) {
		roundCalls.Add(1)
		id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		mu.Lock()
		bad := failing[id]
		mu.Unlock()
		if bad {
			return lifecycleResponse(503, `{}`), nil
		}
		if r.Method == "GET" {
			return lifecycleResponse(200, `{"session":{"id":"`+id+`","source":"api_server"}}`), nil
		}
		return lifecycleResponse(200, `{}`), nil
	})
	service := New(st, config.Config{}, &hermes.Client{BaseURL: "http://lifecycle.invalid", APIKey: "test", HTTP: &http.Client{Transport: transport}},
		nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ids := make([]string, 19)
	for i := range ids {
		ids[i] = model.NewID()
		if _, _, err := st.Send(ctx, ids[i], model.NewID(), "m", "scope", "", "pending", true); err != nil {
			t.Fatal(err)
		}
	}
	// Put the 16 failing sessions first in (created_at, id) order by sorting the created list or inspecting page 1.
	firstPage, err := st.PendingSessions(ctx, nil, 16)
	if err != nil || len(firstPage) != 16 {
		t.Fatalf("first page=%d err=%v", len(firstPage), err)
	}
	firstSet := make(map[string]bool)
	for _, s := range firstPage {
		firstSet[s.ID] = true
		failing[s.ID] = true
	}
	var tail []string
	for _, id := range ids {
		if !firstSet[id] {
			tail = append(tail, id)
		}
	}
	// Mark one tail session as deleting to test both creating and deleting tail recovery.
	if err := st.BeginDelete(ctx, tail[2]); err != nil {
		t.Fatal(err)
	}

	// Round 1: processes at most 16 entries (the failing ones), none of the tail yet.
	roundCalls.Store(0)
	service.reconcilePending(ctx)
	callsPerRound = append(callsPerRound, int(roundCalls.Load()))
	for _, id := range tail {
		if got, err := st.Session(ctx, id); err != nil || got.State == "ready" {
			t.Fatalf("tail session %s settled prematurely in round 1: %+v %v", id, got, err)
		}
	}

	// Round 2: cursor advances past the 16 failing entries and settles all 3 tail sessions.
	roundCalls.Store(0)
	service.reconcilePending(ctx)
	callsPerRound = append(callsPerRound, int(roundCalls.Load()))
	if callsPerRound[0] > 16 || callsPerRound[1] > 16 {
		t.Fatalf("per-round reconcile calls exceeded batch limit: %v", callsPerRound)
	}
	for _, id := range tail[:2] {
		got, err := st.Session(ctx, id)
		if err != nil || got.State != "ready" {
			t.Fatalf("tail creating session %s not recovered: %+v %v", id, got, err)
		}
	}
	if _, err := st.Session(ctx, tail[2]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("tail deleting session %s not finished: err=%v", tail[2], err)
	}
}
