package audio

import (
	"encoding/binary"
	"math"
)

// downsampleTaps is the half-width of the interpolation kernel in 24 kHz
// samples. Output sample n sits at input position 1.5n, so only two kernel
// phases (integer and half-integer offsets) are ever needed.
const downsampleTaps = 12

// Downsampler converts streaming 24 kHz PCM16 mono to 16 kHz with a
// Blackman-windowed sinc low-pass at 7.2 kHz, so content above the new
// Nyquist frequency is attenuated instead of folding back as aliasing.
type Downsampler struct {
	history []float64 // Input samples not yet fully consumed, with leading zero history.
	pos     float64   // Position of the next output sample within history.
	odd     []byte    // A trailing byte when a chunk split a sample.
	phases  [2][]float64
}

func NewDownsampler() *Downsampler {
	d := &Downsampler{history: make([]float64, downsampleTaps), pos: downsampleTaps}
	for phase, offset := range []float64{0, 0.5} {
		weights := make([]float64, 2*downsampleTaps+1)
		sum := 0.0
		for i := range weights {
			u := offset + float64(downsampleTaps-i) // Distance from the output position to tap i.
			weights[i] = kernel(u)
			sum += weights[i]
		}
		for i := range weights {
			weights[i] /= sum
		}
		d.phases[phase] = weights
	}
	return d
}

func kernel(u float64) float64 {
	const cutoff = 7200.0 / 24000.0 // Normalized to the 24 kHz input rate.
	x := 2 * cutoff * u
	sinc := 1.0
	if x != 0 {
		sinc = math.Sin(math.Pi*x) / (math.Pi * x)
	}
	w := (u + downsampleTaps + 1) / (2*downsampleTaps + 2) // Window position in 0..1.
	window := 0.42 - 0.5*math.Cos(2*math.Pi*w) + 0.08*math.Cos(4*math.Pi*w)
	return sinc * window
}

// Write consumes 24 kHz PCM16LE and returns the 16 kHz PCM16LE produced so far.
func (d *Downsampler) Write(pcm []byte) []byte {
	if len(d.odd) > 0 {
		pcm = append(append([]byte{}, d.odd...), pcm...)
		d.odd = nil
	}
	if len(pcm)%2 == 1 {
		d.odd = []byte{pcm[len(pcm)-1]}
		pcm = pcm[:len(pcm)-1]
	}
	for i := 0; i+1 < len(pcm); i += 2 {
		d.history = append(d.history, float64(int16(binary.LittleEndian.Uint16(pcm[i:]))))
	}
	return d.drain()
}

// Flush pads the tail with silence so the last input samples are emitted.
func (d *Downsampler) Flush() []byte {
	d.odd = nil
	for i := 0; i < downsampleTaps+2; i++ {
		d.history = append(d.history, 0)
	}
	out := d.drain()
	d.history = make([]float64, downsampleTaps)
	d.pos = downsampleTaps
	return out
}

func (d *Downsampler) drain() []byte {
	var out []byte
	for d.pos+downsampleTaps < float64(len(d.history)) {
		center := int(d.pos)
		weights := d.phases[0]
		if d.pos != float64(center) {
			weights = d.phases[1]
		}
		sum := 0.0
		for i, w := range weights {
			sum += w * d.history[center-downsampleTaps+i]
		}
		sample := int16(math.Max(math.MinInt16, math.Min(math.MaxInt16, math.Round(sum))))
		out = binary.LittleEndian.AppendUint16(out, uint16(sample))
		d.pos += 1.5
	}
	if drop := int(d.pos) - downsampleTaps; drop > 0 {
		d.history = append(d.history[:0], d.history[drop:]...)
		d.pos -= float64(drop)
	}
	return out
}
