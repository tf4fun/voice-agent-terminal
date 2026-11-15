package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/gateway"
)

// speech streams a reply's synthesized voice as framed binary: a kind byte, a
// little-endian uint16 length and the payload (sentence start, 16 kHz PCM16LE
// mono audio, or end with a status byte). Synthesis follows the reply while it
// is still being generated; listeners joining later start from the beginning.
func (s *Server) speech(w http.ResponseWriter, r *http.Request) {
	reader, err := s.service.OpenSpeech(r.Context(), r.PathValue("session"), r.PathValue("message"))
	if errors.Is(err, gateway.ErrSpeechUnavailable) {
		writeError(w, 503, "speech_unavailable")
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
	w.WriteHeader(http.StatusOK)
	_ = controller.Flush()
	keepalive := []byte{gateway.FrameKeepalive, 0, 0}
	for {
		// Hermes may think for a long time before the first sentence; an empty
		// frame every 5 s keeps the device's read timeout from firing.
		wait, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		frame, ok, err := reader.Next(wait)
		cancel()
		if err != nil && r.Context().Err() == nil && errors.Is(err, context.DeadlineExceeded) {
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
