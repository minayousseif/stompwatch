// Package verify measures the DSP with generated signals and reports whether
// each measurement is inside its limits. "stompwatch verify-dsp" prints the
// report, so the measurement chain can be checked again on the real hardware
// after any change (SPEC.md section 11).
package verify

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/minayousseif/stompwatch/internal/audio"
	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/dsp"
	"github.com/minayousseif/stompwatch/internal/meter"
)

const fs = 48000.0

// newClipFilter builds a clip filter for a cutoff that config.Validate has
// already accepted, so a refusal here is a programming fault and not
// something the report can describe.
func newClipFilter(cutoff float64) *dsp.ClipFilter {
	f, err := dsp.NewClipFilter(cutoff)
	if err != nil {
		panic(err)
	}
	return f
}

// Check is one measurement and its limits.
type Check struct {
	Name     string
	Got      float64
	Min, Max float64
	Unit     string
}

// Pass reports whether Got is inside [Min, Max].
func (c Check) Pass() bool {
	return !math.IsNaN(c.Got) && c.Got >= c.Min && c.Got <= c.Max
}

// noLimit stands for "no lower limit" in a check.
const noLimit = -1e9

// Run measures the filters, the meter with the configured sensitivity and
// calibration, the time weighting, and the sample decoder.
func Run(s config.Config, cal meter.Calibration) []Check {
	var checks []Check
	add := func(name string, got, min, max float64, unit string) {
		checks = append(checks, Check{Name: name, Got: got, Min: min, Max: max, Unit: unit})
	}

	// A-weighting against IEC 61672-1 table 3. +/-0.5 dB to 4 kHz, Class 1
	// limits at 8 kHz and 16 kHz (SPEC.md section 15 decision 2).
	for _, r := range []struct{ freq, nominal, minus, plus float64 }{
		{31.5, -39.4, 0.5, 0.5}, {63, -26.2, 0.5, 0.5}, {125, -16.1, 0.5, 0.5},
		{250, -8.6, 0.5, 0.5}, {500, -3.2, 0.5, 0.5}, {1000, 0, 0.5, 0.5},
		{2000, 1.2, 0.5, 0.5}, {4000, 1.0, 0.5, 0.5}, {8000, -1.1, 3.1, 2.1},
		{16000, -6.6, 17.0, 3.5},
	} {
		got := sineGainDB(dsp.NewAWeighting(fs), r.freq)
		add("A-weighting at "+strconv.FormatFloat(r.freq, 'f', -1, 64)+" Hz", got, r.nominal-r.minus, r.nominal+r.plus, "dB")
	}
	add("A-weighting at 1 kHz is 0 dB", sineGainDB(dsp.NewAWeighting(fs), 1000), -0.02, 0.02, "dB")

	// The clip filter, at the cutoff clip_lowpass_hz sets. Every name says
	// which cutoff was measured, so a report cannot be read as proof about
	// a setting the box is not running (SPEC.md section 15 decision 18).
	cut := s.ClipLowpassHz
	add("clip low-pass at "+hz(cut), sineGainDB(dsp.NewButterworthLowpass4(cut, fs), cut), -3.11, -2.91, "dB")
	// The 4th-order slope, two octaves above the cutoff. It is left out when
	// two octaves reach past 20 kHz, because there is no room to measure it.
	if 4*cut <= 20000 {
		add("clip low-pass at "+hz(4*cut), sineGainDB(dsp.NewButterworthLowpass4(cut, fs), 4*cut), noLimit, -48, "dB")
	}

	// The band levels. These are measurement bands and have nothing to do
	// with the clip filter, so 500 Hz here does not follow the cutoff.
	add("high band at 500 Hz", sineGainDB(dsp.NewButterworthHighpass4(500, fs), 500), -3.11, -2.91, "dB")
	add("impact band at 20 Hz", sineGainDB(dsp.NewImpactBand(fs), 20), -3.2, -2.8, "dB")
	add("impact band at 120 Hz", sineGainDB(dsp.NewImpactBand(fs), 120), -3.2, -2.8, "dB")

	// The full clip path: nothing above the cutoff survives (SPEC.md section 3.2),
	// including the frequencies that alias back into the passband.
	worstStop := math.Inf(-1)
	for _, f := range clipStopFrequencies(cut) {
		worstStop = math.Max(worstStop, clipGainDB(cut, f))
	}
	add("clip filter rejection above "+hz(cut), worstStop, noLimit, -90, "dB")
	worstPass := 0.0
	for _, f := range clipPassFrequencies(cut) {
		worstPass = math.Max(worstPass, math.Abs(clipGainDB(cut, f)))
	}
	add("clip filter passband to "+hz(cut/2), worstPass, 0, 0.5, "dB from flat")
	add("clip filter delay aligns output", clipDelayError(cut), 0, 0.0125, "of full scale")

	// The meter with the live settings.
	calGain := calibrationGainDB(cal)
	add("sine at sensitivity level", meterLAeq(s.SensitivityDBFS, cal, s.SensitivityDBFS, 1000), 94+calGain-0.05, 94+calGain+0.05, "dB SPL")
	fullScale := 94 - s.SensitivityDBFS + calGain
	add("full-scale sine", meterLAeq(s.SensitivityDBFS, cal, 0, 1000), fullScale-0.05, fullScale+0.05, "dB SPL")
	add("level linearity over 20 dB",
		meterLAeq(-13, meter.NoCalibration{}, -20, 1000)-meterLAeq(-13, meter.NoCalibration{}, -40, 1000), 19.99, 20.01, "dB")
	burst := 94 + calGain + 10*math.Log10(1-math.Exp(-0.010/0.125))
	add("Fast LAmax of a 10 ms burst", meterBurstLAmax(s.SensitivityDBFS, cal), burst-0.2, burst+0.2, "dB SPL")

	add("Fast decay after 1 s", fastDecayDB(), -34.79, -34.69, "dB")
	add("envelope of a 60 Hz sine", envelopeMean(), 0.6316, 0.6416, "of full scale")

	// Sample decoding at the sign boundary (SPEC.md section 6.1).
	for _, d := range []struct {
		name string
		b    []byte
		want float64
	}{
		{"S24_3LE 0x7FFFFF", []byte{0xFF, 0xFF, 0x7F}, 8388607.0 / 8388608},
		{"S24_3LE 0x800000", []byte{0x00, 0x00, 0x80}, -1},
		{"S24_3LE 0xFFFFFF", []byte{0xFF, 0xFF, 0xFF}, -1.0 / 8388608},
	} {
		out := make([]float64, 1)
		audio.DecodeS24LE3(d.b, 1, 0, out)
		add(d.name, out[0], d.want, d.want, "of full scale")
	}
	return checks
}

// Report writes one line per check and a summary, and returns the number of
// checks that failed.
func Report(w io.Writer, checks []Check) int {
	failed := 0
	for _, c := range checks {
		status := "PASS"
		if !c.Pass() {
			status = "FAIL"
			failed++
		}
		limits := fmt.Sprintf("%.6g to %.6g", c.Min, c.Max)
		if c.Min <= noLimit {
			limits = fmt.Sprintf("at most %.6g", c.Max)
		}
		fmt.Fprintf(w, "%s  %-38s %12.6g %s  (want %s)\n", status, c.Name, c.Got, c.Unit, limits)
	}
	if failed == 0 {
		fmt.Fprintf(w, "All %d checks passed.\n", len(checks))
	} else {
		fmt.Fprintf(w, "%d of %d checks failed.\n", failed, len(checks))
	}
	return failed
}

type processor interface{ Process(float64) float64 }

// sineGainDB measures the steady-state gain of p for a unit sine at freq
// over a whole number of cycles after one second of settling.
func sineGainDB(p processor, freq float64) float64 {
	settle := int(fs)
	n := int(math.Round(math.Ceil(freq) * fs / freq))
	w := 2 * math.Pi * freq / fs
	var in2, out2 float64
	for i := 0; i < settle+n; i++ {
		x := math.Sin(w * float64(i))
		y := p.Process(x)
		if i >= settle {
			in2 += x * x
			out2 += y * y
		}
	}
	return 10 * math.Log10(out2/in2)
}

// hz writes a frequency the way the report names one.
func hz(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) + " Hz" }

// clipStopFrequencies returns the frequencies that prove the stop band of a
// clip filter at cutoff. The first group is fixed multiples of the cutoff,
// so every setting meets the same aliases. The second group is fixed
// frequencies near the top of the band, so a wide cutoff is still measured
// up to 24 kHz and not only just above itself. Nothing above 23 900 Hz is
// used: 24 kHz is the input Nyquist frequency.
func clipStopFrequencies(cutoff float64) []float64 {
	var out []float64
	seen := make(map[float64]bool)
	add := func(f float64) {
		if f <= cutoff*1.01 || f > 23900 || seen[f] {
			return
		}
		seen[f] = true
		out = append(out, f)
	}
	for _, r := range []float64{1.02, 1.074, 1.226, 1.554, 1.806, 2.026, 2.5, 2.976,
		4.034, 6.666, 9.554, 16.246, 24.69, 32.022, 40.11, 47.8} {
		add(cutoff * r)
	}
	for _, f := range []float64{1013, 2017, 3333, 4777, 8123, 12345, 16011, 20055, 23900} {
		add(f)
	}
	return out
}

// clipPassFrequencies returns the frequencies the clip filter must leave
// flat. The impact band is always in the list, because that is the band the
// system measures. The rest scale with the cutoff, so a wider setting is
// checked over the wider band it claims to keep.
func clipPassFrequencies(cutoff float64) []float64 {
	var out []float64
	seen := make(map[float64]bool)
	for _, f := range []float64{20, 31.5, 63, 125, 250, cutoff / 4, cutoff / 2} {
		if f > cutoff/2 || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// clipGainDB measures output power against input power through the clip
// filter at cutoff, including aliasing.
func clipGainDB(cutoff, freq float64) float64 {
	f := newClipFilter(cutoff)
	w := 2 * math.Pi * freq / fs
	var in2, out2 float64
	var nIn, nOut int
	for i := 0; i < 6*int(fs); i++ {
		x := math.Sin(w * float64(i))
		y, ok := f.Process(x)
		if i < int(fs) {
			continue
		}
		in2 += x * x
		nIn++
		if ok {
			out2 += y * y
			nOut++
		}
	}
	if out2 == 0 {
		return -400
	}
	return 10 * math.Log10((out2/float64(nOut))/(in2/float64(nIn)))
}

// clipDelayError returns the largest difference between the clip output and
// a 10 Hz input shifted by the filter's reported delay.
func clipDelayError(cutoff float64) float64 {
	f := newClipFilter(cutoff)
	d := f.Delay().Seconds()
	worst := 0.0
	for i := 0; i < 5*int(fs); i++ {
		x := math.Sin(2 * math.Pi * 10 * float64(i) / fs)
		y, ok := f.Process(x)
		if ok && i >= int(fs) {
			worst = math.Max(worst, math.Abs(y-math.Sin(2*math.Pi*10*(float64(i)/fs-d))))
		}
	}
	return worst
}

// calibrationGainDB measures what the calibration does to a 1 kHz sine.
func calibrationGainDB(cal meter.Calibration) float64 {
	var in2, out2 float64
	for i := 0; i < int(fs); i++ {
		x := math.Sin(2 * math.Pi * 1000 * float64(i) / fs)
		y := cal.Correct(x)
		in2 += x * x
		out2 += y * y
	}
	return 10 * math.Log10(out2/in2)
}

var start = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

func newMeter(sens float64, cal meter.Calibration, bins *[]meter.Bin) *meter.Meter {
	m, err := meter.New(meter.Config{
		SampleRate: meter.SampleRate, SensitivityDBFS: sens, Calibration: cal,
		OnBin: func(b meter.Bin) { *bins = append(*bins, b) },
	})
	if err != nil {
		panic(err) // the arguments are fixed and valid
	}
	return m
}

// meterLAeq returns the LAeq of the second bin for a sine at dbfs (AES17).
func meterLAeq(sens float64, cal meter.Calibration, dbfs, freq float64) float64 {
	var bins []meter.Bin
	m := newMeter(sens, cal, &bins)
	a := math.Pow(10, dbfs/20)
	x := make([]float64, 3*int(fs))
	for i := range x {
		x[i] = a * math.Sin(2*math.Pi*freq*float64(i)/fs)
	}
	m.Process(x, start)
	m.Flush()
	return bins[1].LAeq
}

// meterBurstLAmax returns LAmax for a 10 ms 1 kHz burst at the sensitivity
// level in a silent second.
func meterBurstLAmax(sens float64, cal meter.Calibration) float64 {
	var bins []meter.Bin
	m := newMeter(sens, cal, &bins)
	a := math.Pow(10, sens/20)
	x := make([]float64, 3*int(fs))
	for k := 0; k < int(fs/100); k++ {
		x[int(1.5*fs)+k] = a * math.Sin(2*math.Pi*1000*float64(k)/fs)
	}
	m.Process(x, start)
	m.Flush()
	return bins[1].LAmax
}

func fastDecayDB() float64 {
	w := dsp.NewTimeWeighting(0.125, fs)
	for i := 0; i < 3*int(fs); i++ {
		w.Process(1)
	}
	var y float64
	for i := 0; i < int(fs); i++ {
		y = w.Process(0)
	}
	return 10 * math.Log10(y)
}

// envelopeMean returns the mean envelope of a unit 60 Hz sine, which should
// be the rectified mean 2/pi = 0.6366.
func envelopeMean() float64 {
	e := dsp.NewEnvelope(fs)
	var sum float64
	var n int
	for i := 0; i < 4*int(fs); i++ {
		y, ok := e.Process(math.Sin(2 * math.Pi * 60 * float64(i) / fs))
		if ok && i >= 2*int(fs) {
			sum += y
			n++
		}
	}
	return sum / float64(n)
}
