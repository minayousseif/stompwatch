package dsp

import (
	"math"
	"testing"
)

func TestEnvelopeOutputsOneSamplePer480Inputs(t *testing.T) {
	e := NewEnvelope(testFS)
	n := 0
	for i := 0; i < 48000; i++ {
		if _, ok := e.Process(0); ok {
			n++
		}
	}
	if n != EnvelopeRate {
		t.Fatalf("48000 inputs gave %d outputs, want %d", n, EnvelopeRate)
	}
}

// steadyEnvelope returns the mean envelope value for a unit sine at freq,
// measured over the second half of a four-second run.
func steadyEnvelope(t *testing.T, freq float64) float64 {
	t.Helper()
	e := NewEnvelope(testFS)
	var sum float64
	var n int
	for i := 0; i < 4*48000; i++ {
		y, ok := e.Process(math.Sin(2 * math.Pi * freq * float64(i) / testFS))
		if ok && i >= 2*48000 {
			sum += y
			n++
		}
	}
	return sum / float64(n)
}

// The mean of a full-wave rectified unit sine is 2/pi = 0.6366. At 60 Hz the
// 20-120 Hz band-pass loses under 0.02 dB, so the envelope must be close to it.
func TestEnvelopeOfInBandSineIsRectifiedMean(t *testing.T) {
	if got := steadyEnvelope(t, 60); math.Abs(got-0.6366) > 0.005 {
		t.Fatalf("envelope of 60 Hz sine = %.4f, want 0.6366 +/-0.005", got)
	}
}

// Rectifying a 60 Hz sine leaves a 120 Hz ripple of amplitude 4/(3pi) = 0.42.
// The 10 Hz smoothing takes it 86 dB down. At 100 Hz sampling any ripple left
// aliases to 20 Hz and looks like a cadence, so it must be tiny. A 40 Hz
// smoother would leave a ripple of about +/-0.005.
func TestEnvelopeRippleOfSteadyToneIsSmall(t *testing.T) {
	e := NewEnvelope(testFS)
	lo, hi := math.Inf(1), math.Inf(-1)
	for i := 0; i < 4*48000; i++ {
		y, ok := e.Process(math.Sin(2 * math.Pi * 60 * float64(i) / testFS))
		if ok && i >= 2*48000 {
			lo, hi = math.Min(lo, y), math.Max(hi, y)
		}
	}
	if hi-lo > 0.001 {
		t.Fatalf("envelope ripple of steady 60 Hz sine = %.5f peak to peak, want < 0.001", hi-lo)
	}
}

// Airborne sound above the band must not reach the envelope. The 120 Hz
// low-pass is 73 dB down at 1 kHz, which leaves a rectified mean near 1.4e-4.
func TestEnvelopeIgnoresSoundAboveBand(t *testing.T) {
	if got := steadyEnvelope(t, 1000); got > 0.001 {
		t.Fatalf("envelope of 1 kHz sine = %.5f, want < 0.001", got)
	}
}

// Two 50 ms bursts of 60 Hz, 0.4 s apart, must give envelope peaks 40 samples
// apart at 100 Hz. The classifier reads cadence from this spacing.
func TestEnvelopePeakSpacingMatchesBurstSpacing(t *testing.T) {
	e := NewEnvelope(testFS)
	burst := func(i, start int) float64 {
		if i >= start && i < start+2400 {
			return math.Sin(2 * math.Pi * 60 * float64(i-start) / testFS)
		}
		return 0
	}
	var env []float64
	for i := 0; i < 3*48000; i++ {
		x := burst(i, 48000) + burst(i, 48000+19200)
		if y, ok := e.Process(x); ok {
			env = append(env, y)
		}
	}
	peak := func(lo, hi int) int {
		best := lo
		for i := lo; i < hi; i++ {
			if env[i] > env[best] {
				best = i
			}
		}
		return best
	}
	p1 := peak(100, 140) // 1.00-1.40 s
	p2 := peak(140, 180) // 1.40-1.80 s
	// A flat envelope would put both "peaks" at the window starts, 40 apart.
	// Require real peaks well above the quiet level.
	for _, p := range []int{p1, p2} {
		if env[p] < 0.05 || env[p] < 10*env[50] {
			t.Fatalf("envelope at sample %d = %.4f, quiet level = %.4f; want a clear peak", p, env[p], env[50])
		}
	}
	if d := p2 - p1; d < 39 || d > 41 {
		t.Fatalf("peaks at samples %d and %d are %d apart, want 40 +/-1", p1, p2, d)
	}
}
