package gateway

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/events"
	"github.com/tf4fun/voice-agent-terminal/server/internal/speech"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

// Speech stream framing, shared with the device: one byte kind, a
// little-endian uint16 payload length, then the payload.
const (
	FrameSentence  = 1 // uint16 sentence index, then the UTF-8 sentence text.
	FrameAudio     = 2 // 16 kHz PCM16LE mono.
	FrameEnd       = 3 // One status byte.
	FrameKeepalive = 4 // Empty; sent while the reply is still being thought about.

	SpeechDone      = 0
	SpeechFailed    = 1
	SpeechCancelled = 2
	SpeechLimit     = 3

	speechFrameAudio = 4096     // PCM bytes per audio frame (128 ms).
	speechMaxBytes   = 16 << 20 // About 8.7 minutes of audio per reply.
	speechMaxStreams = 8
	speechKeep       = 10 * time.Minute // Finished streams stay replayable.
	speechIdleCancel = 10 * time.Second // Synthesis stops this long after the last listener leaves.
)

var ErrSpeechUnavailable = errors.New("speech_unavailable")

// speechStream is the synthesized speech of one reply. Frames are appended by
// one synthesis goroutine and read by any number of listeners, each from the
// start, so a reconnecting device resumes without a second synthesis.
type speechStream struct {
	mu       sync.Mutex
	frames   [][]byte
	bytes    int
	done     bool
	status   byte
	changed  chan struct{}
	readers  int
	cancel   context.CancelFunc
	finished time.Time
	idle     *time.Timer
}

func (st *speechStream) append(frame []byte) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.done {
		return false
	}
	st.frames = append(st.frames, frame)
	st.bytes += len(frame)
	close(st.changed)
	st.changed = make(chan struct{})
	return true
}

func (st *speechStream) end(status byte) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.done {
		return
	}
	st.frames = append(st.frames, frame(FrameEnd, []byte{status}))
	st.done = true
	st.status = status
	st.finished = time.Now()
	close(st.changed)
	st.changed = make(chan struct{})
}

func frame(kind byte, payload []byte) []byte {
	out := make([]byte, 3, 3+len(payload))
	out[0] = kind
	binary.LittleEndian.PutUint16(out[1:], uint16(len(payload)))
	return append(out, payload...)
}

// SpeechReader returns frames of one stream in order.
type SpeechReader struct {
	stream *speechStream
	next   int
	closer func()
	once   sync.Once
}

// Next blocks until a frame is available; ok is false after the end frame.
func (r *SpeechReader) Next(ctx context.Context) ([]byte, bool, error) {
	for {
		r.stream.mu.Lock()
		if r.next < len(r.stream.frames) {
			f := r.stream.frames[r.next]
			r.next++
			r.stream.mu.Unlock()
			return f, true, nil
		}
		done, changed := r.stream.done, r.stream.changed
		r.stream.mu.Unlock()
		if done {
			return nil, false, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-changed:
		}
	}
}

func (r *SpeechReader) Close() { r.once.Do(r.closer) }

type speechStreams struct {
	mu      sync.Mutex
	streams map[string]*speechStream
}

// OpenSpeech starts or joins the synthesis of a reply.
func (s *Service) OpenSpeech(ctx context.Context, sessionID, messageID string) (*SpeechReader, error) {
	if s.TTS == nil || !s.TTS.Enabled() {
		return nil, ErrSpeechUnavailable
	}
	if _, err := s.Store.Message(ctx, sessionID, messageID); err != nil {
		return nil, err
	}
	s.speech.mu.Lock()
	defer s.speech.mu.Unlock()
	if s.speech.streams == nil {
		s.speech.streams = make(map[string]*speechStream)
	}
	s.pruneSpeechLocked()
	stream := s.speech.streams[messageID]
	if stream != nil {
		// A stream stopped for lack of listeners, or by a failure, is redone.
		stream.mu.Lock()
		retry := stream.done && stream.readers == 0 && stream.status != SpeechDone && stream.status != SpeechLimit
		stream.mu.Unlock()
		if retry {
			delete(s.speech.streams, messageID)
			stream = nil
		}
	}
	if stream == nil {
		if len(s.speech.streams) >= speechMaxStreams {
			return nil, store.ErrBusy
		}
		base := s.baseContext()
		synthCtx, cancel := context.WithCancel(base)
		stream = &speechStream{changed: make(chan struct{}), cancel: cancel}
		s.speech.streams[messageID] = stream
		go s.synthesize(synthCtx, stream, sessionID, messageID)
	}
	stream.mu.Lock()
	stream.readers++
	if stream.idle != nil {
		stream.idle.Stop()
		stream.idle = nil
	}
	stream.mu.Unlock()
	reader := &SpeechReader{stream: stream}
	reader.closer = func() {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		stream.readers--
		if stream.readers == 0 && !stream.done {
			// Nobody is listening: stop paying for speech after a short grace.
			stream.idle = time.AfterFunc(speechIdleCancel, stream.cancel)
		}
	}
	return reader, nil
}

func (s *Service) pruneSpeechLocked() {
	for id, stream := range s.speech.streams {
		stream.mu.Lock()
		stale := stream.done && stream.readers == 0 && time.Since(stream.finished) > speechKeep
		stream.mu.Unlock()
		if stale {
			delete(s.speech.streams, id)
		}
	}
	if len(s.speech.streams) < speechMaxStreams {
		return
	}
	// Make room by dropping the oldest finished stream nobody is reading.
	var oldest string
	var at time.Time
	for id, stream := range s.speech.streams {
		stream.mu.Lock()
		if stream.done && stream.readers == 0 && (oldest == "" || stream.finished.Before(at)) {
			oldest, at = id, stream.finished
		}
		stream.mu.Unlock()
	}
	if oldest != "" {
		delete(s.speech.streams, oldest)
	}
}

func activeReply(state string) bool {
	switch state {
	case "queued", "sending", "waiting", "receiving", "attention":
		return true
	}
	return false
}

// synthesize follows the stored reply as it grows and speaks each complete
// sentence in order.
func (s *Service) synthesize(ctx context.Context, stream *speechStream, sessionID, messageID string) {
	defer stream.cancel()
	wake := s.watchMessage(ctx, messageID)
	offset, index := 0, 0
	started := time.Now()
	for {
		if ctx.Err() != nil {
			stream.end(SpeechCancelled)
			return
		}
		message, err := s.Store.Message(ctx, sessionID, messageID)
		if err != nil {
			stream.end(SpeechFailed)
			return
		}
		final := !activeReply(message.State)
		sentence, next, ok := speech.Next(message.Reply, offset, final)
		if !ok {
			if final {
				stream.end(SpeechDone)
				s.Log.Info("speech finished", "message_id", messageID, "sentences", index, "elapsed_ms", time.Since(started).Milliseconds())
				return
			}
			select {
			case <-ctx.Done():
			case <-wake:
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		offset = next
		payload := binary.LittleEndian.AppendUint16(nil, uint16(index))
		text := sentence
		if len(text) > 1024 {
			text = text[:1024]
		}
		if !stream.append(frame(FrameSentence, append(payload, text...))) {
			return
		}
		pending := make([]byte, 0, speechFrameAudio)
		flush := func() bool {
			if len(pending) == 0 {
				return true
			}
			ok := stream.append(frame(FrameAudio, pending))
			pending = make([]byte, 0, speechFrameAudio)
			return ok
		}
		limited := false
		err = s.TTS.Speak(ctx, sentence, func(pcm []byte) error {
			for len(pcm) > 0 {
				take := min(len(pcm), speechFrameAudio-len(pending))
				pending = append(pending, pcm[:take]...)
				pcm = pcm[take:]
				if len(pending) == speechFrameAudio && !flush() {
					return context.Canceled
				}
			}
			stream.mu.Lock()
			limited = stream.bytes > speechMaxBytes
			stream.mu.Unlock()
			if limited {
				return errSpeechLimit
			}
			return nil
		})
		if err == nil && !flush() {
			return
		}
		switch {
		case limited:
			stream.end(SpeechLimit)
			return
		case err != nil && ctx.Err() != nil:
			stream.end(SpeechCancelled)
			return
		case err != nil:
			s.Log.Warn("speech synthesis failed", "message_id", messageID, "sentence", index, "error", err.Error())
			stream.end(SpeechFailed)
			return
		}
		index++
	}
}

var errSpeechLimit = errors.New("speech_limit")

// watchMessage turns events about one message into a coalescing wake signal.
// It never blocks the hub; when the hub drops this subscriber, it resubscribes.
func (s *Service) watchMessage(ctx context.Context, messageID string) <-chan struct{} {
	wake := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			channel, cancel, ok := s.Events.Subscribe()
			if !ok {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
					continue
				}
			}
			s.drainEvents(ctx, channel, messageID, wake)
			cancel()
		}
	}()
	return wake
}

func (s *Service) drainEvents(ctx context.Context, channel <-chan events.Event, messageID string, wake chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-channel:
			if !open {
				return
			}
			if event.MessageID == messageID {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}
}

// SpeechEnabled reports whether replies can be spoken.
func (s *Service) SpeechEnabled() bool { return s.TTS != nil && s.TTS.Enabled() }
