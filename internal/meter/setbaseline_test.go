package meter

import (
	"testing"
	"time"
)

// baselineN returns the bin count the last frame reported.
func baselineN(t *testing.T, r *recorder) int {
	t.Helper()
	if len(r.frames) == 0 {
		t.Fatal("no frames yet")
	}
	return r.frames[len(r.frames)-1].BaselineN
}

// The ring holds a fixed window of seconds, so a new window or percentile
// cannot re-use the old history. The count starts again, and the detector
// waits for min_baseline_s of new seconds before it acts.
func TestSetBaselineStartsTheHistoryAgain(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	// Five seconds of audio close four bins; the fifth second is still open.
	m.Process(tone(1000, -20, 5), t0)
	if got := baselineN(t, r); got != 4 {
		t.Fatalf("after 5 s the baseline covers %d bins, want 4", got)
	}

	if err := m.SetBaseline(120, 25); err != nil {
		t.Fatalf("SetBaseline: %v", err)
	}
	m.Process(tone(1000, -20, 2), t0.Add(5*time.Second))
	if got := baselineN(t, r); got != 2 {
		t.Errorf("after the change and 2 more seconds the baseline covers %d bins, want 2", got)
	}
}

// A percentile change alone also starts the history again, for the same
// reason: the stored seconds are the same, but the answer is not.
func TestSetBaselineChangesThePercentile(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	// Ten seconds that are not all the same level, so the percentile matters.
	for i := range 10 {
		db := -40.0
		if i%2 == 0 {
			db = -20.0
		}
		m.Process(tone(1000, db, 1), t0.Add(time.Duration(i)*time.Second))
	}
	quietAt10 := r.bins[len(r.bins)-1].Baseline

	if err := m.SetBaseline(600, 90); err != nil {
		t.Fatalf("SetBaseline: %v", err)
	}
	for i := 10; i < 20; i++ {
		db := -40.0
		if i%2 == 0 {
			db = -20.0
		}
		m.Process(tone(1000, db, 1), t0.Add(time.Duration(i)*time.Second))
	}
	loudAt90 := r.bins[len(r.bins)-1].Baseline

	// The same seconds at the 90th percentile read far above the 10th.
	if loudAt90-quietAt10 < 15 {
		t.Errorf("baseline at the 90th percentile is %.1f dB, at the 10th %.1f dB; want at least 15 dB between them",
			loudAt90, quietAt10)
	}
}

// Bad values are refused and change nothing.
func TestSetBaselineRefusesBadValues(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(tone(1000, -20, 5), t0)

	for name, args := range map[string][2]float64{
		"window 0":        {0, 10},
		"window negative": {-1, 10},
		"percentile 0":    {600, 0},
		"percentile 101":  {600, 101},
	} {
		if err := m.SetBaseline(int(args[0]), args[1]); err == nil {
			t.Errorf("%s: SetBaseline returned no error", name)
		}
	}
	// The history from before is still there.
	m.Process(tone(1000, -20, 1), t0.Add(5*time.Second))
	if got := baselineN(t, r); got != 5 {
		t.Errorf("the baseline covers %d bins after the refused changes, want 5", got)
	}
}
