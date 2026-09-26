// Package pipeline connects capture, measurement, detection, clips, and
// storage, and runs them as supervised goroutines.
//
// Concurrency model (SPEC.md section 5):
//
//	capture (audio.Capture or audio.FileSource)
//	   | Chunks: bounded; live capture drops and counts a chunk when it is full
//	   v
//	dspLoop -- meter --> detector --> events --> eventLoop
//	   |         |                                  | insert event, wait for post-roll,
//	   |         +--> bins --> storeLoop            | save audio clip, then video
//	   |                         |                  | clip from the ring, insert
//	   |                         |                  | a media row for each
//	   |                         | batch bins,      |
//	   |                         | commit every     |
//	   |                         | batch_interval   |
//	   +--> clip.Recorder <---------------------------+
//	        (mutex-guarded 30 s buffer of filtered audio)
//
// The meter and the detector run on the DSP goroutine. Both do constant work
// per sample and do not allocate, so separate goroutines would only add
// hand-offs.
//
// dspLoop never waits for the other loops. It sends bins, events, and health
// records on bounded channels and counts anything that does not fit. Health
// records are written by storeLoop.
//
// On shutdown, or when Chunks closes, dspLoop flushes the meter and the
// detector and closes the bin and event channels. storeLoop and eventLoop
// drain them and finish their work even if the context is already canceled.
// Run returns after both have finished. A loop that panics is logged,
// recorded as loop_restart, and started again after one second.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/minayousseif/stompwatch/internal/audio"
	"github.com/minayousseif/stompwatch/internal/clip"
	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/health"
	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/store"
	"github.com/minayousseif/stompwatch/internal/video"
)

// Store is the part of store.Store the pipeline writes to.
type Store interface {
	InsertBins(ctx context.Context, bins []meter.Bin) (int, error)
	InsertEvent(ctx context.Context, e detect.Event, created time.Time) (int64, error)
	InsertMedia(ctx context.Context, m store.Media) error
	AddHealth(ctx context.Context, at time.Time, kind, detail string, d time.Duration) error
}

// VideoClips cuts the camera's clip of an event from the segment ring.
// Ready says whether the ring holds the end of a window yet. Save writes
// the clip and returns video.ErrNoVideo when the ring holds none of it.
type VideoClips interface {
	Ready(to time.Time) bool
	Save(ctx context.Context, eventID int64, from, to time.Time) (video.Clip, error)
}

// Config holds what the pipeline needs.
type Config struct {
	Settings config.Config
	Store    Store
	Recorder *clip.Recorder
	// Video cuts a video clip for each event. nil means video is off, and
	// nothing else changes: the collector never depends on the camera.
	Video       VideoClips
	Calibration meter.Calibration
	Chunks      <-chan audio.Chunk
	Status      *health.Status
	Log         *slog.Logger     // nil means slog.Default()
	Now         func() time.Time // nil means time.Now

	// Alert reports a condition that needs attention now, for example
	// through the heartbeat's fail ping. nil does nothing.
	Alert func(reason string)
	// Publish receives every measured second as it is produced, for the live
	// meter in the dashboard. It runs on the DSP goroutine and must not block.
	// nil does nothing.
	Publish func(b meter.Bin)
	// CommitTick replaces the batch_interval ticker, for tests.
	CommitTick <-chan time.Time
}

// Stats are the pipeline's failure and activity counters.
type Stats struct {
	Chunks         int64
	DroppedSamples int64
	BinsDropped    int64
	EventsDropped  int64
	HealthDropped  int64
	WriteFailures  int64
	LoopRestarts   int64
}

const (
	binQueue    = 3600 // one hour of bins
	eventQueue  = 64
	healthQueue = 256

	restartDelay       = time.Second
	alertAfterFailures = 3
	clipPoll           = 250 * time.Millisecond
	// If the clip buffer stops moving for this long, save what there is.
	clipStallLimit = 30 * time.Second
	eventAttempts  = 5
	// videoWait is how long past the end of the window the event loop waits
	// for the ring to close the segment that holds it. A segment is ten
	// seconds by default and the camera runs a few seconds behind the
	// clock, so this covers two segments and a margin. After it, the clip
	// is cut from what there is and marked truncated.
	videoWait = 45 * time.Second
)

// Pipeline runs the collector.
type Pipeline struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	bins   chan meter.Bin
	events chan pendingEvent
	health chan healthRecord

	// settings holds the settings in force. Reload replaces the pointer; the
	// DSP goroutine loads it once per chunk and applies a change there, so
	// nothing else has to lock the meter or the detector.
	settings atomic.Pointer[config.Config]

	chunks         atomic.Int64
	droppedSamples atomic.Int64
	binsDropped    atomic.Int64
	eventsDropped  atomic.Int64
	healthDropped  atomic.Int64
	writeFailures  atomic.Int64
	loopRestarts   atomic.Int64
}

type pendingEvent struct {
	event detect.Event
	hold  int // recorder hold for the event's audio; 0 means none
}

type healthRecord struct {
	at     time.Time
	kind   string
	detail string
	dur    time.Duration
}

// New checks the config and returns a pipeline.
func New(cfg Config) (*Pipeline, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("pipeline: no store")
	case cfg.Recorder == nil:
		return nil, errors.New("pipeline: no clip recorder")
	case cfg.Calibration == nil:
		return nil, errors.New("pipeline: no calibration; use meter.NoCalibration explicitly")
	case cfg.Chunks == nil:
		return nil, errors.New("pipeline: no audio input")
	case cfg.Status == nil:
		return nil, errors.New("pipeline: no health status")
	}
	if err := cfg.Settings.Validate(); err != nil {
		return nil, err
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Alert == nil {
		cfg.Alert = func(string) {}
	}
	if cfg.Publish == nil {
		cfg.Publish = func(meter.Bin) {}
	}
	p := &Pipeline{
		cfg:    cfg,
		log:    cfg.Log,
		now:    cfg.Now,
		bins:   make(chan meter.Bin, binQueue),
		events: make(chan pendingEvent, eventQueue),
		health: make(chan healthRecord, healthQueue),
	}
	s := cfg.Settings
	p.settings.Store(&s)
	return p, nil
}

// Settings returns the settings in force now.
func (p *Pipeline) Settings() config.Config { return *p.settings.Load() }

// Reload installs new settings on the running collector. It returns at once:
// the DSP goroutine takes them up on its next chunk, within about 100 ms. The
// dashboard calls this from a request handler, and a request must never wait
// for the measurement.
//
// Settings that do not validate change nothing. They are logged and recorded
// instead, so a bad value typed into the dashboard cannot stop the collector.
func (p *Pipeline) Reload(c config.Config) {
	if err := c.Validate(); err != nil {
		p.log.Error("the new settings were refused; the collector keeps the settings it has", "err", err)
		p.RecordHealth(p.now(), store.HealthSettingsChange, "the new settings were refused: "+err.Error(), 0)
		return
	}
	p.settings.Store(&c)
}

// Stats returns the current counters.
func (p *Pipeline) Stats() Stats {
	return Stats{
		Chunks:         p.chunks.Load(),
		DroppedSamples: p.droppedSamples.Load(),
		BinsDropped:    p.binsDropped.Load(),
		EventsDropped:  p.eventsDropped.Load(),
		HealthDropped:  p.healthDropped.Load(),
		WriteFailures:  p.writeFailures.Load(),
		LoopRestarts:   p.loopRestarts.Load(),
	}
}

// RecordHealth queues a health record without waiting. Capture callbacks
// use it. A full queue drops the record, counts it, and logs it.
func (p *Pipeline) RecordHealth(at time.Time, kind, detail string, d time.Duration) {
	select {
	case p.health <- healthRecord{at, kind, detail, d}:
	default:
		p.healthDropped.Add(1)
		p.log.Error("health queue full; record not stored", "kind", kind, "detail", detail)
	}
}

// Run runs the pipeline until Chunks closes or ctx is done. It returns after
// every bin and event has been handled, with ctx.Err() if ctx ended it.
func (p *Pipeline) Run(ctx context.Context) error {
	// The drain loops must finish their work after ctx is canceled.
	drain := context.WithoutCancel(ctx)
	stopping := make(chan struct{})
	storeDone := make(chan struct{})
	eventDone := make(chan struct{})

	go func() {
		defer close(storeDone)
		supervise(drain, "store", restartDelay, p.onRestart, p.storeLoop)
	}()
	go func() {
		defer close(eventDone)
		supervise(drain, "event", restartDelay, p.onRestart, func(c context.Context) error {
			return p.eventLoop(c, stopping)
		})
	}()

	err := supervise(ctx, "dsp", restartDelay, p.onRestart, p.dspLoop)
	close(stopping)
	close(p.events)
	close(p.bins)
	<-eventDone
	<-storeDone
	return err
}

func (p *Pipeline) onRestart(name string, cause any) {
	p.loopRestarts.Add(1)
	p.log.Error("loop failed; restarting", "loop", name, "cause", fmt.Sprint(cause))
	p.RecordHealth(p.now(), store.HealthLoopRestart, fmt.Sprintf("%s loop: %v", name, cause), 0)
}

// dspLoop measures and detects. It builds a fresh meter and detector each
// time it starts, so a restart after a panic does not reuse broken state.
func (p *Pipeline) dspLoop(ctx context.Context) error {
	applied := p.settings.Load()
	s := *applied
	rec := p.cfg.Recorder

	var hold int
	var heldFor time.Time

	onEvent := func(e detect.Event) {
		pe := pendingEvent{event: e, hold: hold}
		hold, heldFor = 0, time.Time{}
		select {
		case p.events <- pe:
		default:
			p.eventsDropped.Add(1)
			rec.Release(pe.hold)
			p.log.Error("event queue full; event not stored", "start", e.Start, "end", e.End)
			p.RecordHealth(e.Start, store.HealthWriteError, "event queue full; event not stored", e.Duration())
		}
	}

	det, err := detect.New(detectConfig(s, onEvent))
	if err != nil {
		return err
	}

	// paused is true inside the daily recording pause. It is read by the
	// frame hand-off below and set only on this goroutine, before the chunk
	// that crosses the boundary reaches the meter.
	paused := false

	m, err := meter.New(meter.Config{
		SampleRate:         meter.SampleRate,
		SensitivityDBFS:    s.SensitivityDBFS,
		Calibration:        p.cfg.Calibration,
		BaselineWindow:     int(s.BaselineWindow / time.Second),
		BaselinePercentile: s.BaselinePercentile,
		OnBin: func(b meter.Bin) {
			// The live meter comes first. A full database queue must not stop
			// the number on the screen from moving.
			p.cfg.Publish(b)
			select {
			case p.bins <- b:
			default:
				p.binsDropped.Add(1)
				p.log.Error("bin queue full; second not stored", "second", b.Start)
				p.RecordHealth(b.Start, store.HealthWriteError, "bin queue full; second not stored", time.Second)
			}
		},
		// Inside the recording pause no event opens, so no clip is ever
		// asked for. The meter still measures and the bins still flow, so
		// every second is stored and the record has no hole.
		OnFrame: func(f meter.Frame) {
			if paused {
				return
			}
			det.Frame(f)
		},
		OnEnvelope: det.Envelope,
		OnClockStep: func(expected, got time.Time) {
			p.log.Warn("audio clock stepped", "expected", expected, "got", got)
			p.RecordHealth(got, store.HealthClockStep,
				fmt.Sprintf("audio time moved from %s to %s", expected.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano)),
				got.Sub(expected))
		},
	})
	if err != nil {
		return err
	}

	flush := func() {
		m.Flush()
		det.Flush()
		if hold != 0 {
			rec.Release(hold)
		}
	}

	var stream, nextOffset int64
	for {
		var ch audio.Chunk
		var ok bool
		select {
		case <-ctx.Done():
			flush()
			return ctx.Err()
		case ch, ok = <-p.cfg.Chunks:
		}
		if !ok {
			flush()
			return nil
		}
		p.chunks.Add(1)

		// A settings change waits here until the DSP goroutine reaches it, so
		// the meter and the detector are only ever touched from this one place.
		if next := p.settings.Load(); next != applied {
			if err := p.applySettings(*next, s, det, m, onEvent); err != nil {
				p.log.Error("the new settings were refused in the measurement loop; nothing changed", "err", err)
				p.RecordHealth(p.now(), store.HealthSettingsChange, "the new settings were refused: "+err.Error(), 0)
			} else {
				s = *next
			}
			applied = next
		}

		if stream != 0 && (ch.Stream != stream || ch.Offset != nextOffset) {
			if ch.Stream == stream {
				lost := ch.Offset - nextOffset
				p.droppedSamples.Add(lost)
				p.log.Error("audio chunks dropped; the DSP loop fell behind", "samples", lost)
				p.RecordHealth(ch.Start, store.HealthFrameDrop,
					fmt.Sprintf("%d samples dropped before this chunk", lost),
					time.Duration(lost)*time.Second/meter.SampleRate)
			}
			m.Reset()
			rec.Reset()
		}
		stream, nextOffset = ch.Stream, ch.Offset+int64(len(ch.Samples))

		// The pause is checked on the chunk that crosses into it, before
		// that chunk reaches the detector, and read from the settings in
		// force, so a change from the dashboard applies at once.
		if in := s.RecordingPause.Contains(ch.Start); in != paused {
			paused = in
			p.pauseChanged(det, s.RecordingPause, ch.Start, in)
		}

		p.cfg.Status.AudioArrived(p.now())
		m.Process(ch.Samples, ch.Start)
		rec.Process(ch.Samples, ch.Start)

		// Hold clip audio from the pre-roll of an open candidate, so an
		// event longer than the buffer keeps its start.
		if start, open := det.OpenSince(); open {
			if !heldFor.Equal(start) {
				if hold != 0 {
					rec.Release(hold)
				}
				hold, heldFor = rec.Hold(start.Add(-s.PreRoll)), start
			}
		} else if hold != 0 {
			rec.Release(hold)
			hold, heldFor = 0, time.Time{}
		}
	}
}

// pauseChanged runs on the DSP goroutine when the daily recording pause
// begins or ends. An event still open when the pause begins is closed at its
// last loud moment, so it keeps what was heard before the pause and nothing
// after. Both edges go in the health log: a stretch with no events must be
// explainable, and a pause must never read as a failure (SPEC.md section 3
// rule 4).
func (p *Pipeline) pauseChanged(det *detect.Detector, pause config.PauseWindows, at time.Time, began bool) {
	if began {
		det.Flush()
		until := "it ends"
		if q, ok := pause.Window(at); ok {
			until = fmt.Sprintf("%02d:%02d", int(q.End/time.Hour), int(q.End%time.Hour/time.Minute))
		}
		p.log.Info("the daily recording pause began; no events, clips or video until it ends",
			"until", until, "effect", "the level of every second is still measured and stored")
		p.RecordHealth(at, store.HealthRecordingPause,
			"the daily recording pause began: no events, no clips and no video until "+until+
				"; the level of every second is still measured", 0)
		return
	}
	p.log.Info("the daily recording pause ended; events and clips are recorded again")
	p.RecordHealth(at, store.HealthRecordingPause,
		"the daily recording pause ended: events, clips and video are recorded again", 0)
}

// clipWindow is the stretch the clips of one event cover: the pre-roll, the
// event, and the post-roll, cut back so that nothing from inside the daily
// recording pause reaches the disk. A post-roll that would run into a pause
// stops where the pause begins, and a pre-roll that would reach back into
// one starts where it ended.
//
// An event cannot open inside the pause, so a pause that overlaps the event
// itself only happens when the pause was changed while the event was open.
// Then the clip keeps the part of the event before the pause, or drops only
// the pre-roll when the pause already covered the start.
func clipWindow(pause config.PauseWindows, start, end time.Time, pre, post time.Duration) (from, to time.Time) {
	from, to = start.Add(-pre), end.Add(post)
	for _, sp := range pause.Spans(from, to) {
		switch {
		case !sp.To.After(start):
			if sp.To.After(from) {
				from = sp.To
			}
		case !sp.From.Before(end):
			if sp.From.Before(to) {
				to = sp.From
			}
		case sp.From.After(start):
			to = sp.From
		default:
			from = start
		}
	}
	return from, to
}

// detectConfig turns the settings into detector settings.
func detectConfig(s config.Config, onEvent func(detect.Event)) detect.Config {
	return detect.Config{
		ThresholdDB:     s.ThresholdDB,
		MinDuration:     s.MinDuration,
		Hangover:        s.Hangover,
		Cooldown:        s.Cooldown,
		MaxEvent:        s.MaxEvent,
		MinBaselineBins: int(s.MinBaseline / time.Second),
		OnEvent:         onEvent,
	}
}

// applySettings installs next on the meter and the detector. It runs on the
// DSP goroutine. An event that is open keeps its start and its loud time: the
// new settings apply to the frames that follow.
//
// The baseline is different. Its ring holds a fixed window of seconds, so a
// new window or percentile has to start the history again, and detection
// pauses until min_baseline_s of new seconds have arrived. That is worth a
// warning and a health row: a quiet hour after a settings change should be
// explainable rather than mysterious.
func (p *Pipeline) applySettings(next, prev config.Config, det *detect.Detector, m *meter.Meter, onEvent func(detect.Event)) error {
	if err := det.SetConfig(detectConfig(next, onEvent)); err != nil {
		return err
	}
	if next.BaselineWindow == prev.BaselineWindow && next.BaselinePercentile == prev.BaselinePercentile {
		return nil
	}
	if err := m.SetBaseline(int(next.BaselineWindow/time.Second), next.BaselinePercentile); err != nil {
		return err
	}
	detail := fmt.Sprintf("the baseline changed to the %g%% percentile of a %s window, so it starts again; "+
		"detection pauses for %s", next.BaselinePercentile, next.BaselineWindow, next.MinBaseline)
	p.log.Warn("the baseline starts again after a settings change; detection pauses until it is ready",
		"baseline_window", next.BaselineWindow, "baseline_percentile", next.BaselinePercentile,
		"paused_for", next.MinBaseline)
	p.RecordHealth(p.now(), store.HealthSettingsChange, detail, next.MinBaseline)
	return nil
}

// storeLoop batches bins and commits them on every tick. A failed commit
// keeps the bins for the next tick. It also writes health records.
func (p *Pipeline) storeLoop(ctx context.Context) error {
	tick := p.cfg.CommitTick
	if tick == nil {
		t := time.NewTicker(p.cfg.Settings.BatchInterval)
		defer t.Stop()
		tick = t.C
	}

	var batch []meter.Bin
	failStreak := 0
	commit := func() error {
		if len(batch) == 0 {
			return nil
		}
		conflicts, err := p.cfg.Store.InsertBins(ctx, batch)
		if err != nil {
			p.writeFailures.Add(1)
			failStreak++
			p.log.Error("writing bins failed; they stay queued", "bins", len(batch), "err", err)
			p.addHealth(ctx, healthRecord{p.now(), store.HealthWriteError,
				fmt.Sprintf("writing %d bins failed: %v", len(batch), err), 0})
			if failStreak == alertAfterFailures {
				p.cfg.Alert(fmt.Sprintf("database writes are failing: %v", err))
			}
			return err
		}
		if conflicts > 0 {
			p.log.Error("skipped bins whose second was already stored", "count", conflicts)
			p.addHealth(ctx, healthRecord{p.now(), store.HealthWriteError,
				fmt.Sprintf("skipped %d bins whose second was already stored", conflicts), 0})
		}
		failStreak = 0
		batch = batch[:0]
		p.cfg.Status.BinsCommitted(p.now())
		return nil
	}

	for {
		select {
		case b, ok := <-p.bins:
			if !ok {
				for attempt := 0; commit() != nil && attempt < 3; attempt++ {
					time.Sleep(time.Second)
				}
				if len(batch) > 0 {
					p.log.Error("bins lost at shutdown after failed writes", "bins", len(batch))
				}
				p.drainHealth(ctx)
				return nil
			}
			batch = append(batch, b)
		case h := <-p.health:
			p.addHealth(ctx, h)
		case <-tick:
			commit()
		}
	}
}

func (p *Pipeline) drainHealth(ctx context.Context) {
	for {
		select {
		case h := <-p.health:
			p.addHealth(ctx, h)
		default:
			return
		}
	}
}

func (p *Pipeline) addHealth(ctx context.Context, h healthRecord) {
	if err := p.cfg.Store.AddHealth(ctx, h.at, h.kind, h.detail, h.dur); err != nil {
		p.log.Error("writing health record failed", "kind", h.kind, "detail", h.detail, "err", err)
	}
}

// eventLoop stores each event and its audio clip.
func (p *Pipeline) eventLoop(ctx context.Context, stopping <-chan struct{}) error {
	for pe := range p.events {
		p.handleEvent(ctx, pe, stopping)
	}
	return nil
}

func (p *Pipeline) handleEvent(ctx context.Context, pe pendingEvent, stopping <-chan struct{}) {
	rec := p.cfg.Recorder
	defer rec.Release(pe.hold)
	e := pe.event

	id, err := p.insertEvent(ctx, e)
	if err != nil {
		p.log.Error("storing event failed", "start", e.Start, "err", err)
		p.addHealth(ctx, healthRecord{e.Start, store.HealthWriteError, fmt.Sprintf("storing event failed: %v", err), e.Duration()})
		p.cfg.Alert(fmt.Sprintf("an event could not be stored: %v", err))
		return
	}

	// The roll lengths are read here, not at startup, so a change from the
	// dashboard reaches the next clip.
	s := p.Settings()
	from, to := clipWindow(s.RecordingPause, e.Start, e.End, s.PreRoll, s.PostRoll)
	p.saveAudio(ctx, id, e, from, to, stopping)
	// The video comes after the audio and never instead of it. A slow
	// camera cannot hold up the clip that is the measurement.
	if p.cfg.Video != nil {
		p.saveVideo(ctx, id, e, from, to, stopping)
	}
}

// saveAudio waits for the post-roll, writes the audio clip, and records it.
func (p *Pipeline) saveAudio(ctx context.Context, id int64, e detect.Event, from, to time.Time, stopping <-chan struct{}) {
	rec := p.cfg.Recorder
	lastLatest, lastMove := rec.Latest(), time.Now()
	shortened := false
	for {
		c, err := rec.Save(id, from, to)
		if errors.Is(err, clip.ErrNotReady) {
			latest := rec.Latest()
			if !latest.Equal(lastLatest) {
				lastLatest, lastMove = latest, time.Now()
			}
			stopped := false
			select {
			case <-stopping:
				stopped = true
			default:
			}
			if stopped || time.Since(lastMove) > clipStallLimit {
				if latest.IsZero() || !latest.After(from) {
					p.log.Error("no audio for event clip", "event", id)
					p.addHealth(ctx, healthRecord{e.Start, store.HealthClipTruncated, fmt.Sprintf("event %d: no audio for the clip", id), 0})
					return
				}
				// Capture has stopped or stalled, so the rest of the
				// post-roll is never coming. Ask for everything that
				// arrived and nothing more: a clip runs up to but not
				// including to, so to is one output period past the
				// newest sample. The period follows clip_lowpass_hz, so
				// it is read from the recorder rather than assumed to be
				// a millisecond.
				//
				// This is done once. If the shortened end is still not
				// ready, asking again cannot help, and retrying would be
				// a loop with nothing to wait for.
				if shortened {
					p.log.Error("clip never became ready", "event", id, "end", to, "latest", latest)
					p.addHealth(ctx, healthRecord{e.Start, store.HealthClipTruncated,
						fmt.Sprintf("event %d: the clip never became ready; no clip was written", id), 0})
					return
				}
				to = latest.Add(time.Second / time.Duration(rec.OutputRate()))
				shortened = true
				continue
			}
			select {
			case <-stopping:
			case <-time.After(clipPoll):
			}
			continue
		}
		if err != nil {
			p.log.Error("saving clip failed", "event", id, "err", err)
			p.addHealth(ctx, healthRecord{e.Start, store.HealthWriteError, fmt.Sprintf("event %d: saving clip failed: %v", id, err), 0})
			return
		}

		err = p.cfg.Store.InsertMedia(ctx, store.Media{
			EventID: id, Kind: store.KindAudio, Path: c.Path, Bytes: c.Bytes,
			Duration: c.Duration, SHA256: c.SHA256, Started: c.Start, Truncated: c.Truncated,
		})
		if err != nil {
			p.log.Error("storing clip record failed", "event", id, "path", c.Path, "err", err)
			p.addHealth(ctx, healthRecord{e.Start, store.HealthWriteError, fmt.Sprintf("event %d: storing clip record failed: %v", id, err), 0})
		}
		if c.Truncated || c.Clipped > 0 {
			p.log.Warn("clip incomplete", "event", id, "truncated", c.Truncated, "clipped_samples", c.Clipped)
			p.addHealth(ctx, healthRecord{e.Start, store.HealthClipTruncated,
				fmt.Sprintf("event %d: clip truncated %v, %d clipped samples", id, c.Truncated, c.Clipped), 0})
		}
		return
	}
}

// saveVideo waits for the ring to close the segment that holds the end of
// the window, cuts the clip, and records it. A window the ring does not
// hold, in whole or in part, goes in the health log: a missing minute of
// video must be a known gap, not a surprise (SPEC.md section 3.4 and section 7).
func (p *Pipeline) saveVideo(ctx context.Context, id int64, e detect.Event, from, to time.Time, stopping <-chan struct{}) {
	v := p.cfg.Video
	deadline := time.Now().Add(time.Until(to) + videoWait)
	for !v.Ready(to) {
		stopped := false
		select {
		case <-stopping:
			stopped = true
		default:
		}
		if stopped || time.Now().After(deadline) {
			break
		}
		select {
		case <-stopping:
		case <-time.After(clipPoll):
		}
	}

	c, err := v.Save(ctx, id, from, to)
	switch {
	case errors.Is(err, video.ErrNoVideo):
		p.log.Warn("no video for event clip", "event", id, "from", from, "to", to)
		p.addHealth(ctx, healthRecord{e.Start, store.HealthClipTruncated,
			fmt.Sprintf("event %d: no video covers the clip window", id), 0})
		return
	case err != nil:
		p.log.Error("saving video clip failed", "event", id, "err", err)
		p.addHealth(ctx, healthRecord{e.Start, store.HealthWriteError,
			fmt.Sprintf("event %d: saving video clip failed: %v", id, err), 0})
		return
	}
	err = p.cfg.Store.InsertMedia(ctx, store.Media{
		EventID: id, Kind: store.KindVideo, Path: c.Path, Bytes: c.Bytes,
		Duration: c.Duration, SHA256: c.SHA256, Started: c.Start, Truncated: c.Truncated,
		// What the file holds, which the writer read back from it, and not
		// what the setting says now: a clip keeps what it holds
		// (SPEC.md section 15 decision 22).
		CameraAudio: c.CameraAudio,
	})
	if err != nil {
		p.log.Error("storing video record failed", "event", id, "path", c.Path, "err", err)
		p.addHealth(ctx, healthRecord{e.Start, store.HealthWriteError,
			fmt.Sprintf("event %d: storing video record failed: %v", id, err), 0})
	}
	if c.Truncated {
		p.log.Warn("video clip incomplete", "event", id, "start", c.Start, "duration", c.Duration)
		p.addHealth(ctx, healthRecord{e.Start, store.HealthClipTruncated,
			fmt.Sprintf("event %d: the video clip does not hold the whole window", id), 0})
	}
	// camera_audio was on and the clip has no usable audio track. Nothing
	// noticed the first time this happened, and the owner found it by running
	// ffprobe by hand a day later.
	if c.AudioProblem != "" {
		p.log.Error("the video clip has no usable audio track", "event", id,
			"path", c.Path, "problem", c.AudioProblem)
		p.addHealth(ctx, healthRecord{e.Start, store.HealthClipNoAudio,
			fmt.Sprintf("event %d: %s", id, c.AudioProblem), 0})
	}
}

func (p *Pipeline) insertEvent(ctx context.Context, e detect.Event) (int64, error) {
	delay := time.Second
	var err error
	for attempt := 1; attempt <= eventAttempts; attempt++ {
		var id int64
		if id, err = p.cfg.Store.InsertEvent(ctx, e, p.now()); err == nil {
			return id, nil
		}
		p.log.Error("storing event failed; retrying", "attempt", attempt, "err", err)
		time.Sleep(delay)
		delay *= 2
	}
	return 0, err
}
