package pipeline

import (
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
)

// pauseSpan is a daily pause from h1:m1 to h2:m2, as a one-window list.
func pauseSpan(h1, m1, h2, m2 int) config.PauseWindows {
	return config.PauseWindows{{
		Start: time.Duration(h1)*time.Hour + time.Duration(m1)*time.Minute,
		End:   time.Duration(h2)*time.Hour + time.Duration(m2)*time.Minute,
	}}
}

// wall is an instant on the test day.
func wall(h, m, s int) time.Time { return time.Date(2026, 9, 11, h, m, s, 0, time.UTC) }

// A clip must hold nothing from inside the pause. Its post-roll would run
// on past the start of the pause, so it stops there instead.
func TestClipWindowStopsAtThePauseStart(t *testing.T) {
	from, to := clipWindow(pauseSpan(22, 0, 7, 0), wall(21, 59, 40), wall(21, 59, 45), 30*time.Second, 30*time.Second)
	if !from.Equal(wall(21, 59, 10)) || !to.Equal(wall(22, 0, 0)) {
		t.Errorf("window = %s to %s, want 21:59:10 to 22:00:00", from.Format(time.TimeOnly), to.Format(time.TimeOnly))
	}
}

// The same at the other end: the pre-roll of the first event after a pause
// would reach back into it, so the clip starts where the pause ended.
func TestClipWindowStartsAtThePauseEnd(t *testing.T) {
	from, to := clipWindow(pauseSpan(22, 0, 7, 0), wall(7, 0, 5), wall(7, 0, 8), 30*time.Second, 30*time.Second)
	if !from.Equal(wall(7, 0, 0)) || !to.Equal(wall(7, 0, 38)) {
		t.Errorf("window = %s to %s, want 07:00:00 to 07:00:38", from.Format(time.TimeOnly), to.Format(time.TimeOnly))
	}
}

// A clip that would run into the second window is cut there too.
func TestClipWindowIsCutByEveryWindow(t *testing.T) {
	pause := config.PauseWindows{{Start: 22 * time.Hour, End: 7 * time.Hour}, {Start: 12 * time.Hour, End: 13 * time.Hour}}
	from, to := clipWindow(pause, wall(11, 59, 40), wall(11, 59, 45), 30*time.Second, 30*time.Second)
	if !to.Equal(wall(12, 0, 0)) {
		t.Errorf("the clip runs to %s, want it cut at the 12:00 window", to.Format("15:04:05"))
	}
	if !from.Equal(wall(11, 59, 10)) {
		t.Errorf("the clip starts at %s, want the full pre-roll", from.Format("15:04:05"))
	}
}

// Away from the pause, and with no pause at all, the clip is the event with
// its full pre-roll and post-roll, as before.
func TestClipWindowIsUnchangedAwayFromThePause(t *testing.T) {
	for name, pause := range map[string]config.PauseWindows{
		"a pause at night": pauseSpan(22, 0, 7, 0),
		"no pause":         nil,
	} {
		from, to := clipWindow(pause, wall(21, 59, 40), wall(21, 59, 45), 30*time.Second, 30*time.Second)
		want := wall(22, 0, 15)
		if name == "a pause at night" {
			want = wall(22, 0, 0)
		}
		if !from.Equal(wall(21, 59, 10)) || !to.Equal(want) {
			t.Errorf("%s: window = %s to %s", name, from.Format(time.TimeOnly), to.Format(time.TimeOnly))
		}
	}
	from, to := clipWindow(pauseSpan(22, 0, 7, 0), wall(12, 0, 0), wall(12, 0, 3), 30*time.Second, 30*time.Second)
	if !from.Equal(wall(11, 59, 30)) || !to.Equal(wall(12, 0, 33)) {
		t.Errorf("midday window = %s to %s, want 11:59:30 to 12:00:33", from.Format(time.TimeOnly), to.Format(time.TimeOnly))
	}
}

// Inside the pause no event opens, however loud it is. The level of every
// second is still stored, so the record has no hole, and the pause is in the
// health log, so the missing events have a reason.
func TestNoEventOpensInsideThePause(t *testing.T) {
	e := newEnvWith(t, 16, func(c *config.Config) {
		quickBaseline(c)
		c.RecordingPause = pauseSpan(3, 0, 3, 1) // t0 is 03:00:00
	})
	x := background(20, 11)
	louder(x, 10, 3, 20)

	e.run(t, x, nil)

	db := e.query(t)
	if got := storedEvents(t, db); len(got) != 0 {
		t.Errorf("got %d events inside the pause, want 0: %+v", len(got), got)
	}
	if n := count(t, db, `SELECT count(*) FROM samples_1s`); n < 19 {
		t.Errorf("samples_1s has %d rows, want the whole 20 s measured through the pause", n)
	}
	if n := count(t, db, `SELECT count(*) FROM system_health WHERE kind = 'recording_pause'`); n < 1 {
		t.Errorf("no recording_pause row in the health log; the missing events would have no reason")
	}
}

// The same audio with the pause at another hour must give the event, or the
// test above would pass whatever the pause did.
func TestTheSameNoiseOutsideThePauseIsAnEvent(t *testing.T) {
	e := newEnvWith(t, 16, func(c *config.Config) {
		quickBaseline(c)
		c.RecordingPause = pauseSpan(4, 0, 5, 0)
	})
	x := background(20, 11)
	louder(x, 10, 3, 20)

	e.run(t, x, nil)

	if got := storedEvents(t, e.query(t)); len(got) != 1 {
		t.Fatalf("got %d events outside the pause, want 1: %+v", len(got), got)
	}
}

// An event that is still open when the pause begins ends there, and its
// clips stop there: the post-roll must not carry sound from inside the
// pause onto the disk. The noise runs from 57 s to 65 s and the pause
// begins at 60 s.
func TestAnEventOpenWhenThePauseBeginsStopsThere(t *testing.T) {
	e := newEnvWith(t, 16, func(c *config.Config) {
		quickBaseline(c)
		c.RecordingPause = pauseSpan(3, 1, 3, 2)
	})
	fv := &fakeVideo{}
	e.pipe.cfg.Video = fv
	x := background(70, 11)
	louder(x, 57, 8, 20)

	e.run(t, x, nil)

	got := storedEvents(t, e.query(t))
	if len(got) != 1 {
		t.Fatalf("got %d events, want the one that began before the pause: %+v", len(got), got)
	}
	if got[0].endS > 60 {
		t.Errorf("the event ends at %.2f s, inside the pause that began at 60 s", got[0].endS)
	}
	fv.mu.Lock()
	defer fv.mu.Unlock()
	if len(fv.calls) != 1 {
		t.Fatalf("video Save was called %d times, want 1", len(fv.calls))
	}
	if want := t0.Add(60 * time.Second); !fv.calls[0].to.Equal(want) {
		t.Errorf("the video clip runs to %s, want it to stop at the pause, %s",
			fv.calls[0].to.Format(time.TimeOnly), want.Format(time.TimeOnly))
	}
}

// An event still open when the pause begins is stored when the pause
// begins, not when it ends. Held open, it would pin the start of its audio
// in the recorder for the whole pause, and the buffer would grow for hours.
// The noise runs from 57 s to 65 s, the pause begins at 60 s, and the check
// is made 30 s into the pause, long before the audio ends.
func TestAnEventOpenWhenThePauseBeginsIsStoredStraightAway(t *testing.T) {
	e := newEnvWith(t, 0, func(c *config.Config) {
		quickBaseline(c)
		c.RecordingPause = pauseSpan(3, 1, 3, 3)
	})
	x := background(100, 11)
	louder(x, 57, 8, 20)

	e.run(t, x, map[int]func(){900: func() { // at 90 s
		db := e.query(t)
		deadline := time.Now().Add(3 * time.Second)
		for count(t, db, `SELECT count(*) FROM events`) == 0 {
			if time.Now().After(deadline) {
				t.Errorf("30 s into the pause the event that was open when it began is not stored; it is being held open")
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}})
}
