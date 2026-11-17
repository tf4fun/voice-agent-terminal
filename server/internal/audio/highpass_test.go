package audio

import (
	"testing"
)

func TestHighPassRemovesTheLowEnd(t *testing.T) {
	gain := func(hz float64) float64 {
		in := limiterTone(1, hz, -6)
		h := NewHighPass(200, limiterRate)
		var out []byte
		pcm := limiterPCM(in)
		for at := 0; at < len(pcm); at += 333 { // Odd chunks split samples.
			out = append(out, h.Process(pcm[at:min(at+333, len(pcm))])...)
		}
		got := limiterSamples(out)
		if len(got) != len(in) {
			t.Fatalf("got %d samples, want %d", len(got), len(in))
		}
		return peakDBFS(got[limiterRate/2:]) - peakDBFS(in[limiterRate/2:])
	}
	for _, c := range []struct{ hz, low, high float64 }{
		{50, -30, -22}, {100, -14, -11}, {200, -3.5, -2.5}, {500, -0.3, 0.3}, {2000, -0.1, 0.1},
	} {
		if g := gain(c.hz); g < c.low || g > c.high {
			t.Fatalf("%v Hz: %.2f dB, want %v..%v", c.hz, g, c.low, c.high)
		}
	}
	pcm := limiterPCM(limiterTone(0.1, 50, -6))
	if out := NewHighPass(0, limiterRate).Process(pcm); &out[0] != &pcm[0] {
		t.Fatal("a 0 Hz cutoff should pass audio through")
	}
}
