package audio

import (
	"encoding/binary"
	"math"
)

// Limiter time constants at the 16 kHz speech rate.
const (
	limiterRate      = 16000
	limiterLookahead = limiterRate * 5 / 1000 // 5 ms: gain falls before a peak arrives.
	limiterRelease   = 0.06                   // Seconds for gain to recover after a peak.
)

// Limiter applies a fixed gain to streaming PCM16 mono, then holds every
// sample within a ceiling with a look-ahead peak limiter: gain is lowered
// smoothly over the 5 ms before a peak and recovers over about 60 ms after
// it, so loud syllables are turned down instead of clipped. Output lags input
// by the look-ahead; Flush returns the tail.
type Limiter struct {
	gain, ceiling   float64
	attack, release float64
	delay           []float64 // Gained samples waiting for the look-ahead.
	need            []float64 // Gain each delayed sample needs to stay within the ceiling.
	head, count     int
	window          []int // Monotonic queue of delay positions: smallest need first.
	envelope        float64
	odd             []byte // A trailing byte when a chunk split a sample.
	passthrough     bool
}

// NewLimiter takes the gain and ceiling in dB (ceiling relative to full
// scale). A 0 dB gain with a 0 dB ceiling passes audio through unchanged.
func NewLimiter(gainDB, ceilingDB float64) *Limiter {
	size := limiterLookahead + 1
	return &Limiter{
		gain:        math.Pow(10, gainDB/20),
		ceiling:     32767 * math.Pow(10, ceilingDB/20),
		attack:      1 - math.Exp(-4.0/limiterLookahead), // ~98% of the way within the look-ahead.
		release:     1 - math.Exp(-1/(limiterRelease*limiterRate)),
		delay:       make([]float64, size),
		need:        make([]float64, size),
		envelope:    1,
		passthrough: gainDB == 0 && ceilingDB >= 0,
	}
}

// Process returns the limited audio available so far.
func (l *Limiter) Process(pcm []byte) []byte {
	if l.passthrough {
		return pcm
	}
	if len(l.odd) > 0 {
		pcm = append(l.odd, pcm...)
		l.odd = nil
	}
	if len(pcm)%2 == 1 {
		l.odd = []byte{pcm[len(pcm)-1]}
		pcm = pcm[:len(pcm)-1]
	}
	out := make([]byte, 0, len(pcm))
	for i := 0; i+1 < len(pcm); i += 2 {
		out = l.push(float64(int16(binary.LittleEndian.Uint16(pcm[i:]))), out)
	}
	return out
}

// Flush returns the samples still held for the look-ahead.
func (l *Limiter) Flush() []byte {
	if l.passthrough {
		return nil
	}
	out := make([]byte, 0, 2*l.count)
	for l.count > 0 {
		out = l.emit(out)
	}
	l.odd = nil
	return out
}

func (l *Limiter) push(sample float64, out []byte) []byte {
	if l.count == len(l.delay) {
		out = l.emit(out)
	}
	x := sample * l.gain
	need := 1.0
	if magnitude := math.Abs(x); magnitude > l.ceiling {
		need = l.ceiling / magnitude
	}
	at := (l.head + l.count) % len(l.delay)
	l.delay[at], l.need[at] = x, need
	l.count++
	for len(l.window) > 0 && l.need[l.window[len(l.window)-1]] >= need {
		l.window = l.window[:len(l.window)-1]
	}
	l.window = append(l.window, at)
	return out
}

// emit outputs the oldest delayed sample at the smallest gain any sample in
// the look-ahead needs, approached smoothly.
func (l *Limiter) emit(out []byte) []byte {
	target := l.need[l.window[0]]
	if target < l.envelope {
		l.envelope += (target - l.envelope) * l.attack
	} else {
		l.envelope += (target - l.envelope) * l.release
	}
	y := l.delay[l.head] * l.envelope
	// The smoothed gain can trail a sudden peak by a sliver; clip that rest.
	y = math.Max(-l.ceiling, math.Min(l.ceiling, y))
	out = binary.LittleEndian.AppendUint16(out, uint16(int16(math.Round(y))))
	if l.window[0] == l.head {
		l.window = l.window[1:]
	}
	l.head = (l.head + 1) % len(l.delay)
	l.count--
	return out
}
