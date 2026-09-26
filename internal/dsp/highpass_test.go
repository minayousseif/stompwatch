package dsp

import (
	"math"
	"testing"
)

// Expected values for a 4th-order Butterworth high-pass at fc:
// attenuation = 10*log10(1 + (fc/f)^8). At fc/4 that is 48.2 dB.
func TestButterworthHighpass4Response(t *testing.T) {
	tests := []struct {
		fc, freq float64
		min, max float64
	}{
		{20, 5, math.Inf(-1), -48.0},
		{20, 20, -3.11, -2.91},
		{20, 100, -0.05, 0.05},
		{500, 125, math.Inf(-1), -48.0},
		{500, 250, -24.4, -23.8},
		{500, 500, -3.11, -2.91},
		{500, 2500, -0.05, 0.05},
	}
	for _, tc := range tests {
		got := sineGainDB(t, NewButterworthHighpass4(tc.fc, testFS), tc.freq)
		if got < tc.min || got > tc.max {
			t.Errorf("high-pass %g Hz at %g Hz = %.2f dB, want between %.2f and %.2f dB",
				tc.fc, tc.freq, got, tc.min, tc.max)
		}
	}
}
