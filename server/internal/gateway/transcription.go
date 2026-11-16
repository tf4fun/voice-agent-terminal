package gateway

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/asr"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

// Transcription is a side channel: it turns one recording into text for the
// client's own draft and persists nothing. Live text is kept in memory while
// the recording runs and for transcriptKeep afterwards, so the recorder's last
// poll still finds it.
var transcriptKeep = time.Minute

// transcriptsKept bounds the transcriptions held at once. Each text is at most
// model.MaxTranscriptBytes, so this also bounds their bytes. Running uploads
// are fewer than this (captureSlots), so a full table evicts its oldest
// finished entry.
const transcriptsKept = 16

// ErrNoSpeech reports a recording whose final transcript is empty.
var ErrNoSpeech = errors.New("no_speech")

type transcription struct {
	mu       sync.Mutex
	text     string
	revision int64
	cancel   context.CancelFunc
	finished time.Time   // Zero while the upload runs; guarded by Service.mu.
	expiry   *time.Timer // Guarded by Service.mu.
}

func (t *transcription) stop() { t.cancel() }

// Transcribe streams one recording to the transcriber and returns its final
// text. Each ID is used once; a repeated upload while it is known conflicts.
func (s *Service) Transcribe(parent context.Context, id string, input io.ReadCloser, rate int) (string, error) {
	select {
	case s.captureSlots <- struct{}{}:
		defer func() { <-s.captureSlots }()
	default:
		return "", store.ErrBusy
	}
	ctx, cancel := context.WithTimeout(parent, s.Config.CaptureRequestTimeout())
	defer cancel()
	entry := &transcription{cancel: cancel}
	s.mu.Lock()
	if s.transcripts[id] != nil {
		s.mu.Unlock()
		return "", store.ErrConflict
	}
	if len(s.transcripts) >= transcriptsKept && !s.evictTranscript() {
		s.mu.Unlock()
		return "", store.ErrBusy
	}
	s.transcripts[id] = entry
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		entry.finished = time.Now()
		entry.expiry = time.AfterFunc(transcriptKeep, func() {
			s.mu.Lock()
			if s.transcripts[id] == entry {
				delete(s.transcripts, id)
			}
			s.mu.Unlock()
		})
	}()
	stopClose := context.AfterFunc(ctx, func() { input.Close() })
	defer stopClose()
	final, finalized := "", false
	emit := func(update asr.Update) error {
		if len(update.Text) > model.MaxTranscriptBytes {
			return store.ErrLimit
		}
		entry.mu.Lock()
		entry.text = update.Text
		entry.revision++
		entry.mu.Unlock()
		if update.Final {
			final, finalized = update.Text, true
		}
		return nil
	}
	prepared := s.takePrepared(id)
	var err error
	if preparer, ok := s.ASR.(asr.Preparer); ok && prepared != nil {
		err = preparer.TranscribePrepared(ctx, prepared, input, rate, emit)
	} else {
		if prepared != nil {
			_ = prepared.Close()
		}
		err = s.ASR.Transcribe(ctx, input, rate, emit)
	}
	if err == nil && !finalized {
		err = asr.ErrUpstream
	}
	if err != nil {
		if parent.Err() == nil {
			s.Log.Warn("transcription failed", "transcription_id", id, "prepared", prepared != nil, "error", err.Error())
		}
		return "", err
	}
	if final = strings.TrimSpace(final); final == "" {
		return "", ErrNoSpeech
	}
	return final, nil
}

// evictTranscript drops the oldest finished transcription; s.mu is held.
func (s *Service) evictTranscript() bool {
	oldest := ""
	for id, entry := range s.transcripts {
		if !entry.finished.IsZero() && (oldest == "" || entry.finished.Before(s.transcripts[oldest].finished)) {
			oldest = id
		}
	}
	if oldest == "" {
		return false
	}
	s.transcripts[oldest].expiry.Stop()
	delete(s.transcripts, oldest)
	return true
}

// TranscriptText is the live or final text of a recent transcription.
func (s *Service) TranscriptText(id string) (string, int64, bool) {
	s.mu.Lock()
	entry := s.transcripts[id]
	s.mu.Unlock()
	if entry == nil {
		return "", 0, false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.text, entry.revision, true
}
