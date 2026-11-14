package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/asr"
	"github.com/tf4fun/voice-agent-terminal/server/internal/config"
	"github.com/tf4fun/voice-agent-terminal/server/internal/events"
	"github.com/tf4fun/voice-agent-terminal/server/internal/hermes"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

type capture struct {
	generation int64
	cancel     context.CancelFunc
}

type Service struct {
	Store        *store.Store
	Config       config.Config
	Hermes       *hermes.Client
	ASR          asr.Transcriber
	Events       *events.Hub
	Log          *slog.Logger
	mu           sync.Mutex
	lifecycleMu  sync.Mutex
	lifecycles   map[string]*sessionLifecycleLock
	captures     map[string]capture
	captureSlots chan struct{}
	wake         chan struct{}
	workers      sync.WaitGroup
	cancel       context.CancelFunc
}

func New(st *store.Store, cfg config.Config, client *hermes.Client, transcriber asr.Transcriber, logger *slog.Logger) *Service {
	return &Service{
		Store: st, Config: cfg, Hermes: client, ASR: transcriber,
		Events: events.NewHub(), Log: logger,
		lifecycles:   make(map[string]*sessionLifecycleLock),
		captures:     make(map[string]capture),
		captureSlots: make(chan struct{}, 4),
		wake:         make(chan struct{}, 1),
	}
}

func (s *Service) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
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
				pending, err := s.Store.PendingSessions(ctx)
				if err == nil {
					for _, session := range pending {
						_ = s.Reconcile(ctx, session)
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			if expired, err := s.Store.ExpireStaged(ctx, time.Now()); err == nil {
				for _, session := range expired {
					s.Events.Publish(events.Event{Type: "draft.updated", SessionID: session.ID, Revision: session.Revision, Status: "cancelled"})
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Service) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	for _, active := range s.captures {
		active.cancel()
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

func (s *Service) CancelCapture(segmentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active, exists := s.captures[segmentID]; exists {
		active.cancel()
	}
}

// CancelCaptureGeneration cannot cancel a new recording started after an edit.
func (s *Service) CancelCaptureGeneration(segmentID string, generation int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active, exists := s.captures[segmentID]; exists && active.generation == generation {
		active.cancel()
	}
}

func (s *Service) registerCapture(segmentID string, generation int64, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, exists := s.captures[segmentID]; exists {
		if old.generation >= generation {
			return false
		}
		old.cancel()
	}
	s.captures[segmentID] = capture{generation: generation, cancel: cancel}
	return true
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

func (s *Service) Capture(parent context.Context, sessionID, segmentID string, revision int64, input io.ReadCloser, rate int, replace, deferCommit bool) (result error) {
	select {
	case s.captureSlots <- struct{}{}:
		defer func() { <-s.captureSlots }()
	default:
		return store.ErrBusy
	}
	mode := store.CaptureAppend
	if replace {
		mode = store.CaptureReplace
	}
	if deferCommit {
		mode = store.CaptureStaged
	}
	generation, err := s.Store.BeginCaptureMode(parent, sessionID, segmentID, revision, mode)
	if err != nil {
		return err
	}
	lastText := ""
	defer func() {
		if result == nil {
			return
		}
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		if deferCommit {
			status, next, updateErr := s.Store.AbortReplacement(cleanup, sessionID, segmentID)
			if updateErr == nil && status == "cancelled" {
				s.Events.Publish(events.Event{Type: "draft.updated", SessionID: sessionID, SegmentID: segmentID, Revision: next, Status: "failed"})
			}
			return
		}
		next, updateErr := s.Store.UpdateTranscript(cleanup, sessionID, segmentID, generation, lastText, "failed")
		if updateErr == nil {
			s.Events.Publish(events.Event{Type: "draft.updated", SessionID: sessionID, SegmentID: segmentID, Revision: next, Status: "failed"})
		}
	}()
	ctx, cancel := context.WithTimeout(parent, s.Config.CaptureRequestTimeout())
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { input.Close() })
	defer stopClose()
	if !s.registerCapture(segmentID, generation, cancel) {
		return store.ErrStale
	}
	defer func() {
		s.mu.Lock()
		if active := s.captures[segmentID]; active.generation == generation {
			delete(s.captures, segmentID)
		}
		s.mu.Unlock()
	}()
	segment, checkErr := s.Store.Segment(ctx, sessionID, segmentID)
	if checkErr != nil {
		return checkErr
	}
	if segment.Generation != generation || segment.State != "capturing" {
		return store.ErrStale
	}
	s.Events.Publish(events.Event{Type: "draft.updated", SessionID: sessionID, SegmentID: segmentID, Status: "capturing"})
	finalized := false
	err = s.ASR.Transcribe(ctx, input, rate, func(update asr.Update) error {
		state := "capturing"
		if update.Final {
			state = "ready"
		}
		next, err := s.Store.UpdateTranscript(ctx, sessionID, segmentID, generation, update.Text, state)
		if err != nil {
			return err
		}
		lastText = update.Text
		finalized = update.Final
		if update.Final && deferCommit {
			state = "staged"
		}
		s.Events.Publish(events.Event{Type: "draft.updated", SessionID: sessionID, SegmentID: segmentID, Revision: next, Status: state})
		return nil
	})
	if err == nil && !finalized {
		err = asr.ErrUpstream
	}
	return err
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
