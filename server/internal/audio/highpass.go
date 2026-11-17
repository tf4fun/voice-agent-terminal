package audio

import (
	"encoding/binary"
	"math"
)

// HighPass is a second-order Butterworth high-pass for streaming PCM16 mono.
// The device's small speaker cannot reproduce the low end of synthesized
// speech (about a third of its energy lies below 250 Hz); removing it frees
// headroom that would otherwise only drive the amplifier and cone.
type HighPass struct {
	b0, b1, b2, a1, a2 float64
	x1, x2, y1, y2     float64
	odd                []byte
	off                bool
}

// NewHighPass filters below cutoffHz at the given sample rate; a cutoff of 0
// passes audio through unchanged.
func NewHighPass(cutoffHz float64, rate int) *HighPass {
	if cutoffHz <= 0 {
		return &HighPass{off: true}
	}
	w := 2 * math.Pi * cutoffHz / float64(rate)
	alpha := math.Sin(w) / math.Sqrt2 // Q = 1/sqrt(2)
	a0 := 1 + alpha
	return &HighPass{
		b0: (1 + math.Cos(w)) / 2 / a0, b1: -(1 + math.Cos(w)) / a0, b2: (1 + math.Cos(w)) / 2 / a0,
		a1: -2 * math.Cos(w) / a0, a2: (1 - alpha) / a0,
	}
}

// Process returns the filtered audio; a byte splitting a sample waits for the next call.
func (h *HighPass) Process(pcm []byte) []byte {
	if h.off {
		return pcm
	}
	if len(h.odd) > 0 {
		pcm = append(h.odd, pcm...)
		h.odd = nil
	}
	if len(pcm)%2 == 1 {
		h.odd = []byte{pcm[len(pcm)-1]}
		pcm = pcm[:len(pcm)-1]
	}
	out := make([]byte, 0, len(pcm))
	for i := 0; i+1 < len(pcm); i += 2 {
		x := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		y := h.b0*x + h.b1*h.x1 + h.b2*h.x2 - h.a1*h.y1 - h.a2*h.y2
		h.x2, h.x1, h.y2, h.y1 = h.x1, x, h.y1, y
		out = binary.LittleEndian.AppendUint16(out, uint16(int16(math.Round(math.Max(-32768, math.Min(32767, y))))))
	}
	return out
}
