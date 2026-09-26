package meter

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/minayousseif/stompwatch/internal/dsp"
)

const (
	SampleRate  = 48000
	binLength   = time.Second
	frameLength = 100 * time.Millisecond
	fastTau     = 0.125 // seconds, IEC 61672-1 Fast

	DefaultBaselineWindow     = 600
	DefaultBaselinePercentile = 10.0
	DefaultClockStepThreshold = 100 * time.Millisecond
)

// floorMeanSquare is the mean square of a signal one 24-bit step in size.
// Levels below it cannot be measured and read as this floor, never -Inf.
var floorMeanSquare = math.Pow(2, -46)

// Config sets up a Meter.
type Config struct {
	// SampleRate must be 48000.
	SampleRate float64
	// SensitivityDBFS is the AES17 level, in dBFS, of a 94 dB SPL tone at 1 kHz.
	SensitivityDBFS float64
	// Calibration corrects the mic response. It is required; pass
	// NoCalibration explicitly when there is no calibration file.
	Calibration Calibration

	BaselineWindow     int     // bins; 0 means DefaultBaselineWindow
	BaselinePercentile float64 // 0 means DefaultBaselinePercentile

	// ClockStepThreshold is how far a chunk's start time may differ from the
	// time given by the sample count before the meter re-anchors to it.
	// 0 means DefaultClockStepThreshold.
	ClockStepThreshold time.Duration

	OnBin       func(Bin)
	OnFrame     func(Frame)
	OnEnvelope  func(t time.Time, v float64)
	OnClockStep func(expected, got time.Time)
}

// Bin is one second of measurement. Levels are dB SPL.
type Bin struct {
	Start   time.Time // a whole second
	Samples int       // 48000 for a full second

	LAeq     float64 // A-weighted equivalent level
	LAmax    float64 // highest A-weighted level with Fast time weighting
	LowBand  float64 // unweighted level, 20-120 Hz
	HighBand float64 // unweighted level, above 500 Hz
	Baseline float64 // baseline after this bin was added
}

// Frame is 100 ms of measurement for the detector. Levels are dB SPL.
type Frame struct {
	Start   time.Time // a whole 100 ms
	Samples int

	LAeq     float64
	LAmax    float64
	LowBand  float64
	HighBand float64

	// Baseline is the baseline after the last closed bin. BaselineN is the
	// number of bins it covers; 0 means there is no baseline yet.
	Baseline  float64
	BaselineN int
}

// Meter turns raw samples into calibrated levels: one Bin per second, one
// Frame per 100 ms, and the impact-band envelope at 100 Hz.
//
// Time comes from the sample count, anchored at the start time of the first
// chunk. Later chunk start times are checked against the sample count; if
// one differs by more than ClockStepThreshold, the meter reports a clock step
// and re-anchors. Time inside the meter never goes back: after a backward
// step, samples go into the open bin until the clock passes its end.
type Meter struct {
	cfg      Config
	offsetDB float64 // add to 10*log10(mean square) to get dB SPL

	aweight  *dsp.Cascade
	low      *dsp.Cascade
	high     *dsp.Cascade
	fast     *dsp.TimeWeighting
	envelope *dsp.Envelope

	baseline      *Baseline
	baselineValue float64

	anchored bool
	anchor   time.Time
	count    int64 // samples since anchor

	bin   accumulator
	frame accumulator
}

type accumulator struct {
	open       bool
	start, end time.Time
	n          int
	sumA2      float64
	sumLow2    float64
	sumHigh2   float64
	fastMax    float64
}

// New returns a meter. It refuses a sample rate other than 48 kHz and a
// missing calibration.
func New(cfg Config) (*Meter, error) {
	if cfg.SampleRate != SampleRate {
		return nil, fmt.Errorf("meter: sample rate is %g Hz, want %d Hz", cfg.SampleRate, SampleRate)
	}
	if cfg.Calibration == nil {
		return nil, errors.New("meter: no calibration set; use NoCalibration explicitly if there is no calibration file")
	}
	if cfg.BaselineWindow == 0 {
		cfg.BaselineWindow = DefaultBaselineWindow
	}
	if cfg.BaselinePercentile == 0 {
		cfg.BaselinePercentile = DefaultBaselinePercentile
	}
	if cfg.ClockStepThreshold == 0 {
		cfg.ClockStepThreshold = DefaultClockStepThreshold
	}
	if cfg.BaselineWindow < 1 || cfg.BaselinePercentile < 0 || cfg.BaselinePercentile > 100 {
		return nil, fmt.Errorf("meter: baseline window %d and percentile %g are out of range",
			cfg.BaselineWindow, cfg.BaselinePercentile)
	}

	m := &Meter{
		cfg: cfg,
		// AES17: a sine's mean square is half its peak squared, so add 3.01 dB.
		offsetDB: 10*math.Log10(2) - cfg.SensitivityDBFS + 94,
		baseline: NewBaseline(cfg.BaselineWindow, cfg.BaselinePercentile),
	}
	m.initFilters()
	return m, nil
}

func (m *Meter) initFilters() {
	m.aweight = dsp.NewAWeighting(SampleRate)
	m.low = dsp.NewImpactBand(SampleRate)
	m.high = dsp.NewButterworthHighpass4(500, SampleRate)
	m.fast = dsp.NewTimeWeighting(fastTau, SampleRate)
	m.envelope = dsp.NewEnvelope(SampleRate)
}

// Process measures a chunk of samples. start is the time of the first sample.
// It does not allocate.
func (m *Meter) Process(x []float64, start time.Time) {
	if len(x) == 0 {
		return
	}
	if !m.anchored {
		m.anchor, m.count, m.anchored = start, 0, true
	} else if expected := m.sampleTime(m.count); absDuration(start.Sub(expected)) > m.cfg.ClockStepThreshold {
		if m.cfg.OnClockStep != nil {
			m.cfg.OnClockStep(expected, start)
		}
		m.anchor, m.count = start, 0
	}
	for _, s := range x {
		t := m.sampleTime(m.count)
		m.count++
		m.add(s, t)
	}
}

// Flush emits the open frame and the open bin, even if they are partial.
func (m *Meter) Flush() {
	m.closeFrame()
	m.closeBin()
}

// Reset flushes, clears all filter state, and drops the time anchor. Call it
// when the audio stream restarts. The baseline is kept.
func (m *Meter) Reset() {
	m.Flush()
	m.initFilters()
	m.anchored = false
}

// SetBaseline replaces the baseline window and percentile on a running meter.
// It refuses values outside the range New accepts and changes nothing then.
//
// The ring holds a fixed window of seconds, so the old history cannot answer
// the new question: the baseline starts again from no seconds at all. Until it
// covers min_baseline_s again, the frames say so through BaselineN and the
// detector stays quiet. The caller must run this on the goroutine that calls
// Process, and should say in the log that detection has paused.
func (m *Meter) SetBaseline(window int, percentile float64) error {
	if window < 1 || percentile <= 0 || percentile > 100 {
		return fmt.Errorf("meter: baseline window %d and percentile %g are out of range",
			window, percentile)
	}
	m.cfg.BaselineWindow, m.cfg.BaselinePercentile = window, percentile
	m.baseline = NewBaseline(window, percentile)
	return nil
}

// sampleTime returns the time of sample n after the anchor. It splits n into
// whole seconds so the nanosecond product cannot overflow.
func (m *Meter) sampleTime(n int64) time.Time {
	sec, rem := n/SampleRate, n%SampleRate
	return m.anchor.Add(time.Duration(sec)*time.Second + time.Duration(rem)*time.Second/SampleRate)
}

func (m *Meter) add(s float64, t time.Time) {
	c := m.cfg.Calibration.Correct(s)
	a := m.aweight.Process(c)
	a2 := a * a
	fast := m.fast.Process(a2)
	low := m.low.Process(c)
	high := m.high.Process(c)
	if v, ok := m.envelope.Process(c); ok && m.cfg.OnEnvelope != nil {
		m.cfg.OnEnvelope(t, v)
	}

	// A sample dated before the end of the open bin or frame counts in it.
	// This keeps time moving forward after a backward clock step.
	if !m.bin.open || !t.Before(m.bin.end) {
		m.closeFrame()
		m.closeBin()
		m.bin.begin(t.Truncate(binLength), binLength)
	}
	if !m.frame.open || !t.Before(m.frame.end) {
		m.closeFrame()
		m.frame.begin(t.Truncate(frameLength), frameLength)
	}

	m.bin.addSample(a2, low*low, high*high, fast)
	m.frame.addSample(a2, low*low, high*high, fast)
}

func (a *accumulator) begin(start time.Time, length time.Duration) {
	*a = accumulator{open: true, start: start, end: start.Add(length)}
}

func (a *accumulator) addSample(a2, low2, high2, fast float64) {
	a.n++
	a.sumA2 += a2
	a.sumLow2 += low2
	a.sumHigh2 += high2
	a.fastMax = max(a.fastMax, fast)
}

func (m *Meter) level(meanSquare float64) float64 {
	return 10*math.Log10(max(meanSquare, floorMeanSquare)) + m.offsetDB
}

func (m *Meter) closeBin() {
	if !m.bin.open {
		return
	}
	n := float64(m.bin.n)
	b := Bin{
		Start:    m.bin.start,
		Samples:  m.bin.n,
		LAeq:     m.level(m.bin.sumA2 / n),
		LAmax:    m.level(m.bin.fastMax),
		LowBand:  m.level(m.bin.sumLow2 / n),
		HighBand: m.level(m.bin.sumHigh2 / n),
	}
	m.baselineValue = m.baseline.Add(b.LAeq)
	b.Baseline = m.baselineValue
	m.bin.open = false
	if m.cfg.OnBin != nil {
		m.cfg.OnBin(b)
	}
}

func (m *Meter) closeFrame() {
	if !m.frame.open {
		return
	}
	n := float64(m.frame.n)
	f := Frame{
		Start:     m.frame.start,
		Samples:   m.frame.n,
		LAeq:      m.level(m.frame.sumA2 / n),
		LAmax:     m.level(m.frame.fastMax),
		LowBand:   m.level(m.frame.sumLow2 / n),
		HighBand:  m.level(m.frame.sumHigh2 / n),
		Baseline:  m.baselineValue,
		BaselineN: m.baseline.Len(),
	}
	m.frame.open = false
	if m.cfg.OnFrame != nil {
		m.cfg.OnFrame(f)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
