package dsp

import (
	"math"
	"math/cmplx"
)

// Analog pole frequencies of the A-weighting curve, in Hz (IEC 61672-1).
const (
	aPole1 = 20.598997
	aPole2 = 107.65265
	aPole3 = 737.86223
	aPole4 = 12194.217
)

// NewAWeighting returns an A-weighting filter for sample rate fs.
//
// The analog filter is
//
//	H(s) = k * s^4 / ((s + w1)^2 (s + w2)(s + w3)(s + w4)^2)
//
// It is split into three sections: s^2/(s+w1)^2, s^2/((s+w2)(s+w3)), and
// 1/(s+w4)^2. Each section goes through the bilinear transform without
// prewarping. The overall gain is then set to 0 dB at 1 kHz.
//
// At 48 kHz the response is within 0.05 dB of the analog curve up to 4 kHz.
// Above that the bilinear transform pulls the response down: -0.54 dB at
// 8 kHz and -6.43 dB at 16 kHz. Both are inside the IEC 61672-1 Class 1 limits.
func NewAWeighting(fs float64) *Cascade {
	w1 := 2 * math.Pi * aPole1
	w2 := 2 * math.Pi * aPole2
	w3 := 2 * math.Pi * aPole3
	w4 := 2 * math.Pi * aPole4

	c := NewCascade(
		bilinear(1, 0, 0, 1, 2*w1, w1*w1, fs),
		bilinear(1, 0, 0, 1, w2+w3, w2*w3, fs),
		bilinear(0, 0, 1, 1, 2*w4, w4*w4, fs),
	)
	c.scale(1 / cmplx.Abs(c.Response(1000, fs)))
	return c
}
