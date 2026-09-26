package dsp

import (
	"math"
	"testing"
)

// Expected values for a 4th-order Butterworth low-pass at fc:
// attenuation = 10*log10(1 + (f/fc)^8), with f warped by the bilinear
// transform. At 1 kHz that is 24.1 dB, at 2 kHz 48.4 dB.
func TestButterworthLowpass4Response(t *testing.T) {
	tests := []struct {
		freq     float64
		min, max float64
	}{
		{100, -0.05, 0.05},
		{500, -3.11, -2.91},
		{1000, -24.4, -23.8},
		{2000, math.Inf(-1), -48.0},
	}
	for _, tc := range tests {
		got := sineGainDB(t, NewButterworthLowpass4(500, testFS), tc.freq)
		if got < tc.min || got > tc.max {
			t.Errorf("low-pass at %g Hz = %.2f dB, want between %.2f and %.2f dB",
				tc.freq, got, tc.min, tc.max)
		}
	}
}

// A Butterworth response has no peak. Wrong Q values give a bump near the
// cutoff that the point checks above can miss.
func TestButterworthLowpass4HasNoPassbandPeak(t *testing.T) {
	for f := 200.0; f <= 500; f += 25 {
		got := sineGainDB(t, NewButterworthLowpass4(500, testFS), f)
		if got > 0.01 {
			t.Errorf("low-pass at %g Hz = %.3f dB, want <= 0 dB", f, got)
		}
	}
}
