// Package realtime maintains a single warm WebSocket session with ping/pong
// health checks, age rotation and exponential reconnect backoff for an OpenAI
// realtime protocol adapter.
package realtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	DefaultPingInterval = 20 * time.Second
	DefaultPongWait     = 50 * time.Second
	DefaultMaxAge       = 50 * time.Minute
	DefaultOpenTimeout  = 15 * time.Second
	DefaultWriteTimeout = 15 * time.Second
	DefaultPingTimeout  = 5 * time.Second
)

// Conn owns one WebSocket connection and its dedicated reader goroutine.
// Writes and control pings are serialized by writeMu so concurrent callers
// never violate gorilla/websocket's single-writer rule.
type Conn[E any] struct {
	connection *websocket.Conn
	Events     chan E
	Done       chan struct{}
	stop       chan struct{}
	created    time.Time
	maxAge     time.Duration
	pongWait   time.Duration
	closeOnce  sync.Once
	writeMu    sync.Mutex
}

// NewConn starts reading JSON events from connection until an error occurs or
// Close is called.
func NewConn[E any](connection *websocket.Conn, readLimit int64, eventBuffer int, pongWait, maxAge time.Duration) *Conn[E] {
	if pongWait <= 0 {
		pongWait = DefaultPongWait
	}
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	c := &Conn[E]{
		connection: connection,
		Events:     make(chan E, eventBuffer),
		Done:       make(chan struct{}),
		stop:       make(chan struct{}),
		created:    time.Now(),
		maxAge:     maxAge,
		pongWait:   pongWait,
	}
	connection.SetReadLimit(readLimit)
	_ = connection.SetReadDeadline(time.Now().Add(pongWait))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(pongWait))
	})
	go c.read()
	return c
}

func (c *Conn[E]) read() {
	defer close(c.Done)
	defer close(c.Events)
	for {
		var event E
		if err := c.connection.ReadJSON(&event); err != nil {
			return
		}
		_ = c.connection.SetReadDeadline(time.Now().Add(c.pongWait))
		select {
		case c.Events <- event:
		case <-c.stop:
			return
		}
	}
}

func (c *Conn[E]) Close() error {
	c.closeOnce.Do(func() {
		close(c.stop)
		_ = c.connection.Close()
	})
	return nil
}

func (c *Conn[E]) Healthy() bool {
	select {
	case <-c.Done:
		return false
	default:
		return time.Since(c.created) < c.maxAge
	}
}

func (c *Conn[E]) Ping() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(DefaultPingTimeout))
}

func (c *Conn[E]) Write(value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.connection.SetWriteDeadline(time.Now().Add(DefaultWriteTimeout))
	return c.connection.WriteJSON(value)
}

// Pool keeps at most one configured Conn warm and opens extra connections on
// demand when concurrent callers arrive.
type Pool[E any] struct {
	Name         string
	Open         func(context.Context) (*Conn[E], error)
	Log          *slog.Logger
	PingInterval time.Duration
	OpenTimeout  time.Duration

	mu      sync.Mutex
	idle    *Conn[E]
	leased  map[*Conn[E]]struct{} // Checked-out sessions; Run pings them too.
	running bool
	wake    chan struct{}
}

func (p *Pool[E]) pingInterval() time.Duration {
	if p.PingInterval > 0 {
		return p.PingInterval
	}
	return DefaultPingInterval
}

func (p *Pool[E]) openTimeout() time.Duration {
	if p.OpenTimeout > 0 {
		return p.OpenTimeout
	}
	return DefaultOpenTimeout
}

func (p *Pool[E]) signal() {
	p.mu.Lock()
	wake := p.wake
	p.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// HasIdle reports whether a healthy warm session is currently waiting in the pool.
func (p *Pool[E]) HasIdle() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.idle != nil && p.idle.Healthy()
}

// Acquire checks out the warm session or, when none is ready, opens one.
func (p *Pool[E]) Acquire(ctx context.Context) (*Conn[E], error) {
	p.mu.Lock()
	s := p.idle
	p.idle = nil
	if s != nil && s.Healthy() {
		p.lease(s)
		p.mu.Unlock()
		return s, nil
	}
	p.mu.Unlock()
	if s != nil {
		_ = s.Close()
		p.signal()
	}
	openCtx, cancel := context.WithTimeout(ctx, p.openTimeout())
	defer cancel()
	fresh, err := p.Open(openCtx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.lease(fresh)
	p.mu.Unlock()
	return fresh, nil
}

// lease records s as checked out; p.mu must be held.
func (p *Pool[E]) lease(s *Conn[E]) {
	if p.leased == nil {
		p.leased = make(map[*Conn[E]]struct{})
	}
	p.leased[s] = struct{}{}
}

// Release checks a session back in: a clean, healthy one becomes the warm
// session, anything else is closed and Run opens a replacement.
func (p *Pool[E]) Release(s *Conn[E], clean bool) {
	if s == nil {
		return
	}
	p.mu.Lock()
	delete(p.leased, s)
	keep := clean && s.Healthy() && p.running && p.idle == nil
	if keep {
		p.idle = s
	}
	p.mu.Unlock()
	if !keep {
		_ = s.Close()
	}
	p.signal()
}

// Run keeps one configured session warm until ctx ends: it reconnects with
// backoff, pings the idle and checked-out sessions and replaces the idle one
// before its maximum age. A checked-out session needs pings as well: upstream
// may send no events for longer than the read deadline while audio streams in.
func (p *Pool[E]) Run(ctx context.Context) {
	p.mu.Lock()
	p.running = true
	if p.wake == nil {
		p.wake = make(chan struct{}, 1)
	}
	wake := p.wake
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		s := p.idle
		p.idle = nil
		p.running = false
		p.mu.Unlock()
		if s != nil {
			_ = s.Close()
		}
	}()
	ticker := time.NewTicker(p.pingInterval())
	defer ticker.Stop()
	backoff := time.Second
	for ctx.Err() == nil {
		p.mu.Lock()
		current := p.idle
		stale := current != nil && !current.Healthy()
		if stale {
			p.idle = nil
		}
		needed := p.idle == nil && len(p.leased) == 0
		p.mu.Unlock()
		if stale {
			_ = current.Close()
		}
		if needed {
			openCtx, cancel := context.WithTimeout(ctx, p.openTimeout())
			started := time.Now()
			fresh, err := p.Open(openCtx)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				p.logWarn("warm "+p.Name+" session failed", "retry_ms", backoff.Milliseconds())
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, time.Minute)
				continue
			}
			backoff = time.Second
			p.logInfo("warm "+p.Name+" session ready", "elapsed_ms", time.Since(started).Milliseconds())
			p.mu.Lock()
			if p.idle == nil && len(p.leased) == 0 {
				p.idle, fresh = fresh, nil
			}
			p.mu.Unlock()
			if fresh != nil {
				_ = fresh.Close()
			}
		}
		var dropped <-chan struct{}
		p.mu.Lock()
		if p.idle != nil {
			dropped = p.idle.Done
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-dropped:
		case <-wake:
		case <-ticker.C:
			p.mu.Lock()
			live := make([]*Conn[E], 0, 1+len(p.leased))
			if p.idle != nil {
				live = append(live, p.idle)
			}
			for s := range p.leased {
				live = append(live, s)
			}
			p.mu.Unlock()
			for _, s := range live {
				if s.Ping() != nil {
					_ = s.Close()
				}
			}
		}
	}
}

func (p *Pool[E]) logInfo(message string, args ...any) {
	if p.Log != nil {
		p.Log.Info(message, args...)
	}
}

func (p *Pool[E]) logWarn(message string, args ...any) {
	if p.Log != nil {
		p.Log.Warn(message, args...)
	}
}
