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
	captures     map[string]capture
	captureSlots chan struct{}
	wake         chan struct{}
	workers      sync.WaitGroup
	cancel       context.CancelFunc
}

func New(st *store.Store, cfg config.Config, client *hermes.Client, transcriber asr.Transcriber, logger *slog.Logger) *Service {
	return &Service{Store: st, Config: cfg, Hermes: client, ASR: transcriber, Events: events.NewHub(), Log: logger,
		captures: make(map[string]capture), captureSlots: make(chan struct{}, 4), wake: make(chan struct{}, 1)}
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
	var err error
	switch session.State {
	case "creating":
		err = s.Hermes.CreateSession(ctx, session.ID)
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

func (s *Service) Capture(parent context.Context, sessionID, segmentID string, revision int64, input io.ReadCloser, rate int) error {
	select {
	case s.captureSlots <- struct{}{}:
		defer func() { <-s.captureSlots }()
	default:
		return store.ErrBusy
	}
	generation, err := s.Store.BeginCapture(parent, sessionID, segmentID, revision)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, s.Config.CaptureTimeout)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { input.Close() })
	defer stopClose()
	s.mu.Lock()
	if old, exists := s.captures[segmentID]; exists {
		old.cancel()
	}
	s.captures[segmentID] = capture{generation: generation, cancel: cancel}
	s.mu.Unlock()
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
	lastText := ""
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
		s.Events.Publish(events.Event{Type: "draft.updated", SessionID: sessionID, SegmentID: segmentID, Revision: next, Status: state})
		return nil
	})
	if err != nil {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		next, updateErr := s.Store.UpdateTranscript(cleanup, sessionID, segmentID, generation, lastText, "failed")
		if updateErr == nil {
			s.Events.Publish(events.Event{Type: "draft.updated", SessionID: sessionID, SegmentID: segmentID, Revision: next, Status: "failed"})
		}
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
		offset, err := s.Store.AppendReply(ctx, message.ID, update.Text)
		if err != nil {
			return err
		}
		for text := update.Text; text != ""; {
			chunk := model.Prefix(text, 64)
			s.Events.Publish(events.Event{Type: "chat.delta", SessionID: message.SessionID, MessageID: message.ID, Text: chunk, Offset: offset})
			offset += len(chunk)
			text = text[len(chunk):]
		}
		return nil
	})
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
