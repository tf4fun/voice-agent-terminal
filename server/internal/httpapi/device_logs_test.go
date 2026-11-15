package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestDeviceReportUploadListAndRender(t *testing.T) {
	e := setup(t)
	fixture, err := os.ReadFile("../diag/testdata/report.json")
	if err != nil {
		t.Fatal(err)
	}
	post := func(body []byte, key string) int {
		request, _ := http.NewRequest("POST", e.server.URL+"/api/v1/device/logs", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
		response, err := e.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := post(fixture, ""); status != 401 {
		t.Fatalf("unauthenticated upload: %d", status)
	}
	if status := post([]byte(`{"trigger":"x","events":[[1]]}`), "device-test"); status != 400 {
		t.Fatalf("malformed upload: %d", status)
	}
	if status := post(bytes.Repeat([]byte(" "), maxDeviceReportBytes+1), "device-test"); status != 413 {
		t.Fatalf("oversized upload: %d", status)
	}
	if status := post(fixture, "device-test"); status != 201 {
		t.Fatalf("upload: %d", status)
	}
	var page struct {
		Items []struct {
			ID, Trigger, Summary string
		} `json:"items"`
	}
	if err := json.Unmarshal(e.request(t, "GET", "/api/v1/device/logs?limit=5", nil, 200), &page); err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	item := page.Items[0]
	if item.Trigger != "send_after_failure" || !strings.Contains(item.Summary, "saved=0") {
		t.Fatal(item)
	}
	text := string(e.request(t, "GET", "/api/v1/device/logs/"+item.ID, nil, 200))
	if !strings.Contains(text, "发送草稿（修订 191）") || !strings.Contains(text, "PCM 队列已满") {
		t.Fatal(text)
	}
	request, _ := http.NewRequest("GET", e.server.URL+"/api/v1/device/logs/"+item.ID+"?format=json", nil)
	request.Header.Set("Authorization", "Bearer device-test")
	response, err := e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !bytes.Equal(raw, fixture) {
		t.Fatal("raw report changed")
	}
	e.request(t, "GET", "/api/v1/device/logs/not-a-uuid", nil, 400)
	e.request(t, "GET", "/api/v1/device/logs/00000000-0000-4000-8000-000000000009", nil, 404)
}
