package asr

import (
	"errors"
	"io"
	"sync"
)

// PrefetchBytes bounds the audio held while the upstream is slower than the
// device. It exceeds a whole 300 s 16 kHz PCM16 recording (9.6 MB), so the
// device never sees upstream backpressure; the request's MaxBytesReader stays
// the real upload limit. Chunks are allocated only as audio arrives.
const PrefetchBytes = 16 << 20

const prefetchChunk = 4 << 10

var errPrefetchClosed = errors.New("audio prefetch closed")

// prefetch drains the device upload while Transcribe opens the upstream
// session. Without it the gateway stops reading the request body during the
// handshake, TCP backpressure reaches the device and its small PCM queue
// overflows. Memory stays bounded: once the limit is buffered, reads pause and
// backpressure resumes exactly as before.
type prefetch struct {
	source   io.ReadCloser
	chunks   chan []byte
	done     chan struct{}
	finished chan struct{}
	once     sync.Once
	closeErr error
	err      error
	current  []byte
}

func newPrefetch(source io.ReadCloser, limit int) *prefetch {
	slots := limit / prefetchChunk
	if slots < 1 {
		slots = 1
	}
	p := &prefetch{
		source:   source,
		chunks:   make(chan []byte, slots),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *prefetch) run() {
	defer close(p.finished)
	// p.err is written before chunks closes, so Read observes it after the close.
	defer close(p.chunks)
	for {
		buffer := make([]byte, prefetchChunk)
		n, err := p.source.Read(buffer)
		if n > 0 {
			select {
			case p.chunks <- buffer[:n]:
			case <-p.done:
				p.err = errPrefetchClosed
				return
			}
		}
		if err != nil {
			p.err = err
			return
		}
	}
}

func (p *prefetch) Read(buffer []byte) (int, error) {
	for len(p.current) == 0 {
		chunk, ok := <-p.chunks
		if !ok {
			return 0, p.err
		}
		p.current = chunk
	}
	n := copy(buffer, p.current)
	p.current = p.current[n:]
	return n, nil
}

// Close interrupts the source and waits for the reader goroutine, so the
// request body is never read after the handler returns.
func (p *prefetch) Close() error {
	p.once.Do(func() {
		close(p.done)
		p.closeErr = p.source.Close()
	})
	<-p.finished
	return p.closeErr
}
