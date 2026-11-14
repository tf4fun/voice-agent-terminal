package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/asr"
	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func TestStagedAudioCommitAbortContract(t *testing.T) {
	e := setup(t)
	session, old, next := model.NewID(), model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "staged"}, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": old, "revision": 1, "text": "old reviewed draft"}, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": next, "revision": 2}, 201)
	request := httptest.NewRequest("POST", base+"/segments/"+next+"/audio?revision=3&replace=1&defer_commit=1", strings.NewReader(strings.Repeat("\x00", 6400)))
	request.Header.Set("Content-Type", "audio/pcm")
	request.Header.Set("Authorization", "Bearer device-test")
	response := httptest.NewRecorder()
	New(e.service).ServeHTTP(response, request)
	var captured struct {
		Status   string
		Revision int64
	}
	if err := json.Unmarshal(response.Body.Bytes(), &captured); err != nil || response.Code != 200 || captured.Revision <= 3 || captured.Status != "ready" {
		t.Fatalf("audio: %d %s %v", response.Code, response.Body.String(), err)
	}
	segment, _, err := e.service.Store.DraftSegment(context.Background(), session, next)
	if err != nil || segment.State != "staged" {
		t.Fatalf("state=%s %v", segment.State, err)
	}
	text, _, err := e.service.Store.DraftText(context.Background(), session)
	if err != nil || text != "old reviewed draft" {
		t.Fatal(text, err)
	}
	var committed struct{ Revision int64 }
	body := map[string]any{"revision": captured.Revision}
	if err := json.Unmarshal(e.request(t, "POST", base+"/segments/"+next+"/commit", body, 200), &committed); err != nil || committed.Revision != captured.Revision+1 {
		t.Fatal(committed, err)
	}
	// Discard the first response, then retry the identical body.
	var repeated struct{ Revision int64 }
	_ = json.Unmarshal(e.request(t, "POST", base+"/segments/"+next+"/commit", body, 200), &repeated)
	if repeated.Revision != committed.Revision {
		t.Fatal("commit response changed")
	}
	var abort struct {
		Status   string
		Revision int64
	}
	_ = json.Unmarshal(e.request(t, "POST", base+"/segments/"+next+"/abort", nil, 200), &abort)
	if abort.Status != "committed" {
		t.Fatal(abort)
	}
	text, _, err = e.service.Store.DraftText(context.Background(), session)
	if err != nil || text != "测试转写" {
		t.Fatal(text, err)
	}
	var capabilities map[string]any
	if err := json.Unmarshal(e.request(t, "GET", "/api/v1/server", nil, 200), &capabilities); err != nil || capabilities["staged_recording"] != true || capabilities["replacement_reservation"] != true {
		t.Fatal(capabilities, err)
	}
}

func TestStagedAudioRequiresReplacementQuery(t *testing.T) {
	e := setup(t)
	session, next := model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "validation"}, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": next, "revision": 1}, 201)
	for _, query := range []string{"defer_commit=1", "replace=0&defer_commit=1", "replace=1&defer_commit=2"} {
		request := httptest.NewRequest("POST", base+"/segments/"+next+"/audio?revision=2&"+query, strings.NewReader(""))
		request.Header.Set("Content-Type", "audio/pcm")
		request.Header.Set("Authorization", "Bearer device-test")
		response := httptest.NewRecorder()
		New(e.service).ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("query=%s status=%d", query, response.Code)
		}
	}
	e.request(t, "POST", base+"/segments/"+next+"/abort", nil, 200)
	e.request(t, "POST", base+"/segments/"+next+"/abort", nil, 200)
}

type finalizingTranscriber struct{ finalized chan struct{} }

func (*finalizingTranscriber) Enabled() bool { return true }
func (f *finalizingTranscriber) Transcribe(ctx context.Context, input io.ReadCloser, rate int, emit func(asr.Update) error) error {
	if _, err := io.Copy(io.Discard, input); err != nil {
		return err
	}
	if err := emit(asr.Update{Text: "new final text", Final: true}); err != nil {
		return err
	}
	close(f.finalized)
	<-ctx.Done()
	return ctx.Err()
}

func TestCancelAfterFinalTranscriptBeforeAudioResponsePreservesOldDraft(t *testing.T) {
	for _, abort := range []bool{true, false} {
		t.Run(map[bool]string{true: "abort", false: "delete"}[abort], func(t *testing.T) {
			e := setup(t)
			transcriber := &finalizingTranscriber{finalized: make(chan struct{})}
			e.service.ASR = transcriber
			session, old, next := model.NewID(), model.NewID(), model.NewID()
			base := "/api/v1/sessions/" + session
			e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "cancel finalization"}, 201)
			e.request(t, "POST", base+"/segments", map[string]any{"id": old, "revision": 1, "text": "original draft"}, 201)
			e.request(t, "POST", base+"/segments", map[string]any{"id": next, "revision": 2}, 201)
			request := httptest.NewRequest("POST", base+"/segments/"+next+"/audio?revision=3&replace=1&defer_commit=1", strings.NewReader("pcm"))
			request.Header.Set("Content-Type", "audio/pcm")
			request.Header.Set("Authorization", "Bearer device-test")
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { New(e.service).ServeHTTP(response, request); close(done) }()
			select {
			case <-transcriber.finalized:
			case <-time.After(time.Second):
				t.Fatal("finalization not reached")
			}
			if abort {
				e.request(t, "POST", base+"/segments/"+next+"/abort", nil, 200)
			} else {
				current, err := e.service.Store.Session(context.Background(), session)
				if err != nil {
					t.Fatal(err)
				}
				e.request(t, "DELETE", base+"/segments/"+next, map[string]any{"revision": current.Revision}, 200)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("capture did not cancel")
			}
			if response.Code != 502 {
				t.Fatalf("cancelled audio=%d %s", response.Code, response.Body.String())
			}
			text, _, err := e.service.Store.DraftText(context.Background(), session)
			if err != nil || text != "original draft" {
				t.Fatal(text, err)
			}
			var result struct{ Status string }
			_ = json.Unmarshal(e.request(t, "POST", base+"/segments/"+next+"/abort", nil, 200), &result)
			if result.Status != "cancelled" {
				t.Fatal(result)
			}
		})
	}
}

func TestFullDraftDeviceReplacement(t *testing.T) {
	e := setup(t)
	session, id := model.NewID(), model.NewID()
	base := "/api/v1/sessions/" + session
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": session, "title": "full replacement"}, 201)
	revision := int64(1)
	for index := 0; index < model.MaxSegments; index++ {
		var err error
		revision, err = e.service.Store.AddSegment(context.Background(), session, model.NewID(), "old part", revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	body := map[string]any{"id": id, "revision": revision, "replace": true}
	var reserved struct{ Revision int64 }
	if err := json.Unmarshal(e.request(t, "POST", base+"/segments", body, 201), &reserved); err != nil {
		t.Fatal(err)
	}
	e.request(t, "POST", base+"/segments", body, 201)
	e.request(t, "POST", base+"/segments", map[string]any{"id": model.NewID(), "revision": reserved.Revision, "replace": true, "text": "accidental text"}, 400)
	request := httptest.NewRequest("POST", base+"/segments/"+id+"/audio?revision="+strconv.FormatInt(reserved.Revision, 10)+"&replace=1&defer_commit=1", strings.NewReader(strings.Repeat("\x00", 6400)))
	request.Header.Set("Content-Type", "audio/pcm")
	request.Header.Set("Authorization", "Bearer device-test")
	response := httptest.NewRecorder()
	New(e.service).ServeHTTP(response, request)
	var ready struct{ Revision int64 }
	if err := json.Unmarshal(response.Body.Bytes(), &ready); err != nil || response.Code != 200 {
		t.Fatalf("audio %d %s %v", response.Code, response.Body, err)
	}
	e.request(t, "POST", base+"/segments/"+id+"/commit", map[string]any{"revision": ready.Revision}, 200)
	text, _, err := e.service.Store.DraftText(context.Background(), session)
	if err != nil || text != "测试转写" {
		t.Fatalf("replacement failed %v", err)
	}
}
