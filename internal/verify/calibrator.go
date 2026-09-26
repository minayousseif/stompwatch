package verify

import (
	"math"
	"math/cmplx"

	"github.com/minayousseif/stompwatch/internal/dsp"
	"github.com/minayousseif/stompwatch/internal/meter"
)

// CalibratorLevel returns the AES17 level, in dBFS, of a 1 kHz calibrator
// tone in x (48 kHz samples) after the calibration correction. With a 94 dB
// SPL calibrator on the mic, this is the value for sensitivity_dbfs.
//
// The signal passes a band-pass from 500 Hz to 2 kHz (4th-order Butterworth
// high-pass and low-pass) first, so room rumble and hiss do not change the
// reading. The small loss of that band-pass at 1 kHz is added back. The
// first second is skipped while the filters settle.
func CalibratorLevel(x []float64, cal meter.Calibration) float64 {
	hp := dsp.NewButterworthHighpass4(500, fs)
	lp := dsp.NewButterworthLowpass4(2000, fs)
	var sum float64
	var n int
	for i, v := range x {
		y := lp.Process(hp.Process(cal.Correct(v)))
		if i >= int(fs) {
			sum += y * y
			n++
		}
	}
	if n == 0 {
		return math.NaN()
	}
	bandGain := cmplx.Abs(hp.Response(1000, fs)) * cmplx.Abs(lp.Response(1000, fs))
	return 10*math.Log10(sum/float64(n)) + 10*math.Log10(2) - 20*math.Log10(bandGain)
}
