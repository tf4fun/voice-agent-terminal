package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/gateway"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

// speech streams a reply's synthesized voice as framed binary: a kind byte, a
// little-endian uint16 length and the payload (sentence start, 16 kHz PCM16LE
// mono audio, or end with a status byte). Speech starts once the reply is
// complete; the stream is cached so listeners can join or resume at any byte.
func (s *Server) speech(w http.ResponseWriter, r *http.Request) {
	// offset=<bytes>&stream=<id> resumes after a dropped connection on the
	// exact stream it started on, even mid-frame; keepalives are not counted.
	offset := 0
	if value := r.URL.Query().Get("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeError(w, 400, "invalid_offset")
			return
		}
		offset = parsed
	}
	streamID := r.URL.Query().Get("stream")
	if streamID != "" && !model.ValidID(streamID) {
		writeError(w, 400, "invalid_stream")
		return
	}
	reader, err := s.service.OpenSpeech(r.Context(), r.PathValue("session"), r.PathValue("message"), streamID, offset)
	if errors.Is(err, gateway.ErrSpeechUnavailable) {
		writeError(w, 503, "speech_unavailable")
		return
	}
	if errors.Is(err, gateway.ErrSpeechRestarted) {
		writeError(w, 409, "speech_restarted")
		return
	}
	if err != nil {
		storeError(w, err)
		return
	}
	defer reader.Close()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Speech-Format", "pcm_s16le;rate=16000;channels=1")
	w.Header().Set("X-Speech-Stream", reader.StreamID())
	w.WriteHeader(http.StatusOK)
	_ = controller.Flush()
	keepalive := []byte{gateway.FrameKeepalive, 0, 0}
	for {
		// Hermes may think for a long time before speech starts; an empty
		// frame every 5 s keeps the device's read timeout from firing.
		wait, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		frame, ok, err := reader.Next(wait)
		cancel()
		if err != nil && r.Context().Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			if reader.Resuming() {
				// A device resuming mid-frame would read a keepalive as
				// frame bytes; stay silent until the stream reaches it.
				continue
			}
			frame, ok, err = keepalive, true, nil
		}
		if err != nil || !ok {
			return
		}
		// A device plays in real time, so a write may wait for its buffer; a
		// listener that stalls for half a minute is dropped.
		_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := w.Write(frame); err != nil {
			return
		}
		if err := controller.Flush(); err != nil {
			return
		}
	}
}
