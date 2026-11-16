package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/asr"
	"github.com/tf4fun/voice-agent-terminal/server/internal/config"
	"github.com/tf4fun/voice-agent-terminal/server/internal/events"
	"github.com/tf4fun/voice-agent-terminal/server/internal/hermes"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
	"github.com/tf4fun/voice-agent-terminal/server/internal/tts"
)

// preparedTTL bounds how long an unused upstream session stays open.
var preparedTTL = 30 * time.Second

const pendingBatchLimit = 16

type preparedCapture struct {
	session asr.Prepared
	timer   *time.Timer
}

type Service struct {
	Store         *store.Store
	Config        config.Config
	Hermes        *hermes.Client
	ASR           asr.Transcriber
	TTS           tts.Synthesizer // Optional; nil disables spoken replies.
	Events        *events.Hub
	Log           *slog.Logger
	mu            sync.Mutex
	lifecycleMu   sync.Mutex
	lifecycles    map[string]*sessionLifecycleLock
	pendingMu     sync.Mutex
	pendingCursor *store.PendingCursor
	transcripts   map[string]*transcription
	prepared      map[string]*preparedCapture
	captureSlots  chan struct{}
	wake          chan struct{}
	workers       sync.WaitGroup
	cancel        context.CancelFunc
	ctx           context.Context
	speech        speechStreams
}

func New(st *store.Store, cfg config.Config, client *hermes.Client, transcriber asr.Transcriber, logger *slog.Logger) *Service {
	return &Service{
		Store: st, Config: cfg, Hermes: client, ASR: transcriber,
		Events: events.NewHub(), Log: logger,
		lifecycles:   make(map[string]*sessionLifecycleLock),
		transcripts:  make(map[string]*transcription),
		prepared:     make(map[string]*preparedCapture),
		captureSlots: make(chan struct{}, 4),
		wake:         make(chan struct{}, 1),
	}
}

func (s *Service) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	// Keep a configured speech session warm for the next spoken reply.
	if warm, ok := s.TTS.(interface{ Run(context.Context) }); ok && s.TTS.Enabled() {
		s.workers.Add(1)
		go func() { defer s.workers.Done(); warm.Run(ctx) }()
	}
	// Keep a configured transcription session warm for the next recording.
	if warm, ok := s.ASR.(interface{ Run(context.Context) }); ok && s.ASR.Enabled() {
		s.workers.Add(1)
		go func() { defer s.workers.Done(); warm.Run(ctx) }()
	}
	for index := 0; index < s.Config.Workers; index++ {
		s.workers.Add(1)
		go func() { defer s.workers.Done(); s.runMessages(ctx) }()
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			if s.Hermes.Enabled() {
				s.reconcilePending(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// reconcilePending processes at most pendingBatchLimit pending sessions per
// round and advances a cursor so persistent upstream errors never starve later entries.
func (s *Service) reconcilePending(ctx context.Context) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	pending, err := s.Store.PendingSessions(ctx, s.pendingCursor, pendingBatchLimit)
	if err != nil {
		return
	}
	if len(pending) == 0 && s.pendingCursor != nil {
		s.pendingCursor = nil
		pending, err = s.Store.PendingSessions(ctx, nil, pendingBatchLimit)
		if err != nil {
			return
		}
	}
	for _, session := range pending {
		if ctx.Err() != nil {
			return
		}
		_ = s.Reconcile(ctx, session)
	}
	if len(pending) < pendingBatchLimit {
		s.pendingCursor = nil
		return
	}
	last := pending[len(pending)-1]
	s.pendingCursor = &store.PendingCursor{CreatedAt: last.CreatedAt, ID: last.ID}
}

func (s *Service) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	for _, active := range s.transcripts {
		active.stop()
	}
	for id, entry := range s.prepared {
		entry.timer.Stop()
		_ = entry.session.Close()
		delete(s.prepared, id)
	}
	s.mu.Unlock()
	s.workers.Wait()
}

func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Reconcile(ctx context.Context, session model.Session) error {
	unlock, err := s.lockSession(ctx, session.ID)
	if err != nil {
		return err
	}
	defer unlock()
	// Pending-session snapshots can outlive another create or delete operation.
	session, err = s.Store.Session(ctx, session.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch session.State {
	case "creating":
		err = s.Hermes.CreateSession(ctx, session.ID, session.Title)
		if err == nil {
			err = s.Store.SetSessionReady(ctx, session.ID)
		}
		if err == nil {
			s.Wake() // The session's first message waits for it.
		}
	case "deleting":
		err = s.Hermes.DeleteSession(ctx, session.ID)
		if err == nil {
			err = s.Store.FinishDelete(ctx, session.ID)
		}
	default:
		return nil
	}
	if err != nil {
		_ = s.Store.SessionError(ctx, session.ID, "hermes_session_unavailable")
	}
	s.Events.Publish(events.Event{Type: "session.updated", SessionID: session.ID})
	return err
}

// PrepareCapture opens the upstream transcription session for a recording
// while the device shows its preparing animation. It waits at most
// wait; a slower or failed handshake leaves the upload to open its own session.
func (s *Service) PrepareCapture(id string, wait time.Duration) {
	preparer, ok := s.ASR.(asr.Preparer)
	if !ok || s.ASR == nil || !s.ASR.Enabled() {
		return
	}
	s.mu.Lock()
	full := len(s.prepared) >= cap(s.captureSlots)
	s.mu.Unlock()
	if full {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	started := time.Now()
	session, err := preparer.Prepare(ctx)
	if err != nil {
		s.Log.Warn("transcription session not prepared", "transcription_id", id,
			"elapsed_ms", time.Since(started).Milliseconds(), "error", err.Error())
		return
	}
	s.Log.Info("transcription session prepared", "transcription_id", id, "elapsed_ms", time.Since(started).Milliseconds())
	entry := &preparedCapture{session: session}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.prepared[id]; old != nil && old.timer.Stop() {
		_ = old.session.Close()
	}
	entry.timer = time.AfterFunc(preparedTTL, func() {
		s.mu.Lock()
		if s.prepared[id] == entry {
			delete(s.prepared, id)
		}
		s.mu.Unlock()
		_ = session.Close()
	})
	s.prepared[id] = entry
}

// takePrepared hands a prepared session to exactly one upload.
func (s *Service) takePrepared(id string) asr.Prepared {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.prepared[id]
	if entry == nil {
		return nil
	}
	delete(s.prepared, id)
	if !entry.timer.Stop() {
		return nil // Expiry is already closing it.
	}
	return entry.session
}

func (s *Service) RenameSession(ctx context.Context, id, title string) error {
	unlock, err := s.lockSession(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	session, err := s.Store.Session(ctx, id)
	if err != nil {
		return err
	}
	if session.State != "ready" {
		return store.ErrConflict
	}
	if !s.Hermes.Enabled() {
		return &hermes.UpstreamError{Code: "hermes_not_configured"}
	}
	if err := s.Hermes.RenameSession(ctx, id, title); err != nil {
		return err
	}
	return s.Store.Rename(ctx, id, title)
}

func (s *Service) runMessages(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		message, err := s.Store.Claim(ctx)
		if err == nil {
			s.complete(ctx, message)
			continue
		}
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			s.Log.Error("claiming a queued message failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

func (s *Service) complete(parent context.Context, message model.Message) {
	if message.UpstreamScope != s.Hermes.Scope() {
		_ = s.Store.SetMessageState(parent, message.ID, "unconfirmed", "", "hermes_identity_changed")
		s.Events.Publish(events.Event{Type: "message.updated", SessionID: message.SessionID, MessageID: message.ID, Status: "unconfirmed"})
		return
	}
	ctx, cancel := context.WithTimeout(parent, s.Config.ChatTimeout)
	defer cancel()
	if err := s.Store.SetMessageState(ctx, message.ID, "waiting", "", ""); err != nil {
		return
	}
	s.Events.Publish(events.Event{Type: "message.updated", SessionID: message.SessionID, MessageID: message.ID, Status: "waiting"})
	started := time.Now()
	firstText := true
	replyBytes := 0
	deltaCount := 0
	var storeTime, maxStoreTime time.Duration
	firstPublished := true
	s.Log.Info("chat upstream started", "message_id", message.ID)
	finish, err := s.Hermes.Chat(ctx, message, func(update hermes.Update) error {
		if update.Attention {
			if err := s.Store.SetMessageState(ctx, message.ID, "attention", "", "approval_required"); err != nil {
				return err
			}
			s.Events.Publish(events.Event{Type: "message.updated", SessionID: message.SessionID, MessageID: message.ID, Status: "attention"})
		}
		if update.Reset {
			if err := s.Store.ReplaceReply(ctx, message.ID, update.Text); err != nil {
				return err
			}
			s.Events.Publish(events.Event{Type: "chat.reset", SessionID: message.SessionID, MessageID: message.ID})
			return nil
		}
		if update.Text == "" {
			return nil
		}
		if firstText {
			s.Log.Info("chat first text", "message_id", message.ID, "elapsed_ms", time.Since(started).Milliseconds())
			firstText = false
		}
		replyBytes += len(update.Text)
		storeStarted := time.Now()
		offset, err := s.Store.AppendReply(ctx, message.ID, update.Text)
		elapsed := time.Since(storeStarted)
		storeTime += elapsed
		if elapsed > maxStoreTime {
			maxStoreTime = elapsed
		}
		if err != nil {
			return err
		}
		deltaCount++
		for text := update.Text; text != ""; {
			chunk := model.Prefix(text, 64)
			s.Events.Publish(events.Event{Type: "chat.delta", SessionID: message.SessionID, MessageID: message.ID, Text: chunk, Offset: offset})
			offset += len(chunk)
			text = text[len(chunk):]
		}
		if firstPublished {
			s.Log.Info("chat first published", "message_id", message.ID, "elapsed_ms", time.Since(started).Milliseconds())
			firstPublished = false
		}
		return nil
	})
	s.Log.Info("chat upstream finished", "message_id", message.ID,
		"elapsed_ms", time.Since(started).Milliseconds(), "reply_bytes", replyBytes,
		"delta_count", deltaCount, "store_ms", storeTime.Milliseconds(), "max_store_ms", maxStoreTime.Milliseconds())
	state, code := "completed", ""
	if err != nil {
		state, code = "unconfirmed", "hermes_connection_lost"
		var upstream *hermes.UpstreamError
		if errors.As(err, &upstream) {
			code = upstream.Code
			if !upstream.Uncertain {
				state = "failed"
			}
		}
		if errors.Is(err, store.ErrLimit) {
			state, code = "incomplete", "reply_limit_exceeded"
		}
	} else {
		switch finish {
		case "stop":
		case "length":
			state = "incomplete"
		case "cancelled":
			state = "cancelled"
		case "error":
			state, code = "failed", "hermes_execution_failed"
		default:
			state, code = "incomplete", "hermes_unknown_finish"
		}
	}
	cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	if err := s.Store.SetMessageState(cleanup, message.ID, state, finish, code); err != nil {
		s.Log.Error("persisting a message outcome failed")
		return
	}
	s.Events.Publish(events.Event{Type: "message.updated", SessionID: message.SessionID, MessageID: message.ID, Status: state})
}

// baseContext is the service lifetime; background work started by requests
// (speech synthesis) outlives the request but not the service.
func (s *Service) baseContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}
