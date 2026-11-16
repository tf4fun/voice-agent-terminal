package gateway

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/events"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/speech"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
	"github.com/tf4fun/voice-agent-terminal/server/internal/tts"
)

// Speech stream framing, shared with the device: one byte kind, a
// little-endian uint16 payload length, then the payload.
const (
	FrameSentence  = 1 // Speech starts: uint16 index 0, then the start of the spoken text.
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
	// The stream opens only once this much audio is ready, so the device starts
	// with a lead; synthesis then runs faster than playback and stays ahead.
	speechLeadBytes = 32000 // 1 s of 16 kHz PCM16
	speechHeadBytes = 240   // Reply start carried by the sentence frame (device limit 256).
)

var (
	ErrSpeechUnavailable = errors.New("speech_unavailable")
	// ErrSpeechRestarted refuses a resume into a stream that would have to be
	// synthesized again: new audio need not match the bytes already played.
	ErrSpeechRestarted = errors.New("speech_restarted")
)

// speechStream is the synthesized speech of one reply. Frames are appended by
// one synthesis goroutine and read by any number of listeners, each from the
// start, so a reconnecting device resumes without a second synthesis.
type speechStream struct {
	id       string
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
	// abandoned is set when the idle timer stops synthesis; the stream is
	// about to end as cancelled, so a new listener starts a fresh one.
	abandoned bool
}

// join adds a listener and cancels a pending idle stop; st.mu is held.
func (st *speechStream) join() {
	st.readers++
	if st.idle != nil {
		st.idle.Stop()
		st.idle = nil
	}
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

// audioMillis is the length of the audio appended so far.
func (st *speechStream) audioMillis() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	total := 0
	for _, f := range st.frames {
		if f[0] == FrameAudio {
			total += len(f) - 3
		}
	}
	return total / 32
}

func frame(kind byte, payload []byte) []byte {
	out := make([]byte, 3, 3+len(payload))
	out[0] = kind
	binary.LittleEndian.PutUint16(out[1:], uint16(len(payload)))
	return append(out, payload...)
}

// SpeechReader returns the bytes of one stream in order. A reader resuming
// after a dropped connection starts `skip` bytes in, possibly mid-frame: the
// first chunk it returns may be the tail of a frame, then whole frames follow.
type SpeechReader struct {
	stream *speechStream
	next   int
	skip   int
	closer func()
	once   sync.Once
}

// Next blocks until bytes are available; ok is false after the end frame.
func (r *SpeechReader) Next(ctx context.Context) ([]byte, bool, error) {
	for {
		r.stream.mu.Lock()
		if r.next < len(r.stream.frames) {
			f := r.stream.frames[r.next]
			r.next++
			if r.skip >= len(f) {
				r.skip -= len(f)
				r.stream.mu.Unlock()
				continue
			}
			f, r.skip = f[r.skip:], 0
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

// Resuming reports that the reader has not yet reached its resume offset. The
// offset may fall inside a frame that is not synthesized yet (the stream is
// being redone), so nothing may be inserted between frames until it is reached.
func (r *SpeechReader) Resuming() bool { return r.skip > 0 }

func (r *SpeechReader) StreamID() string { return r.stream.id }

func (r *SpeechReader) Close() { r.once.Do(r.closer) }

type speechStreams struct {
	mu      sync.Mutex
	streams map[string]*speechStream
}

// speechKey names the speech of one attempt at a reply; a retry under the
// same message ID writes a new reply and so gets a new stream.
func speechKey(messageID string, attempts int) string {
	return messageID + "/" + strconv.Itoa(attempts)
}

// OpenSpeech starts or joins the synthesis of a reply. A resume (offset > 0)
// requires the exact stream ID it started on; any replaced or missing stream
// returns ErrSpeechRestarted without touching a replacement stream.
func (s *Service) OpenSpeech(ctx context.Context, sessionID, messageID, streamID string, offset int) (*SpeechReader, error) {
	if s.TTS == nil || !s.TTS.Enabled() {
		return nil, ErrSpeechUnavailable
	}
	message, err := s.Store.Message(ctx, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	key := speechKey(messageID, message.Attempts)
	s.speech.mu.Lock()
	defer s.speech.mu.Unlock()
	if s.speech.streams == nil {
		s.speech.streams = make(map[string]*speechStream)
	}
	s.pruneSpeechLocked()
	stream := s.speech.streams[key]
	if stream != nil {
		// A stream stopped for lack of listeners, or by a failure, is redone.
		// The check and the join share one lock, so the idle timer cannot
		// abandon the stream between them.
		stream.mu.Lock()
		retry := stream.readers == 0 && (stream.abandoned && !stream.done ||
			stream.done && stream.status != SpeechDone && stream.status != SpeechLimit)
		match := streamID == "" || stream.id == streamID
		canJoin := !retry && match && (offset == 0 || streamID != "")
		if canJoin {
			stream.join()
		}
		stream.mu.Unlock()
		if retry {
			delete(s.speech.streams, key)
			stream = nil
		} else if !match || (offset > 0 && streamID == "") {
			return nil, ErrSpeechRestarted
		}
	}
	if stream == nil {
		if offset > 0 || streamID != "" {
			return nil, ErrSpeechRestarted
		}
		if len(s.speech.streams) >= speechMaxStreams {
			return nil, store.ErrBusy
		}
		base := s.baseContext()
		synthCtx, cancel := context.WithCancel(base)
		stream = &speechStream{id: model.NewID(), changed: make(chan struct{}), cancel: cancel, readers: 1}
		s.speech.streams[key] = stream
		go s.synthesize(synthCtx, stream, sessionID, messageID)
	}
	reader := &SpeechReader{stream: stream, skip: offset}
	reader.closer = func() {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		stream.readers--
		if stream.readers == 0 && !stream.done {
			// Nobody is listening: stop paying for speech after a short grace.
			stream.idle = time.AfterFunc(speechIdleCancel, func() {
				stream.mu.Lock()
				abandoned := stream.readers == 0
				stream.abandoned = abandoned
				stream.mu.Unlock()
				if abandoned {
					stream.cancel()
				}
			})
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

// synthesize waits for the stored reply to finish, then speaks all of it in
// one request into the stream cache, which any number of listeners read and
// resume from at any byte.
func (s *Service) synthesize(ctx context.Context, stream *speechStream, sessionID, messageID string) {
	defer stream.cancel()
	wake := s.watchMessage(ctx, messageID)
	started := time.Now()
	var message model.Message
	for {
		var err error
		message, err = s.Store.Message(ctx, sessionID, messageID)
		if ctx.Err() != nil {
			stream.end(SpeechCancelled)
			return
		}
		if err != nil {
			stream.end(SpeechFailed)
			return
		}
		if !activeReply(message.State) {
			break
		}
		select {
		case <-ctx.Done():
		case <-wake:
		case <-time.After(500 * time.Millisecond):
		}
	}
	// Markdown, code blocks and link targets are cleaned from the full reply,
	// then the whole text goes upstream in one request.
	text := speech.SpokenText(message.Reply)
	if text == "" {
		stream.end(SpeechDone)
		return
	}
	if len(text) > tts.MaxText {
		text = strings.ToValidUTF8(text[:tts.MaxText], "")
	}
	// The sentence frame tells the device that speech starts; the device
	// shows the reply text it fetched itself, so only the start is sent.
	head := text
	if len(head) > speechHeadBytes {
		head = strings.ToValidUTF8(head[:speechHeadBytes], "")
	}
	if !stream.append(frame(FrameSentence, append([]byte{0, 0}, head...))) {
		return
	}
	// Audio is held until the device can start with a lead; synthesis runs
	// faster than playback and stays ahead after that.
	var held []byte
	holding := true
	pending := make([]byte, 0, speechFrameAudio)
	flush := func() bool {
		if len(pending) == 0 {
			return true
		}
		f := frame(FrameAudio, pending)
		pending = make([]byte, 0, speechFrameAudio)
		if holding {
			held = append(held, f...)
			if len(held) < speechLeadBytes {
				return true
			}
			return release(stream, &held, &holding)
		}
		return stream.append(f)
	}
	limited := false
	err := s.TTS.Speak(ctx, text, func(pcm []byte) error {
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
	if (err == nil && !flush()) || !release(stream, &held, &holding) {
		return
	}
	switch {
	case limited:
		stream.end(SpeechLimit)
	case err != nil && ctx.Err() != nil:
		stream.end(SpeechCancelled)
	case err != nil:
		s.Log.Warn("speech synthesis failed", "message_id", messageID, "error", err.Error())
		stream.end(SpeechFailed)
	default:
		stream.end(SpeechDone)
		s.Log.Info("speech finished", "message_id", messageID, "text_bytes", len(text),
			"audio_ms", stream.audioMillis(), "elapsed_ms", time.Since(started).Milliseconds())
	}
}

// release appends the frames held for the lead, kept as one byte run.
func release(stream *speechStream, held *[]byte, holding *bool) bool {
	*holding = false
	run := *held
	*held = nil
	for len(run) > 0 {
		size := 3 + int(binary.LittleEndian.Uint16(run[1:]))
		if !stream.append(run[:size:size]) {
			return false
		}
		run = run[size:]
	}
	return true
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
