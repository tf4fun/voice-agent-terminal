package httpapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/tf4fun/voice-agent-terminal/server/internal/events"
	"github.com/tf4fun/voice-agent-terminal/server/internal/gateway"
	"github.com/tf4fun/voice-agent-terminal/server/internal/hermes"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

const Version = "0.2.0"

type Server struct{ service *gateway.Service }

func New(service *gateway.Service) http.Handler {
	s := &Server{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", managementAsset(managementHTML, "text/html; charset=utf-8"))
	mux.HandleFunc("GET /ui/app.css", managementAsset(managementCSS, "text/css; charset=utf-8"))
	mux.HandleFunc("GET /ui/app.js", managementAsset(managementJS, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /ui/icon.svg", managementAsset(managementIcon, "image/svg+xml"))
	handle := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			for _, parameter := range []string{"session", "transcription", "message"} {
				if value := r.PathValue(parameter); value != "" && !model.ValidID(value) {
					writeError(w, 400, "invalid_id")
					return
				}
			}
			handler(w, r)
		})
	}
	handle("GET /api/v1/health", s.health)
	handle("GET /api/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "protocol": 1})
	})
	handle("GET /api/v1/server", s.capabilities)
	handle("GET /api/v1/events", s.events)
	handle("GET /api/v1/sessions", s.sessions)
	handle("GET /api/v1/sessions/{session}", s.session)
	handle("PATCH /api/v1/sessions/{session}", s.renameSession)
	handle("DELETE /api/v1/sessions/{session}", s.deleteSession)
	handle("POST /api/v1/sessions/{session}/messages", s.send)
	handle("GET /api/v1/sessions/{session}/messages", s.messages)
	handle("GET /api/v1/sessions/{session}/conversation", s.conversation)
	handle("GET /api/v1/sessions/{session}/turn", s.turn)
	handle("GET /api/v1/sessions/{session}/messages/{message}/content", s.messageContent)
	handle("GET /api/v1/sessions/{session}/messages/{message}", s.message)
	handle("GET /api/v1/sessions/{session}/messages/{message}/text", s.messageText)
	handle("POST /api/v1/sessions/{session}/messages/{message}/retry", s.retry)
	handle("GET /api/v1/sessions/{session}/messages/{message}/speech", s.speech)
	handle("POST /api/v1/transcriptions", s.prepareTranscription)
	handle("POST /api/v1/transcriptions/{transcription}/audio", s.audio)
	handle("GET /api/v1/transcriptions/{transcription}/text", s.transcriptText)
	handle("POST /api/v1/device/logs", s.addDeviceReport)
	handle("GET /api/v1/device/logs", s.deviceReports)
	handle("GET /api/v1/device/logs/{report}", s.deviceReport)
	protected := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				service.Log.Error("request handler failed")
				writeError(w, 500, "internal_error")
			}
		}()
		if key := service.Config.APIKey; key != "" && r.URL.Path != "/api/v1/health" && !managementPageRequest(r) {
			expected := "Bearer " + key
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
				writeError(w, 401, "unauthorized")
				return
			}
		}
		protected.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "not_found")
	case errors.Is(err, store.ErrStale):
		writeError(w, 409, "stale_revision")
	case errors.Is(err, store.ErrBusy):
		writeError(w, 409, "busy")
	case errors.Is(err, store.ErrConflict):
		writeError(w, 409, "conflict")
	case errors.Is(err, store.ErrLimit):
		writeError(w, 413, "limit_exceeded")
	default:
		writeError(w, 500, "storage_error")
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil {
		writeError(w, 413, "request_too_large")
		return false
	}
	if !utf8.Valid(body) {
		writeError(w, 400, "invalid_utf8")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, 400, "invalid_json")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, 400, "invalid_json")
		return false
	}
	return true
}

func paging(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset := 4, 0
	var err error
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
	}
	if err != nil || limit < 1 || limit > 20 {
		writeError(w, 400, "invalid_pagination")
		return 0, 0, false
	}
	if value := r.URL.Query().Get("offset"); value != "" {
		offset, err = strconv.Atoi(value)
	}
	if err != nil || offset < 0 || offset > 1_000_000 {
		writeError(w, 400, "invalid_pagination")
		return 0, 0, false
	}
	return limit, offset, true
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.service.Store.Ping(r.Context()); err != nil {
		writeError(w, 503, "database_unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"service": "voice-agent-server", "version": Version, "database": "ok"})
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"service": "voice-agent-server", "protocol": 2, "version": Version,
		"chat": s.service.Hermes.Enabled(), "transcription": s.service.ASR.Enabled(), "asr_provider": s.service.Config.ASRProvider,
		"audio_format": "pcm_s16le", "sample_rates": []int{16000, 24000},
		"bounded_paging": true, "text_before": true, "text_tail": true,
		"conversation_windows": true, "whole_messages": true, "reply_sse": true,
		"max_input_bytes": model.MaxInputBytes, "max_transcript_bytes": model.MaxTranscriptBytes,
		"max_text_window_bytes": model.WindowBytes, "max_event_bytes": 1024,
		"max_audio_ms": s.service.Config.CaptureTimeout.Milliseconds(), "device_logs": true,
		"speech": s.service.SpeechEnabled(), "speech_format": "pcm_s16le", "speech_rate": 16000,
	})
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := paging(w, r)
	if !ok {
		return
	}
	items, err := s.service.Store.Sessions(r.Context(), limit+1, offset)
	if err != nil {
		storeError(w, err)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writeJSON(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "has_more": hasMore})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	session, err := s.service.Store.Session(r.Context(), r.PathValue("session"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, session)
}

func (s *Server) renameSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if body.Title == "" || len(body.Title) > 256 {
		writeError(w, 400, "invalid_title")
		return
	}
	if err := s.service.RenameSession(r.Context(), r.PathValue("session"), body.Title); err != nil {
		var upstream *hermes.UpstreamError
		if errors.As(err, &upstream) {
			writeError(w, 502, upstream.Code)
		} else {
			storeError(w, err)
		}
		return
	}
	s.service.Events.Publish(events.Event{Type: "session.updated", SessionID: r.PathValue("session")})
	s.session(w, r)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session")
	if err := s.service.Store.BeginDelete(r.Context(), id); err != nil {
		storeError(w, err)
		return
	}
	session, err := s.service.Store.Session(r.Context(), id)
	if err != nil {
		storeError(w, err)
		return
	}
	if !s.service.Hermes.Enabled() || s.service.Reconcile(r.Context(), session) != nil {
		writeJSON(w, 202, map[string]string{"id": id, "state": "deleting"})
		return
	}
	writeJSON(w, 200, map[string]string{"id": id, "state": "deleted"})
}

// prepareWait bounds the transcriber handshake while the device shows its
// preparing animation, below the device's 4 s request timeout.
const prepareWait = 3 * time.Second

// prepareTranscription opens the upstream transcriber for the recording that
// follows, so it starts against a ready session. It stores nothing.
func (s *Server) prepareTranscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !model.ValidID(body.ID) {
		writeError(w, 400, "invalid_id")
		return
	}
	if !s.service.ASR.Enabled() {
		writeError(w, 503, "openai_not_configured")
		return
	}
	s.service.PrepareCapture(body.ID, prepareWait)
	writeJSON(w, 201, map[string]string{"id": body.ID})
}

func (s *Server) transcriptText(w http.ResponseWriter, r *http.Request) {
	text, revision, ok := s.service.TranscriptText(r.PathValue("transcription"))
	if !ok {
		writeError(w, 404, "not_found")
		return
	}
	textWindow(w, r, text, revision)
}

func textWindow(w http.ResponseWriter, r *http.Request, text string, revision int64) {
	offset, limit := 0, model.WindowBytes
	var err error
	if value := r.URL.Query().Get("offset"); value != "" {
		offset, err = strconv.Atoi(value)
	}
	if err != nil {
		writeError(w, 400, "invalid_window")
		return
	}
	before := r.URL.Query().Get("before")
	tail := r.URL.Query().Get("tail")
	if (tail != "" && tail != "0" && tail != "1") ||
		(before != "" && (r.URL.Query().Has("offset") || tail == "1")) ||
		(tail == "1" && r.URL.Query().Has("offset")) {
		writeError(w, 400, "invalid_window")
		return
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
	}
	if err != nil {
		writeError(w, 400, "invalid_window")
		return
	}
	if value := r.URL.Query().Get("revision"); value != "" {
		expected, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			writeError(w, 400, "invalid_revision")
			return
		}
		if expected != revision {
			writeError(w, 409, "stale_revision")
			return
		}
	}
	if r.URL.Query().Has("layout") {
		writeError(w, 400, "invalid_layout")
		return
	}
	window, err := model.TextWindow(text, offset, limit, revision)
	if before != "" {
		end, parseErr := strconv.Atoi(before)
		if parseErr != nil || end < 0 || end > len(text) || (end < len(text) && !utf8.RuneStart(text[end])) ||
			limit < 4 || limit > model.WindowBytes {
			writeError(w, 400, "invalid_window")
			return
		}
		start := max(0, end-limit)
		for start < end && !utf8.RuneStart(text[start]) {
			start++
		}
		window = model.Window{Text: text[start:end], Offset: start, End: end, Total: len(text), Revision: revision}
		err = nil
	}
	if tail == "1" {
		if limit < 4 || limit > model.WindowBytes {
			writeError(w, 400, "invalid_window")
			return
		}
		start := max(0, len(text)-limit)
		for start < len(text) && !utf8.RuneStart(text[start]) {
			start++
		}
		window = model.Window{Text: text[start:], Offset: start, End: len(text), Total: len(text), Revision: revision}
		err = nil
	}
	if err != nil {
		writeError(w, 400, "invalid_window")
		return
	}
	writeJSON(w, 200, window)
}

func (s *Server) audio(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	if !s.service.ASR.Enabled() {
		writeError(w, 503, "openai_not_configured")
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "audio/pcm" || r.Header.Get("Content-Encoding") != "" {
		writeError(w, 415, "expected_raw_pcm")
		return
	}
	rate := 16000
	if value := r.URL.Query().Get("sample_rate"); value != "" {
		rate, err = strconv.Atoi(value)
	}
	if err != nil || rate != 16000 && rate != 24000 {
		writeError(w, 400, "invalid_sample_rate")
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(s.service.Config.CaptureRequestTimeout()))
	_ = controller.EnableFullDuplex()
	maximum := min(s.service.Config.MaxAudioBytes, int64(rate)*2*int64(s.service.Config.CaptureTimeout/time.Second))
	input := &audioBody{ReadCloser: http.MaxBytesReader(w, r.Body, maximum), controller: controller}
	text, err := s.service.Transcribe(r.Context(), r.PathValue("transcription"), input, rate)
	if err != nil {
		var maximum *http.MaxBytesError
		switch {
		case errors.As(err, &maximum):
			writeError(w, 413, "audio_too_large")
		case errors.Is(err, gateway.ErrNoSpeech):
			writeError(w, 422, "no_speech")
		case errors.Is(err, store.ErrBusy) || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrLimit):
			storeError(w, err)
		default:
			writeError(w, 502, "transcription_failed")
		}
		return
	}
	// The final text is the whole body, so the device reads it as its draft.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(text)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, text)
}

type audioBody struct {
	io.ReadCloser
	controller *http.ResponseController
	complete   atomic.Bool
}

func (body *audioBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if errors.Is(err, io.EOF) {
		body.complete.Store(true)
	}
	return n, err
}

func (body *audioBody) Close() error {
	// Interrupt incomplete uploads while preserving the completed request context.
	if !body.complete.Load() {
		_ = body.controller.SetReadDeadline(time.Now())
	}
	return body.ReadCloser.Close()
}

// send queues one complete input. NewSession creates the session with it, so
// a session exists only once its first message does.
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID         string `json:"id"`
		Text       string `json:"text"`
		NewSession bool   `json:"new_session"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !model.ValidID(body.ID) {
		writeError(w, 400, "invalid_id")
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeError(w, 400, "empty_input")
		return
	}
	if len(body.Text) > model.MaxInputBytes {
		writeError(w, 413, "input_too_large")
		return
	}
	if !s.service.Hermes.Enabled() {
		writeError(w, 503, "hermes_not_configured")
		return
	}
	sessionID := r.PathValue("session")
	message, created, err := s.service.Store.Send(r.Context(), sessionID, body.ID, s.service.Config.HermesModel, s.service.Hermes.Scope(),
		s.service.Config.HermesPrompt, body.Text, body.NewSession)
	if err != nil {
		storeError(w, err)
		return
	}
	session, err := s.service.Store.Session(r.Context(), sessionID)
	if err != nil {
		storeError(w, err)
		return
	}
	if session.State == "creating" {
		// A failed Hermes create leaves the message queued for the reconcile loop.
		_ = s.service.Reconcile(r.Context(), session)
		if refreshed, err := s.service.Store.Session(r.Context(), sessionID); err == nil {
			session = refreshed
		}
	}
	if created {
		s.service.Events.Publish(events.Event{Type: "session.updated", SessionID: sessionID})
	}
	s.service.Wake()
	writeJSON(w, 202, struct {
		model.Message
		Session model.Session `json:"session"`
	}{message, session})
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := paging(w, r)
	if !ok {
		return
	}
	if _, err := s.service.Store.Session(r.Context(), r.PathValue("session")); err != nil {
		storeError(w, err)
		return
	}
	items, err := s.service.Store.Messages(r.Context(), r.PathValue("session"), limit+1, offset)
	if err != nil {
		storeError(w, err)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writeJSON(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "has_more": hasMore})
}

func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	mode, cursor := "tail", ""
	for _, key := range []string{"before", "after", "from"} {
		if value := r.URL.Query().Get(key); value != "" {
			if mode != "tail" || len(value) > 79 {
				writeError(w, 400, "invalid_cursor")
				return
			}
			mode, cursor = key, value
		}
	}
	if r.URL.Query().Has("layout") {
		writeError(w, 400, "invalid_layout")
		return
	}
	page, err := s.service.Store.Conversation(r.Context(), r.PathValue("session"), mode, cursor)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	message, err := s.service.Store.Message(r.Context(), r.PathValue("session"), r.PathValue("message"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, message)
}

func (s *Server) messageText(w http.ResponseWriter, r *http.Request) {
	message, err := s.service.Store.Message(r.Context(), r.PathValue("session"), r.PathValue("message"))
	if err != nil {
		storeError(w, err)
		return
	}
	text := message.Reply
	if part := r.URL.Query().Get("part"); part == "input" {
		text = message.Input
	} else if part != "" && part != "reply" {
		writeError(w, 400, "invalid_part")
		return
	}
	textWindow(w, r, text, message.Revision)
}

func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	if !s.service.Hermes.Enabled() {
		writeError(w, 503, "hermes_not_configured")
		return
	}
	if err := s.service.Store.Retry(r.Context(), r.PathValue("session"), r.PathValue("message"), s.service.Hermes.Scope()); err != nil {
		storeError(w, err)
		return
	}
	s.service.Wake()
	s.message(w, r)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID != "" {
		if !model.ValidID(sessionID) {
			writeError(w, 400, "invalid_id")
			return
		}
		if _, err := s.service.Store.Session(r.Context(), sessionID); err != nil {
			storeError(w, err)
			return
		}
	}
	channel, cancel, ok := s.service.Events.Subscribe()
	if !ok {
		writeError(w, 503, "subscriber_limit")
		return
	}
	defer cancel()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(event events.Event) error {
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if len(payload)+8 > 1024 {
			return errors.New("event too large")
		}
		if _, err := w.Write(append(append([]byte("data: "), payload...), '\n', '\n')); err != nil {
			return err
		}
		return controller.Flush()
	}
	if err := write(events.Event{Type: "resync", SessionID: sessionID}); err != nil {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-channel:
			if !open {
				return
			}
			if sessionID == "" || event.SessionID == sessionID {
				if err := write(event); err != nil {
					return
				}
			}
		case <-ticker.C:
			_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}
