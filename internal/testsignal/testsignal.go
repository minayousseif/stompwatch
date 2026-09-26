// Package testsignal makes synthetic audio for tests. Production code must
// not import it.
package testsignal

import (
	"encoding/binary"
	"io"
	"math"
	"math/rand/v2"
)

// Rate is the sample rate of all signals, in Hz.
const Rate = 48000

// Pink returns n samples of pink noise with the given RMS level (full scale
// is 1). The same seed gives the same noise. It uses Paul Kellet's economy
// filter on Gaussian white noise, which falls about 3 dB per octave.
func Pink(n int, rms float64, seed uint64) []float64 {
	rng := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	x := make([]float64, n)
	var b0, b1, b2, sum float64
	for i := range x {
		w := rng.NormFloat64()
		b0 = 0.99765*b0 + w*0.0990460
		b1 = 0.96300*b1 + w*0.2965164
		b2 = 0.57000*b2 + w*1.0526913
		x[i] = b0 + b1 + b2 + w*0.1848
		sum += x[i] * x[i]
	}
	if sum > 0 {
		scale := rms / math.Sqrt(sum/float64(n))
		for i := range x {
			x[i] *= scale
		}
	}
	return x
}

// Thump settings: an 80 Hz tone that decays with a 40 ms time constant and
// lasts 300 ms, like a footfall through a floor.
const (
	ThumpHz       = 80.0
	thumpDecay    = 0.040
	thumpDuration = 0.3
)

// AddThump adds an impact with peak amplitude peak at time at (seconds).
func AddThump(x []float64, at, peak float64) {
	start := int(math.Round(at * Rate))
	for k := 0; k < int(thumpDuration*Rate) && start+k < len(x); k++ {
		if start+k < 0 {
			continue
		}
		t := float64(k) / Rate
		x[start+k] += peak * math.Exp(-t/thumpDecay) * math.Sin(2*math.Pi*ThumpHz*t)
	}
}

// AddThumps adds an impact at each time in seconds.
func AddThumps(x []float64, peak float64, at ...float64) {
	for _, a := range at {
		AddThump(x, a, peak)
	}
}

// WriteWAV24 writes x as a 48 kHz, 24-bit, stereo PCM WAV file with the same
// signal on both channels. Samples outside +/-1 are clipped.
func WriteWAV24(w io.Writer, x []float64) error {
	const channels, bytesPerSample = 2, 3
	le := binary.LittleEndian
	data := len(x) * channels * bytesPerSample
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	le.PutUint32(hdr[4:], uint32(36+data))
	copy(hdr[8:], "WAVEfmt ")
	le.PutUint32(hdr[16:], 16)
	le.PutUint16(hdr[20:], 1)
	le.PutUint16(hdr[22:], channels)
	le.PutUint32(hdr[24:], Rate)
	le.PutUint32(hdr[28:], Rate*channels*bytesPerSample)
	le.PutUint16(hdr[32:], channels*bytesPerSample)
	le.PutUint16(hdr[34:], 24)
	copy(hdr[36:], "data")
	le.PutUint32(hdr[40:], uint32(data))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	body := make([]byte, data)
	for i, v := range x {
		q := int32(math.Round(math.Max(-1, math.Min(1, v)) * 8388607))
		for c := 0; c < channels; c++ {
			p := body[(i*channels+c)*bytesPerSample:]
			p[0], p[1], p[2] = byte(q), byte(q>>8), byte(q>>16)
		}
	}
	_, err := w.Write(body)
	return err
}
