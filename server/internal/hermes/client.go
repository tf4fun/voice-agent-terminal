package hermes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/stream"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

type Update struct {
	Text      string
	Attention bool
	Reset     bool
}

type UpstreamError struct {
	Status    int
	Code      string
	Uncertain bool
}

func (e *UpstreamError) Error() string { return e.Code }

func (c *Client) Enabled() bool { return c.BaseURL != "" && c.APIKey != "" }

func (c *Client) Scope() string {
	digest := sha256.Sum256([]byte(strings.TrimRight(c.BaseURL, "/") + "\x00" + c.APIKey))
	return fmt.Sprintf("%x", digest)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *Client) request(ctx context.Context, method, path string, payload any) (*http.Request, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.APIKey)
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}

func (c *Client) sessionRequest(ctx context.Context, method, path string, payload any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := c.request(ctx, method, path, payload)
	if err != nil {
		return 0, err
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return 0, &UpstreamError{Code: "hermes_unavailable", Uncertain: true}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, &UpstreamError{Status: response.StatusCode, Code: "hermes_session_error"}
	}
	return response.StatusCode, nil
}

func (c *Client) CreateSession(ctx context.Context, id string) error {
	status, err := c.sessionRequest(ctx, "POST", "/api/sessions", map[string]string{"id": id, "source": "api_server"})
	if status == http.StatusConflict {
		_, err = c.ownedSession(ctx, id)
	}
	return err
}

func (c *Client) DeleteSession(ctx context.Context, id string) error {
	status, err := c.ownedSession(ctx, id)
	if status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	status, err = c.sessionRequest(ctx, "DELETE", "/api/sessions/"+url.PathEscape(id), nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *Client) ownedSession(parent context.Context, id string) (int, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	request, err := c.request(ctx, "GET", "/api/sessions/"+url.PathEscape(id), nil)
	if err != nil {
		return 0, err
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return 0, &UpstreamError{Code: "hermes_unavailable", Uncertain: true}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return response.StatusCode, &UpstreamError{Status: response.StatusCode, Code: "hermes_session_error"}
	}
	var body struct {
		Session struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"session"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&body); err != nil || body.Session.ID != id || body.Session.Source != "api_server" {
		return response.StatusCode, &UpstreamError{Code: "hermes_session_conflict"}
	}
	return response.StatusCode, nil
}

var errDone = errors.New("completion_done")

func (c *Client) Chat(ctx context.Context, message model.Message, emit func(Update) error) (string, error) {
	request, err := c.request(ctx, "POST", "/v1/chat/completions", map[string]any{
		"model": message.Model, "stream": message.Attempts == 0,
		"messages": []map[string]string{{"role": "user", "content": message.Input}},
	})
	if err != nil {
		return "", err
	}
	request.Header.Set("X-Hermes-Session-Id", message.SessionID)
	request.Header.Set("Idempotency-Key", "voice-agent-"+message.ID)
	request.Header.Set("Accept", "text/event-stream")
	if message.Attempts > 0 {
		request.Header.Set("Accept", "application/json")
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return "", &UpstreamError{Code: "hermes_connection_lost", Uncertain: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", &UpstreamError{Status: response.StatusCode, Code: fmt.Sprintf("hermes_http_%d", response.StatusCode), Uncertain: response.StatusCode >= 500}
	}
	if message.Attempts > 0 {
		return recoverReply(response.Body, emit)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return "", &UpstreamError{Code: "hermes_invalid_stream", Uncertain: true}
	}
	finish := ""
	var textFilter stream.MediaFilter
	err = stream.ReadSSE(response.Body, func(event string, payload []byte) error {
		if string(payload) == "[DONE]" {
			if finish == "" {
				return &UpstreamError{Code: "hermes_missing_finish", Uncertain: true}
			}
			if tail := textFilter.Flush(); len(tail) > 0 {
				if err := emit(Update{Text: string(tail)}); err != nil {
					return err
				}
			}
			return errDone
		}
		if event == "approval.request" {
			return emit(Update{Attention: true})
		}
		if event != "" && event != "message" {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return &UpstreamError{Code: "hermes_invalid_json", Uncertain: true}
		}
		if len(chunk.Choices) == 0 {
			if len(chunk.Error) > 0 {
				return &UpstreamError{Code: "hermes_stream_error", Uncertain: true}
			}
			return nil
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != nil {
			finish = *choice.FinishReason
		}
		text := textFilter.Write([]byte(choice.Delta.Content))
		if len(text) > 0 {
			return emit(Update{Text: string(text)})
		}
		return nil
	})
	if errors.Is(err, errDone) {
		return finish, nil
	}
	if errors.Is(err, stream.ErrEventTooLarge) {
		return "", &UpstreamError{Code: "hermes_event_too_large", Uncertain: true}
	}
	if errors.Is(err, io.EOF) {
		return "", &UpstreamError{Code: "hermes_incomplete_stream", Uncertain: true}
	}
	return "", err
}

func recoverReply(input io.Reader, emit func(Update) error) (string, error) {
	var filter stream.MediaFilter
	var body bytes.Buffer
	buffer := make([]byte, 4096)
	for {
		n, err := input.Read(buffer)
		part := filter.Write(buffer[:n])
		if body.Len()+len(part) > model.MaxReplyBytes+65536 {
			return "", &UpstreamError{Code: "hermes_event_too_large", Uncertain: true}
		}
		body.Write(part)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return "", &UpstreamError{Code: "hermes_connection_lost", Uncertain: true}
			}
			break
		}
	}
	body.Write(filter.Flush())
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body.Bytes(), &reply); err != nil || len(reply.Choices) != 1 || reply.Choices[0].Finish == "" {
		return "", &UpstreamError{Code: "hermes_invalid_reply", Uncertain: true}
	}
	if reply.Choices[0].Message.Content == "" && reply.Choices[0].Finish != "stop" {
		return reply.Choices[0].Finish, nil
	}
	if err := emit(Update{Text: reply.Choices[0].Message.Content, Reset: true}); err != nil {
		return "", err
	}
	return reply.Choices[0].Finish, nil
}
