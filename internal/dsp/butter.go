package dsp

import "math"

// butterworthQ returns the Q of section k (1-based) of an even-order
// Butterworth filter: Q = 1 / (2*cos((2k-1)*pi / 2N)). For N = 4 that gives
// Q = 0.54120 and Q = 1.30656.
func butterworthQ(order, k int) float64 {
	return 1 / (2 * math.Cos(float64(2*k-1)*math.Pi/float64(2*order)))
}

// NewButterworthLowpass4 returns a 4th-order Butterworth low-pass filter with
// its -3 dB point at fc, for sample rate fs. The cutoff is prewarped so that
// -3 dB falls exactly at fc.
func NewButterworthLowpass4(fc, fs float64) *Cascade {
	return butterworth4(fc, fs, false)
}

// NewButterworthHighpass4 returns a 4th-order Butterworth high-pass filter
// with its -3 dB point at fc, for sample rate fs. The cutoff is prewarped so
// that -3 dB falls exactly at fc.
func NewButterworthHighpass4(fc, fs float64) *Cascade {
	return butterworth4(fc, fs, true)
}

func butterworth4(fc, fs float64, highpass bool) *Cascade {
	const order = 4
	w0 := 2 * fs * math.Tan(math.Pi*fc/fs)
	sections := make([]Biquad, 0, order/2)
	for k := 1; k <= order/2; k++ {
		q := butterworthQ(order, k)
		if highpass {
			sections = append(sections, bilinear(1, 0, 0, 1, w0/q, w0*w0, fs))
		} else {
			sections = append(sections, bilinear(0, 0, w0*w0, 1, w0/q, w0*w0, fs))
		}
	}
	return NewCascade(sections...)
}
