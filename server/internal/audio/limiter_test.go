package audio

import (
	"encoding/binary"
	"math"
	"testing"
)

func limiterTone(seconds, hz, dbfs float64) []int16 {
	amplitude := 32767 * math.Pow(10, dbfs/20)
	samples := make([]int16, int(seconds*limiterRate))
	for i := range samples {
		samples[i] = int16(math.Round(amplitude * math.Sin(2*math.Pi*hz*float64(i)/limiterRate)))
	}
	return samples
}

func limiterPCM(samples []int16) []byte {
	out := make([]byte, 0, 2*len(samples))
	for _, s := range samples {
		out = binary.LittleEndian.AppendUint16(out, uint16(s))
	}
	return out
}

func limiterSamples(pcm []byte) []int16 {
	out := make([]int16, len(pcm)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	return out
}

// limitAll feeds pcm in chunks of the given byte sizes, cycling, and flushes.
func limitAll(l *Limiter, pcm []byte, chunks ...int) []int16 {
	var out []byte
	for at, i := 0, 0; at < len(pcm); i++ {
		n := min(chunks[i%len(chunks)], len(pcm)-at)
		out = append(out, l.Process(pcm[at:at+n])...)
		at += n
	}
	return limiterSamples(append(out, l.Flush()...))
}

func peakDBFS(samples []int16) float64 {
	m := 0.0
	for _, s := range samples {
		m = math.Max(m, math.Abs(float64(s)))
	}
	return 20 * math.Log10(m/32767)
}

func TestLimiterKeepsLoudSpeechUnderTheCeiling(t *testing.T) {
	in := limiterPCM(limiterTone(1, 440, -3))
	out := limitAll(NewLimiter(0, -12), in, 4096)
	if len(out) != len(in)/2 {
		t.Fatalf("got %d samples, want %d", len(out), len(in)/2)
	}
	if p := peakDBFS(out); p > -11.99 {
		t.Fatalf("peak %.2f dBFS above the -12 dBFS ceiling", p)
	}
	// Steady loud audio sits at the ceiling rather than far below it.
	if p := peakDBFS(out[limiterRate/2:]); p < -12.5 {
		t.Fatalf("steady peak %.2f dBFS, limited too hard", p)
	}
}

func TestLimiterLeavesQuietAudioAlone(t *testing.T) {
	in := limiterTone(0.5, 300, -30)
	out := limitAll(NewLimiter(6, -12), limiterPCM(in), 1000)
	for i, s := range in {
		if want := float64(s) * math.Pow(10, 6.0/20); math.Abs(float64(out[i])-want) > 1 {
			t.Fatalf("sample %d = %d, want %.0f (6 dB gain, no limiting)", i, out[i], want)
		}
	}
}

func TestLimiterRecoversAfterAPeakAndIgnoresChunking(t *testing.T) {
	quiet, loud := limiterTone(0.3, 500, -24), limiterTone(0.05, 500, 0)
	in := append(append(append([]int16{}, quiet...), loud...), quiet...)
	pcm := limiterPCM(in)
	whole := limitAll(NewLimiter(0, -12), pcm, len(pcm))
	split := limitAll(NewLimiter(0, -12), pcm, 1, 3, 77, 512) // Odd sizes split samples.
	if len(whole) != len(in) || len(split) != len(in) {
		t.Fatalf("lengths %d %d, want %d", len(whole), len(split), len(in))
	}
	for i := range whole {
		if whole[i] != split[i] {
			t.Fatalf("chunking changed sample %d: %d vs %d", i, whole[i], split[i])
		}
	}
	if p := peakDBFS(whole); p > -11.99 {
		t.Fatalf("burst peak %.2f dBFS", p)
	}
	// 250 ms after the burst the quiet tone is back at full level.
	tail := whole[len(quiet)+len(loud)+limiterRate/4:]
	if p, want := peakDBFS(tail), peakDBFS(quiet); math.Abs(p-want) > 0.2 {
		t.Fatalf("tail peak %.2f dBFS, want %.2f", p, want)
	}
}

func TestLimiterPassesThroughWhenOff(t *testing.T) {
	pcm := limiterPCM(limiterTone(0.1, 440, -1))
	l := NewLimiter(0, 0)
	if out := l.Process(pcm); &out[0] != &pcm[0] || len(l.Flush()) != 0 {
		t.Fatal("a 0 dB gain and ceiling should not touch the audio")
	}
}
