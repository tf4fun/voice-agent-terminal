package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/diag"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

// maxDeviceReportBytes bounds one diagnostic upload; the firmware's report is
// about 9 KiB.
const maxDeviceReportBytes = 64 << 10

// addDeviceReport stores a diagnostic report uploaded by a terminal.
func (s *Server) addDeviceReport(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDeviceReportBytes))
	if err != nil {
		writeError(w, 413, "request_too_large")
		return
	}
	report, err := diag.Parse(body)
	if err != nil {
		writeError(w, 400, "invalid_report")
		return
	}
	item := store.DeviceReport{
		ID: model.NewID(), ReceivedAt: time.Now().UnixMilli(), Trigger: report.Trigger,
		Firmware: report.Firmware + " " + report.Build, ResetReason: diag.ResetLabel(report.ResetReason),
		Summary: diag.Summary(report), Body: string(body),
	}
	if err := s.service.Store.AddDeviceReport(r.Context(), item); err != nil {
		storeError(w, err)
		return
	}
	s.service.Log.Warn("device diagnostic report", "report_id", item.ID, "trigger", report.Trigger,
		"firmware", item.Firmware, "boot", report.Boot, "reset", item.ResetReason, "events", len(report.Events),
		"summary", item.Summary)
	writeJSON(w, 201, map[string]string{"id": item.ID})
}

func (s *Server) deviceReports(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := paging(w, r)
	if !ok {
		return
	}
	items, err := s.service.Store.DeviceReports(r.Context(), limit+1, offset)
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

// deviceReport renders one report as text, or returns the raw upload with
// format=json.
func (s *Server) deviceReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("report")
	if !model.ValidID(id) {
		writeError(w, 400, "invalid_id")
		return
	}
	item, err := s.service.Store.DeviceReport(r.Context(), id)
	if err != nil {
		storeError(w, err)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, item.Body)
		return
	}
	report, err := diag.Parse([]byte(item.Body))
	if err != nil && !errors.Is(err, diag.ErrInvalid) {
		storeError(w, err)
		return
	}
	text := diag.Render(report, time.UnixMilli(item.ReceivedAt), time.Local)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(text)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, text)
}
