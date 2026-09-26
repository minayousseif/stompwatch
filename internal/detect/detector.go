package detect

import (
	"errors"
	"math"
	"slices"
	"time"

	"github.com/minayousseif/stompwatch/internal/meter"
)

// Config holds the detector settings (SPEC.md section 6.6 and section 15 decision 3).
type Config struct {
	// ThresholdDB is how far a frame's LAmax must be above the baseline to
	// count as loud. A frame exactly at the threshold counts.
	ThresholdDB float64
	// MinDuration is the total loud time an event needs to be kept.
	MinDuration time.Duration
	// Hangover is the quiet time that closes an event.
	Hangover time.Duration
	// Cooldown is the time after an event closes before a new one may open.
	Cooldown time.Duration
	// MaxEvent force-closes an event that runs this long.
	MaxEvent time.Duration
	// MinBaselineBins is how many one-second bins the baseline must cover
	// before the detector acts.
	MinBaselineBins int

	OnEvent func(Event)
}

// DefaultConfig returns the default settings from SPEC.md section 6.6.
func DefaultConfig() Config {
	return Config{
		ThresholdDB:     15,
		MinDuration:     400 * time.Millisecond,
		Hangover:        2 * time.Second,
		Cooldown:        5 * time.Second,
		MaxEvent:        300 * time.Second,
		MinBaselineBins: 30,
	}
}

// Event is one detected impact noise event. Levels are dB SPL.
type Event struct {
	Start time.Time // start of the first loud frame
	End   time.Time // end of the last loud frame

	LAeq              float64 // energy mean over the event
	LAmax             float64
	BaselineAtTrigger float64
	LowBand           float64 // energy mean, 20-120 Hz
	HighBand          float64 // energy mean, above 500 Hz
	LowHighRatioDB    float64 // LowBand - HighBand

	// Forced is true when MaxEvent closed the event.
	Forced bool

	// JumpDB is LAmax over the median frame LAeq of the 30 s before the
	// event, measured from 30 s to 2 s before it opened. RiseDB is the largest
	// step between the LAeq means of two consecutive seconds, counted from one
	// second before the event opened to its close. A hit through the ceiling
	// jumps in one second; a fan that starts ramps up over several. Both are
	// only meaningful when HasContext is true, which needs 10 s of frames
	// before the event.
	JumpDB     float64
	RiseDB     float64
	HasContext bool

	Class      Class
	Confidence float64
	// Envelope is the impact-band envelope at 100 Hz from Start to 200 ms
	// after End. It is kept so the class can be computed again later.
	Envelope []float64
}

// Duration is End - Start.
func (e Event) Duration() time.Duration { return e.End.Sub(e.Start) }

const (
	envelopeTail    = 200 * time.Millisecond
	envelopeHistory = 200 // samples, 2 s at 100 Hz

	levelHistory     = 300                                   // frames, 30 s at 100 ms
	levelWindow      = levelHistory * 100 * time.Millisecond // the context window: 30 s
	contextMinFrames = 100                                   // 10 s of frames before an event has context
	jumpWindowEnd    = 2 * time.Second                       // the jump ignores the 2 s just before the event
)

type envSample struct {
	t time.Time
	v float64
}

// levelSample is one frame's LAeq, kept to measure the jump against.
type levelSample struct {
	t    time.Time
	laeq float64
}

// Detector opens and closes events from meter frames.
//
// A frame is loud when its LAmax is at least ThresholdDB above the baseline.
// The first loud frame opens a candidate event. Loud time adds up across
// short quiet dips. The event closes after Hangover of quiet time, or when a
// gap in the frames is that long, or at MaxEvent. It is kept only if its loud
// time reached MinDuration. After a kept event, no new event opens for
// Cooldown. The quiet frames at the end of an event are not part of it.
type Detector struct {
	cfg Config

	open          bool
	ev            eventState
	cooldownUntil time.Time

	history  [envelopeHistory]envSample
	histNext int
	histLen  int
	envelope []envSample

	// levels is the LAeq of the last 30 s of frames, for the jump.
	levels     [levelHistory]levelSample
	levelsNext int
	levelsLen  int
}

type eventState struct {
	start        time.Time
	lastLoudEnd  time.Time
	loudTime     time.Duration
	baseline     float64
	forcedClosed bool

	kept    levelSums // frames up to the last loud frame
	pending levelSums // quiet frames since the last loud frame

	// preMedian is the median LAeq of the 30 s before the event, and
	// hasContext says enough frames were seen to trust it.
	preMedian  float64
	hasContext bool
	// The one-second buckets for the rise. bucket is the index of the
	// current bucket counted from one second before the start; sum and n
	// build its energy mean; last is the mean of the bucket before it, or
	// NaN before the first bucket closes.
	bucket  int
	sum     float64
	n       int
	last    float64
	maxRise float64
}

// levelSums holds energy sums weighted by sample count.
type levelSums struct {
	samples float64
	a       float64
	low     float64
	high    float64
	laMax   float64
}

func (s *levelSums) add(f meter.Frame) {
	w := float64(f.Samples)
	if s.samples == 0 {
		s.laMax = math.Inf(-1)
	}
	s.samples += w
	s.a += w * math.Pow(10, f.LAeq/10)
	s.low += w * math.Pow(10, f.LowBand/10)
	s.high += w * math.Pow(10, f.HighBand/10)
	s.laMax = math.Max(s.laMax, f.LAmax)
}

// addRise puts a frame in its one-second bucket, counted from one second
// before the event opened, and closes the buckets it has passed.
func (ev *eventState) addRise(f meter.Frame) {
	b := int(f.Start.Sub(ev.start.Add(-time.Second)) / time.Second)
	for ev.bucket < b {
		ev.closeBucket()
	}
	ev.sum += math.Pow(10, f.LAeq/10)
	ev.n++
}

// closeBucket turns the current bucket's energy sum into its LAeq mean, folds
// the step from the bucket before it into maxRise, and starts the next
// bucket. An empty bucket (a gap in the frames) is skipped rather than
// counted as silence.
func (ev *eventState) closeBucket() {
	if ev.n > 0 {
		mean := 10 * math.Log10(ev.sum/float64(ev.n))
		if !math.IsNaN(ev.last) {
			ev.maxRise = math.Max(ev.maxRise, mean-ev.last)
		}
		ev.last = mean
	}
	ev.sum, ev.n = 0, 0
	ev.bucket++
}

func (s *levelSums) merge(o levelSums) {
	if o.samples == 0 {
		return
	}
	if s.samples == 0 {
		*s = o
		return
	}
	s.samples += o.samples
	s.a += o.a
	s.low += o.low
	s.high += o.high
	s.laMax = math.Max(s.laMax, o.laMax)
}

// New returns a detector. It refuses settings that would make it silently
// useless.
func New(cfg Config) (*Detector, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Detector{cfg: cfg}, nil
}

func (cfg Config) validate() error {
	switch {
	case cfg.OnEvent == nil:
		return errors.New("detect: OnEvent is not set")
	case cfg.ThresholdDB <= 0:
		return errors.New("detect: threshold must be above 0 dB")
	case cfg.MinDuration < 0 || cfg.Hangover < 0 || cfg.Cooldown < 0:
		return errors.New("detect: durations must not be negative")
	case cfg.MaxEvent <= 0 || cfg.MaxEvent < cfg.MinDuration:
		return errors.New("detect: max event length must be at least the minimum duration")
	case cfg.MinBaselineBins < 1:
		return errors.New("detect: the baseline must cover at least one bin")
	}
	return nil
}

// SetConfig replaces the settings of a running detector. It checks them the
// same way New does and changes nothing if they are bad, so a value typed
// into the dashboard cannot stop detection.
//
// An event that is open at the time keeps its start, its accumulated loud
// time, and its envelope. The new settings apply to the frames that follow.
// The caller must run this on the goroutine that calls Frame.
func (d *Detector) SetConfig(cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	d.cfg = cfg
	return nil
}

// Envelope adds one envelope sample. The meter delivers the envelope samples
// of a frame before the frame itself.
func (d *Detector) Envelope(t time.Time, v float64) {
	d.history[d.histNext] = envSample{t, v}
	d.histNext = (d.histNext + 1) % envelopeHistory
	d.histLen = min(d.histLen+1, envelopeHistory)
	if d.open {
		d.envelope = append(d.envelope, envSample{t, v})
	}
}

// Frame adds one 100 ms frame from the meter.
func (d *Detector) Frame(f meter.Frame) {
	end := f.Start.Add(time.Duration(f.Samples) * time.Second / meter.SampleRate)

	if d.open && f.Start.Sub(d.ev.lastLoudEnd) >= d.cfg.Hangover {
		d.close(d.ev.lastLoudEnd.Add(d.cfg.Hangover))
	}

	d.recordLevel(f)

	loud := f.BaselineN >= d.cfg.MinBaselineBins && f.LAmax-f.Baseline >= d.cfg.ThresholdDB

	if !d.open {
		if !loud || f.Start.Before(d.cooldownUntil) {
			return
		}
		d.openAt(f)
	}
	d.ev.addRise(f)

	if loud {
		d.ev.kept.merge(d.ev.pending)
		d.ev.pending = levelSums{}
		d.ev.kept.add(f)
		d.ev.loudTime += end.Sub(f.Start)
		d.ev.lastLoudEnd = end
	} else {
		d.ev.pending.add(f)
		if end.Sub(d.ev.lastLoudEnd) >= d.cfg.Hangover {
			d.close(end)
			return
		}
	}

	if end.Sub(d.ev.start) >= d.cfg.MaxEvent {
		d.ev.forcedClosed = true
		d.close(end)
	}
}

// Flush closes an open event, for example at shutdown.
func (d *Detector) Flush() {
	if d.open {
		d.close(d.ev.lastLoudEnd)
	}
}

// OpenSince returns the start of the open candidate event, and whether one
// is open. The pipeline uses it to hold clip audio from the start of an event
// that may run longer than the audio buffer.
func (d *Detector) OpenSince() (time.Time, bool) {
	if !d.open {
		return time.Time{}, false
	}
	return d.ev.start, true
}

func (d *Detector) openAt(f meter.Frame) {
	d.open = true
	d.ev = eventState{start: f.Start, baseline: f.Baseline}
	d.envelope = d.envelope[:0]
	for i := 0; i < d.histLen; i++ {
		s := d.history[(d.histNext-d.histLen+i+envelopeHistory)%envelopeHistory]
		if !s.t.Before(f.Start) {
			d.envelope = append(d.envelope, s)
		}
	}

	d.ev.last = math.NaN()
	median, lastSecond, ok := d.context(f.Start)
	d.ev.preMedian, d.ev.hasContext = median, ok
	d.ev.bucket = 0
	for _, v := range lastSecond {
		d.ev.sum += math.Pow(10, v/10)
		d.ev.n++
	}
}

// recordLevel keeps the LAeq of the last 30 s of frames, so a new event can
// read the level before it.
func (d *Detector) recordLevel(f meter.Frame) {
	d.levels[d.levelsNext] = levelSample{f.Start, f.LAeq}
	d.levelsNext = (d.levelsNext + 1) % levelHistory
	d.levelsLen = min(d.levelsLen+1, levelHistory)
}

// context reads the 30 s before start out of the level history: the median
// LAeq from 30 s to 2 s before, and the frames of the last second, which seed
// the first rise bucket. Samples older than the 30 s window are ignored, so a
// recording pause, a capture gap or a stream reset before start does not
// count as context.
func (d *Detector) context(start time.Time) (median float64, lastSecond []float64, ok bool) {
	var pre []float64
	for i := 0; i < d.levelsLen; i++ {
		s := d.levels[(d.levelsNext-d.levelsLen+i+levelHistory)%levelHistory]
		switch {
		case s.t.Before(start.Add(-levelWindow)):
			// Older than the 30 s window: from before a gap. Not context.
		case s.t.Before(start.Add(-jumpWindowEnd)):
			pre = append(pre, s.laeq)
		case s.t.Before(start.Add(-time.Second)):
			// Between 2 s and 1 s before start: neither the jump window nor
			// the bucket that seeds the rise.
		case s.t.Before(start):
			lastSecond = append(lastSecond, s.laeq)
		}
	}
	if len(pre)+len(lastSecond) < contextMinFrames || len(pre) == 0 {
		return 0, nil, false
	}
	slices.Sort(pre)
	return pre[len(pre)/2], lastSecond, true
}

// close ends the open event at closeTime. It emits the event if its loud
// time reached MinDuration, and starts the cooldown.
func (d *Detector) close(closeTime time.Time) {
	d.open = false
	ev := d.ev
	if ev.loudTime < d.cfg.MinDuration {
		return
	}
	d.cooldownUntil = closeTime.Add(d.cfg.Cooldown)
	ev.closeBucket()

	level := func(sum float64) float64 { return 10 * math.Log10(sum/ev.kept.samples) }
	e := Event{
		Start:             ev.start,
		End:               ev.lastLoudEnd,
		LAeq:              level(ev.kept.a),
		LAmax:             ev.kept.laMax,
		BaselineAtTrigger: ev.baseline,
		LowBand:           level(ev.kept.low),
		HighBand:          level(ev.kept.high),
		Forced:            ev.forcedClosed,
		HasContext:        ev.hasContext,
	}
	e.LowHighRatioDB = e.LowBand - e.HighBand
	if ev.hasContext {
		e.JumpDB = ev.kept.laMax - ev.preMedian
		e.RiseDB = ev.maxRise
	}

	tailEnd := e.End.Add(envelopeTail)
	for _, s := range d.envelope {
		if !s.t.Before(e.Start) && s.t.Before(tailEnd) {
			e.Envelope = append(e.Envelope, s.v)
		}
	}
	e.Class, e.Confidence = ClassifyEvent(e.Envelope, Context{
		RatioDB: e.LowHighRatioDB, JumpDB: e.JumpDB, RiseDB: e.RiseDB, CrestDB: e.LAmax - e.LAeq,
		Duration: e.Duration(), Known: e.HasContext,
	})

	d.cfg.OnEvent(e)
}
