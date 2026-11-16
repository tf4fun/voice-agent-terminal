package audio

import (
	"encoding/binary"
	"math"
	"testing"
)

func tone(hz float64, samples int, rate float64) []byte {
	out := make([]byte, 0, samples*2)
	for i := 0; i < samples; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*hz*float64(i)/rate))
		out = binary.LittleEndian.AppendUint16(out, uint16(v))
	}
	return out
}

func rms(pcm []byte) float64 {
	sum := 0.0
	n := len(pcm) / 2
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
		sum += v * v
	}
	return math.Sqrt(sum / float64(n))
}

func TestDownsamplerKeepsSpeechBandAndRemovesAliases(t *testing.T) {
	// Feed odd-sized chunks to exercise sample splitting and streaming state.
	run := func(hz float64) (float64, int) {
		d := NewDownsampler()
		input := tone(hz, 24000, 24000)
		var out []byte
		for i := 0; i < len(input); i += 777 {
			out = append(out, d.Write(input[i:min(i+777, len(input))])...)
		}
		out = append(out, d.Flush()...)
		return rms(out[2000 : len(out)-2000]), len(out) / 2
	}
	pass, samples := run(1000)
	if samples < 15990 || samples > 16010 {
		t.Fatalf("1 s at 24 kHz gave %d samples at 16 kHz", samples)
	}
	if pass < 12000/math.Sqrt2*0.95 {
		t.Fatalf("1 kHz attenuated: rms %.0f", pass)
	}
	// 11 kHz would alias to 5 kHz without filtering.
	if stop, _ := run(11000); stop > 12000/math.Sqrt2*0.03 {
		t.Fatalf("11 kHz leaked as an alias: rms %.0f", stop)
	}
}
