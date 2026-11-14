package httpapi

import (
	"io"
	"net/http"
	"strconv"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func (s *Server) turn(w http.ResponseWriter, r *http.Request) {
	mode, anchor := "latest", ""
	for _, key := range []string{"message", "before", "after"} {
		if values, present := r.URL.Query()[key]; present {
			if len(values) != 1 || !model.ValidID(values[0]) || mode != "latest" {
				writeError(w, 400, "invalid_message_selector")
				return
			}
			mode, anchor = key, values[0]
		}
	}
	turn, err := s.service.Store.Turn(r.Context(), r.PathValue("session"), mode, anchor)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, turn)
}

func writeContent(w http.ResponseWriter, text string, revision int64) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(text)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Revision", strconv.FormatInt(revision, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, text)
}

func checkContentRevision(w http.ResponseWriter, r *http.Request, revision int64) bool {
	value := r.URL.Query().Get("revision")
	if !r.URL.Query().Has("revision") {
		return true
	}
	expected, err := strconv.ParseInt(value, 10, 64)
	if err != nil || expected < 1 {
		writeError(w, 400, "invalid_revision")
		return false
	}
	if expected != revision {
		writeError(w, 409, "stale_revision")
		return false
	}
	return true
}

func (s *Server) messageContent(w http.ResponseWriter, r *http.Request) {
	part := r.URL.Query().Get("part")
	if part != "input" && part != "reply" {
		writeError(w, 400, "invalid_part")
		return
	}
	message, err := s.service.Store.Message(r.Context(), r.PathValue("session"), r.PathValue("message"))
	if err != nil {
		storeError(w, err)
		return
	}
	if !checkContentRevision(w, r, message.Revision) {
		return
	}
	w.Header().Set("X-Message-State", message.State)
	text := message.Input
	if part == "reply" {
		text = message.Reply
	}
	writeContent(w, text, message.Revision)
}

func (s *Server) draftContent(w http.ResponseWriter, r *http.Request) {
	text, revision, err := s.service.Store.DraftText(r.Context(), r.PathValue("session"))
	if err != nil {
		storeError(w, err)
		return
	}
	if !checkContentRevision(w, r, revision) {
		return
	}
	writeContent(w, text, revision)
}
