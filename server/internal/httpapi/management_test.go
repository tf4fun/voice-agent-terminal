package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func TestManagementShellIsPublicAndAPIsStayAuthenticated(t *testing.T) {
	e := setup(t)
	for _, path := range []string{"/", "/ui/app.css", "/ui/app.js"} {
		response, err := e.server.Client().Get(e.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || len(body) == 0 {
			t.Fatalf("asset %s: %d %v", path, response.StatusCode, err)
		}
		if strings.Contains(string(body), e.service.Config.APIKey) || strings.Contains(string(body), e.service.Config.HermesKey) {
			t.Fatal("credential embedded in public asset")
		}
		if response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing management content headers")
		}
	}
	response, err := e.server.Client().Head(e.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || len(body) != 0 {
		t.Fatal("HEAD returned page body")
	}
	for _, path := range []string{"/api/v1/sessions", "/api/v1/server"} {
		response, err := e.server.Client().Get(e.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatalf("private API %s exposed: %d", path, response.StatusCode)
		}
	}
}

func TestManagementDeleteUsesExistingSameOriginSessionFlow(t *testing.T) {
	e := setup(t)
	id := model.NewID()
	path := "/api/v1/sessions/" + id
	e.request(t, "POST", "/api/v1/sessions", map[string]string{"id": id, "title": "管理页删除测试"}, 201)
	request, err := http.NewRequest("DELETE", e.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer device-test")
	request.Header.Set("Origin", "https://unrelated.invalid")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response, err := e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("cross-origin delete status %d", response.StatusCode)
	}
	session, err := e.service.Store.Session(context.Background(), id)
	if err != nil || session.State != "ready" {
		t.Fatal("rejected delete changed session")
	}
	request, err = http.NewRequest("DELETE", e.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer device-test")
	request.Header.Set("Origin", e.server.URL)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err = e.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("same-origin delete status %d", response.StatusCode)
	}
	e.request(t, "GET", path, nil, 404)
}
