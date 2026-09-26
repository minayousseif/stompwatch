package verify

import (
	"math"
	"testing"

	"github.com/minayousseif/stompwatch/internal/meter"
)

func sine(freq, dbfs, seconds float64) []float64 {
	a := math.Pow(10, dbfs/20)
	x := make([]float64, int(seconds*fs))
	for i := range x {
		x[i] = a * math.Sin(2*math.Pi*freq*float64(i)/fs)
	}
	return x
}

// With a 94 dB SPL calibrator on the mic, the AES17 level of the 1 kHz tone
// is the value for sensitivity_dbfs.
func TestCalibratorLevelOfSineIsItsAES17Level(t *testing.T) {
	if got := CalibratorLevel(sine(1000, -12.5, 5), meter.NoCalibration{}); math.Abs(got+12.5) > 0.05 {
		t.Fatalf("CalibratorLevel = %.3f dBFS, want -12.50", got)
	}
}

// Room rumble must not change the reading. Without a band-pass, a 60 Hz sine
// at -20 dBFS would raise -12.5 dBFS to 10*log10(10^-1.25 + 10^-2) = -11.79.
func TestCalibratorLevelIgnoresLowFrequencyRumble(t *testing.T) {
	x := sine(1000, -12.5, 5)
	for i, v := range sine(60, -20, 5) {
		x[i] += v
	}
	if got := CalibratorLevel(x, meter.NoCalibration{}); math.Abs(got+12.5) > 0.1 {
		t.Fatalf("CalibratorLevel with rumble = %.3f dBFS, want -12.50 +/-0.1", got)
	}
}

// The calibration file's correction applies first: a mic that reads +1 dB
// hot gives -13.5 dBFS for a -12.5 dBFS tone.
func TestCalibratorLevelAppliesCalibration(t *testing.T) {
	cal, err := meter.NewScalarCalibration([]meter.CalPoint{{FreqHz: 1000, MagDB: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := CalibratorLevel(sine(1000, -12.5, 5), cal); math.Abs(got+13.5) > 0.05 {
		t.Fatalf("CalibratorLevel = %.3f dBFS, want -13.50", got)
	}
}
