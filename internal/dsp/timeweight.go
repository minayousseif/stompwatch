package dsp

import "math"

// TimeWeighting is an exponential moving average with time constant tau, as
// defined for sound level meters in IEC 61672-1. Feed it squared samples.
// Fast weighting is tau = 0.125 s.
type TimeWeighting struct {
	alpha float64
	y     float64
}

// NewTimeWeighting returns a time weighting with time constant tau seconds
// for sample rate fs. After n samples of a unit step the output is exactly
// 1 - exp(-n / (tau*fs)).
func NewTimeWeighting(tau, fs float64) *TimeWeighting {
	return &TimeWeighting{alpha: 1 - math.Exp(-1/(tau*fs))}
}

// Process adds one sample and returns the weighted value.
func (w *TimeWeighting) Process(x float64) float64 {
	w.y += w.alpha * (x - w.y)
	return w.y
}
