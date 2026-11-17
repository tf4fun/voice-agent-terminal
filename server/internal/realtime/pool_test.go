package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type testEvent struct {
	Type string `json:"type"`
}

func TestPoolLifecycleReuseStaleAndDiscard(t *testing.T) {
	var connections atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connections.Add(1)
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	wsURL := "ws" + strings.TrimPrefix(upstream.URL, "http")

	pool := &Pool[testEvent]{
		Name:         "test",
		PingInterval: 20 * time.Millisecond,
		Open: func(ctx context.Context) (*Conn[testEvent], error) {
			conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
			if err != nil {
				if resp != nil {
					resp.Body.Close()
				}
				return nil, err
			}
			return NewConn[testEvent](conn, 64<<10, 8, time.Second, 80*time.Millisecond), nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { pool.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitReady := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if pool.HasIdle() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("pool did not warm up")
	}

	waitReady()
	c1, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := c1.Write(testEvent{Type: "ok"}); err != nil {
		t.Fatal(err)
	}
	pool.Release(c1, true)
	waitReady()
	if got := connections.Load(); got != 1 {
		t.Fatalf("clean release opened %d connections, want 1", got)
	}

	// An unclean release discards the session and triggers replacement.
	c2, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c2 != c1 {
		t.Fatal("expected to reuse clean warm session")
	}
	pool.Release(c2, false)
	waitReady()
	if got := connections.Load(); got < 2 {
		t.Fatalf("unclean release did not replace session: %d", got)
	}

	// Expired maxAge is rotated automatically by Run.
	before := connections.Load()
	deadline := time.Now().Add(2 * time.Second)
	for connections.Load() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if connections.Load() == before {
		t.Fatal("stale session was not rotated")
	}
}

func TestPoolKeepsCheckedOutSessionAlive(t *testing.T) {
	// A silent upstream that only answers pings: without pings a checked-out
	// session would hit its read deadline while the caller streams audio.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	wsURL := "ws" + strings.TrimPrefix(upstream.URL, "http")
	pool := &Pool[testEvent]{
		Name:         "test",
		PingInterval: 20 * time.Millisecond,
		Open: func(ctx context.Context) (*Conn[testEvent], error) {
			conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
			if err != nil {
				return nil, err
			}
			return NewConn[testEvent](conn, 64<<10, 8, 100*time.Millisecond, time.Minute), nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { pool.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	c, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release(c, false)
	select {
	case <-c.Done:
		t.Fatal("checked-out session hit its read deadline")
	case <-time.After(500 * time.Millisecond):
	}
}
