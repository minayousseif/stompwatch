package dsp

import (
	"math"
	"testing"
)

const testFS = 48000.0

type processor interface {
	Process(x float64) float64
}

// sineGainDB drives p with a unit sine at freq and returns the steady-state
// RMS gain in dB. It discards one second of settling time, then measures over
// a whole number of cycles that spans at least one second.
func sineGainDB(t *testing.T, p processor, freq float64) float64 {
	t.Helper()
	settle := int(testFS)
	cycles := math.Ceil(freq)
	n := int(math.Round(cycles * testFS / freq))
	w := 2 * math.Pi * freq / testFS
	var in2, out2 float64
	for i := 0; i < settle+n; i++ {
		x := math.Sin(w * float64(i))
		y := p.Process(x)
		if i >= settle {
			in2 += x * x
			out2 += y * y
		}
	}
	if out2 == 0 {
		return math.Inf(-1)
	}
	return 10 * math.Log10(out2/in2)
}
