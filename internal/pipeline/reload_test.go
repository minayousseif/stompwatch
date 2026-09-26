package pipeline

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/audio"
	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/testsignal"
)

// The tests here run the real DSP loop over synthetic audio and change the
// settings while it runs, because that is the only way to prove that a change
// made in a browser reaches the detector.

const chunkSamples = meter.SampleRate / 10 // 100 ms

// background is pink noise at a level the detector treats as the quiet room.
func background(seconds float64, seed uint64) []float64 {
	return testsignal.Pink(int(seconds*testsignal.Rate), 0.001, seed)
}

// louder multiplies the stretch from `from` for `dur` seconds by gain, which
// makes a noise that is 20*log10(gain) dB above the background.
func louder(x []float64, from, dur, gain float64) {
	a := int(from * testsignal.Rate)
	b := min(int((from+dur)*testsignal.Rate), len(x))
	for i := a; i < b; i++ {
		x[i] *= gain
	}
}

// feed sends x to the pipeline in 100 ms chunks and closes the channel at the
// end. Before chunk i it calls at[i], if there is one. With an unbuffered
// chunk queue, a Reload from at[i] is in force for chunk i itself: the send
// only completes once the DSP loop has taken the chunk, and the loop reads the
// settings after it takes one.
func (e *env) feed(x []float64, at map[int]func()) {
	for i := 0; i*chunkSamples < len(x); i++ {
		if fn := at[i]; fn != nil {
			fn()
		}
		end := min((i+1)*chunkSamples, len(x))
		e.chunks <- audio.Chunk{
			Samples: x[i*chunkSamples : end],
			Start:   t0.Add(time.Duration(i) * 100 * time.Millisecond),
			Stream:  1,
			Offset:  int64(i * chunkSamples),
		}
	}
	close(e.chunks)
}

// run feeds the audio and waits for the pipeline to finish.
func (e *env) run(t *testing.T, x []float64, at map[int]func()) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.pipe.Run(context.Background()) }()
	e.feed(x, at)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

type storedEvent struct {
	startS, endS float64 // seconds after t0
	clipMS       int64
	truncated    int64
}

func storedEvents(t *testing.T, db *sql.DB) []storedEvent {
	t.Helper()
	rows, err := db.Query(`SELECT e.started_ms, e.ended_ms,
		coalesce(m.duration_ms, 0), coalesce(m.truncated, 0)
		FROM events e LEFT JOIN event_media m ON m.event_id = e.id AND m.kind = 'audio'
		ORDER BY e.started_ms`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedEvent
	for rows.Next() {
		var start, end int64
		var ev storedEvent
		if err := rows.Scan(&start, &end, &ev.clipMS, &ev.truncated); err != nil {
			t.Fatal(err)
		}
		ev.startS = float64(start-t0.UnixMilli()) / 1000
		ev.endS = float64(end-t0.UnixMilli()) / 1000
		out = append(out, ev)
	}
	return out
}

// quickBaseline gives the detector a baseline it can reach in five seconds, so
// a test does not have to feed half a minute of audio before every check.
func quickBaseline(c *config.Config) {
	c.MinBaseline = 5 * time.Second
	c.BaselineWindow = 60 * time.Second
}

// A threshold lowered while the collector runs must reach the detector. The
// noise at 10 s is 26 dB above the background: the starting threshold of 60 dB
// misses it and the new threshold of 15 dB catches it.
func TestReloadedThresholdReachesTheDetector(t *testing.T) {
	e := newEnvWith(t, 0, func(c *config.Config) {
		quickBaseline(c)
		c.ThresholdDB = 60
	})
	x := background(20, 11)
	louder(x, 10, 3, 20)

	next := e.settings
	next.ThresholdDB = 15
	e.run(t, x, map[int]func(){95: func() { e.pipe.Reload(next) }}) // at 9.5 s

	got := storedEvents(t, e.query(t))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	if got[0].startS < 9.5 || got[0].startS > 11 {
		t.Errorf("the event starts at %.2f s, want it inside the loud noise at 10 s", got[0].startS)
	}
}

// The same audio with no reload must produce nothing, or the test above would
// pass whatever Reload did.
func TestTheStartingThresholdMissesTheSameNoise(t *testing.T) {
	e := newEnvWith(t, 0, func(c *config.Config) {
		quickBaseline(c)
		c.ThresholdDB = 60
	})
	x := background(20, 11)
	louder(x, 10, 3, 20)

	e.run(t, x, nil)

	if got := storedEvents(t, e.query(t)); len(got) != 0 {
		t.Fatalf("got %d events with the starting threshold of 60 dB, want 0: %+v", len(got), got)
	}
}

// Settings that do not validate must change nothing. A rejected value that
// reached the detector would stop the measurement, which is the one thing the
// dashboard must never do.
func TestReloadRefusesSettingsThatDoNotValidate(t *testing.T) {
	e := newEnvWith(t, 0, func(c *config.Config) {
		quickBaseline(c)
		c.ThresholdDB = 60
	})
	x := background(20, 11)
	louder(x, 10, 3, 20)

	bad := e.settings
	bad.ThresholdDB = 0 // below the floor config.Validate allows
	e.run(t, x, map[int]func(){95: func() { e.pipe.Reload(bad) }})

	if got := e.pipe.Settings().ThresholdDB; got != 60 {
		t.Errorf("threshold_db is %g after the refused change, want 60", got)
	}
	if got := storedEvents(t, e.query(t)); len(got) != 0 {
		t.Errorf("got %d events after the refused change, want 0: %+v", len(got), got)
	}
	// The measurement itself carried on.
	if n := count(t, e.query(t), `SELECT count(*) FROM samples_1s`); n < 19 {
		t.Errorf("samples_1s has %d rows after the refused change, want the whole 20 s", n)
	}
}

// A change while an event is open must not lose the event. The noise runs from
// 10 s to 14 s and the threshold changes in the middle of it.
func TestReloadKeepsAnOpenEvent(t *testing.T) {
	e := newEnvWith(t, 0, func(c *config.Config) {
		quickBaseline(c)
		c.ThresholdDB = 15
	})
	x := background(22, 12)
	louder(x, 10, 4, 20)

	next := e.settings
	next.ThresholdDB = 20                                            // still well under the noise, so the event stays open
	e.run(t, x, map[int]func(){120: func() { e.pipe.Reload(next) }}) // at 12 s

	got := storedEvents(t, e.query(t))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	if got[0].startS > 10.5 {
		t.Errorf("the event starts at %.2f s, want about 10 s: the change restarted it", got[0].startS)
	}
	if d := got[0].endS - got[0].startS; d < 3 {
		t.Errorf("the event is %.2f s long, want at least 3 s: the change cut it short", d)
	}
}

// The baseline holds a fixed window of seconds, so a new window or percentile
// has to start it again, and detection pauses for min_baseline_s. Any other
// change must leave the baseline alone.
func TestBaselineRestartsOnlyWhenItsOwnSettingsChange(t *testing.T) {
	tests := []struct {
		name       string
		change     func(*config.Config)
		wantEvents int
		wantHealth int
	}{{
		name:       "the baseline percentile changed",
		change:     func(c *config.Config) { c.BaselinePercentile = 25 },
		wantEvents: 0, // the baseline is empty again, so nothing is detected
		wantHealth: 1,
	}, {
		name:       "the baseline window changed",
		change:     func(c *config.Config) { c.BaselineWindow = 90 * time.Second },
		wantEvents: 0,
		wantHealth: 1,
	}, {
		name:       "another setting changed",
		change:     func(c *config.Config) { c.MinDuration = 300 * time.Millisecond },
		wantEvents: 1, // the baseline is untouched, so the noise is still detected
		wantHealth: 0,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnvWith(t, 0, func(c *config.Config) {
				quickBaseline(c)
				c.ThresholdDB = 15
			})
			x := background(20, 13)
			louder(x, 8, 3, 20)

			next := e.settings
			tc.change(&next)
			e.run(t, x, map[int]func(){79: func() { e.pipe.Reload(next) }}) // just before 8 s

			db := e.query(t)
			if got := storedEvents(t, db); len(got) != tc.wantEvents {
				t.Errorf("got %d events, want %d: %+v", len(got), tc.wantEvents, got)
			}
			n := count(t, db, `SELECT count(*) FROM system_health WHERE detail LIKE '%baseline%'`)
			if n != tc.wantHealth {
				t.Errorf("%d health rows mention the baseline, want %d", n, tc.wantHealth)
			}
		})
	}
}

// pre_roll_s and post_roll_s are read where they are used, not kept from
// startup, so a clip saved after a change has the length the owner asked for.
func TestReloadedRollLengthsReachTheClip(t *testing.T) {
	e := newEnvWith(t, 0, func(c *config.Config) {
		quickBaseline(c)
		c.ThresholdDB = 15
		c.PreRoll = 0
		c.PostRoll = 0
	})
	x := background(24, 14)
	louder(x, 10, 3, 20)

	next := e.settings
	next.PreRoll = 8 * time.Second
	next.PostRoll = 4 * time.Second
	e.run(t, x, map[int]func(){90: func() { e.pipe.Reload(next) }}) // at 9 s

	got := storedEvents(t, e.query(t))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	// About 3 s of event, 8 s before it, and 4 s after it.
	if got[0].clipMS < 14000 {
		t.Errorf("the clip is %d ms long, want at least 14000: the roll lengths came from startup", got[0].clipMS)
	}
	if got[0].truncated != 0 {
		t.Errorf("the clip is marked truncated; the audio for it should all be there")
	}
}

// An event longer than the 30 s audio buffer keeps its start only because the
// DSP loop holds the audio from the pre-roll onward. That hold has to use the
// pre-roll in force now, not the one from startup, or the audio before the
// event is dropped while the event is still running.
func TestAReloadedPreRollHoldsTheAudioOfALongEvent(t *testing.T) {
	e := newEnvWith(t, 0, func(c *config.Config) {
		quickBaseline(c)
		c.ThresholdDB = 15
		c.PreRoll = 0
		c.PostRoll = 0
	})
	x := background(70, 15)
	louder(x, 20, 35, 20) // 35 s, longer than the buffer

	next := e.settings
	next.PreRoll = 8 * time.Second
	next.PostRoll = 2 * time.Second
	e.run(t, x, map[int]func(){190: func() { e.pipe.Reload(next) }}) // at 19 s

	got := storedEvents(t, e.query(t))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	// 8 s before the event, 35 s of event, and 2 s after it.
	if got[0].clipMS < 44000 || got[0].truncated != 0 {
		t.Errorf("the clip is %d ms long and truncated %d; want at least 44000 ms and not truncated: "+
			"the hold used the pre-roll from startup", got[0].clipMS, got[0].truncated)
	}
}
