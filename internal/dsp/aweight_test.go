package dsp

import (
	"math"
	"testing"
)

// Nominal A-weighting values are from IEC 61672-1, table 3. Up to 4 kHz the
// limit is +/-0.5 dB. At 8 kHz and 16 kHz the limits are the Class 1 tolerances,
// because the bilinear transform at 48 kHz cannot reach +/-0.5 dB there
// (SPEC.md section 15, decision 2).
func TestAWeightingMatchesIEC61672Table(t *testing.T) {
	tests := []struct {
		freq    float64
		nominal float64
		minus   float64
		plus    float64
	}{
		{31.5, -39.4, 0.5, 0.5},
		{63, -26.2, 0.5, 0.5},
		{125, -16.1, 0.5, 0.5},
		{250, -8.6, 0.5, 0.5},
		{500, -3.2, 0.5, 0.5},
		{1000, 0.0, 0.5, 0.5},
		{2000, 1.2, 0.5, 0.5},
		{4000, 1.0, 0.5, 0.5},
		{8000, -1.1, 3.1, 2.1},
		{16000, -6.6, 17.0, 3.5},
	}
	for _, tc := range tests {
		got := sineGainDB(t, NewAWeighting(testFS), tc.freq)
		if got < tc.nominal-tc.minus || got > tc.nominal+tc.plus {
			t.Errorf("A-weighting at %g Hz = %.2f dB, want %.1f dB (-%.1f/+%.1f)",
				tc.freq, got, tc.nominal, tc.minus, tc.plus)
		}
	}
}

// The table test allows 0.5 dB. A normalization error smaller than that would
// shift every reading, so 1 kHz gets a tight check of its own.
func TestAWeightingIsZeroDBAt1kHz(t *testing.T) {
	got := sineGainDB(t, NewAWeighting(testFS), 1000)
	if math.Abs(got) > 0.02 {
		t.Fatalf("A-weighting at 1 kHz = %.3f dB, want 0.00 +/-0.02 dB", got)
	}
}

// analogADB is the closed-form A-weighting magnitude from IEC 61672-1
// annex E, normalized to 0 dB at 1 kHz. It does not use the digital filter.
func analogADB(f float64) float64 {
	const (
		f1 = 20.598997
		f2 = 107.65265
		f3 = 737.86223
		f4 = 12194.217
	)
	r := func(f float64) float64 {
		ff := f * f
		return f4 * f4 * ff * ff /
			((ff + f1*f1) * math.Sqrt((ff+f2*f2)*(ff+f3*f3)) * (ff + f4*f4))
	}
	return 20 * math.Log10(r(f)/r(1000))
}

// Up to 4 kHz the bilinear warping error is under 0.05 dB, so the digital
// filter must follow the analog curve closely. This catches a wrong pole
// frequency that the rounded table values would still let pass.
func TestAWeightingFollowsAnalogCurveTo4kHz(t *testing.T) {
	for _, f := range []float64{20, 40, 80, 160, 315, 630, 1250, 2500, 4000} {
		got := sineGainDB(t, NewAWeighting(testFS), f)
		want := analogADB(f)
		if math.Abs(got-want) > 0.1 {
			t.Errorf("A-weighting at %g Hz = %.3f dB, analog curve = %.3f dB", f, got, want)
		}
	}
}
