package dsp

import (
	"math"
	"testing"
)

// Input: 400 samples, 1 at every 40th sample (n = 0, 40, ... 360), 0 elsewhere.
// Mean u = 0.025. sum(x-u)^2 = 10 - 400*u^2 = 9.75.
//
// Lag 40: sumx[n]x[n+40] = 9, sumx[n] = 9, sumx[n+40] = 9 over n < 360, so the sum
// is 9 - u*18 + 360*u^2 = 8.775 and r = 8.775 / 9.75 = 0.9.
//
// Lag 20: sumx[n]x[n+20] = 0, sumx[n] = 10, sumx[n+20] = 9 over n < 380, so the sum
// is -u*19 + 380*u^2 = -0.2375 and r = -0.2375 / 9.75 = -0.024359.
func TestAutocorrelationOfImpulseTrain(t *testing.T) {
	x := make([]float64, 400)
	for i := 0; i < 400; i += 40 {
		x[i] = 1
	}
	out := make([]float64, 60)
	Autocorrelation(x, out)

	for _, tc := range []struct {
		lag  int
		want float64
	}{
		{0, 1},
		{20, -0.024359},
		{40, 0.9},
	} {
		if math.Abs(out[tc.lag]-tc.want) > 1e-5 {
			t.Errorf("r[%d] = %.6f, want %.6f", tc.lag, out[tc.lag], tc.want)
		}
	}
}

// A constant envelope has no variance. The result must be zeros, not NaN,
// so the classifier sees "no cadence".
func TestAutocorrelationOfConstantIsZero(t *testing.T) {
	x := []float64{3, 3, 3, 3, 3, 3}
	out := []float64{9, 9, 9}
	Autocorrelation(x, out)
	for k, v := range out {
		if v != 0 {
			t.Fatalf("r[%d] = %v, want 0", k, v)
		}
	}
}

// Lags at or beyond the input length have no overlapping samples.
func TestAutocorrelationLagsBeyondInputAreZero(t *testing.T) {
	x := []float64{1, 0, 1, 0}
	out := make([]float64, 6)
	for i := range out {
		out[i] = 9
	}
	Autocorrelation(x, out)
	for k := 4; k < 6; k++ {
		if out[k] != 0 {
			t.Fatalf("r[%d] = %v, want 0", k, out[k])
		}
	}
}
