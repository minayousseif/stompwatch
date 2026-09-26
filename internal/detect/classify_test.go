package detect

import (
	"math"
	"testing"
	"time"
)

// Synthetic envelopes at 100 Hz: a constant background of 0.01 with bumps.
// A bump is a raised cosine, 10 samples (100 ms) wide, height 1.

func background(n int) []float64 {
	env := make([]float64, n)
	for i := range env {
		env[i] = 0.01
	}
	return env
}

func addBump(env []float64, at int) {
	for k := 0; k < 10; k++ {
		if i := at + k; i < len(env) {
			env[i] += 0.5 * (1 - math.Cos(2*math.Pi*float64(k)/10))
		}
	}
}

func train(n, period int) []float64 {
	env := background(n)
	for at := 0; at < n; at += period {
		addBump(env, at)
	}
	return env
}

func bumpsAt(n int, at ...int) []float64 {
	env := background(n)
	for _, a := range at {
		addBump(env, a)
	}
	return env
}

func TestClassify(t *testing.T) {
	irregular := func() []float64 {
		env := background(700)
		at := 20
		for _, gap := range []int{30, 70, 45, 90, 25, 60, 35, 80, 55} {
			addBump(env, at)
			at += gap
		}
		addBump(env, at)
		return env
	}

	tests := []struct {
		name string
		env  []float64
		want Class
	}{
		{"2.5 Hz for 6 s is running", train(600, 40), Running},
		{"3 Hz for 6 s is running", train(600, 34), Running},
		{"2 Hz for 6 s is running", train(600, 50), Running},
		{"2.5 Hz for only 2 s is unknown", train(200, 40), Unknown},
		{"4 Hz cadence is not running", train(600, 25), Unknown},
		{"1.5 Hz cadence is not running", train(900, 67), Unknown},
		{"impacts 2 s apart are jumping", bumpsAt(800, 50, 250, 450, 650), Jumping},
		{"irregular impacts are stomping", irregular(), Stomping},
		{"one impact is unknown", bumpsAt(300, 100), Unknown},
		{"two impacts 0.5 s apart are unknown", bumpsAt(300, 100, 150), Unknown},
		{"flat envelope is unknown", background(600), Unknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, conf := Classify(tc.env)
			if got != tc.want {
				t.Errorf("class = %q (confidence %.2f), want %q", got, conf, tc.want)
			}
			if conf < 0 || conf > 1 || math.IsNaN(conf) {
				t.Errorf("confidence = %v, want 0 to 1", conf)
			}
			if got == Unknown && conf != 0 {
				t.Errorf("unknown has confidence %v, want 0", conf)
			}
		})
	}
}

// A slow swell 3 s wide has high autocorrelation at every short lag. Without
// a check for a real peak, it would look like a 2-3 Hz cadence.
func TestClassifySlowSwellIsNotRunning(t *testing.T) {
	env := background(600)
	for k := 0; k < 300; k++ {
		env[150+k] += 0.5 * (1 - math.Cos(2*math.Pi*float64(k)/300))
	}
	if got, _ := Classify(env); got == Running {
		t.Fatalf("slow swell classified as running")
	}
}

func TestClassifyRunningConfidenceReflectsRegularity(t *testing.T) {
	_, conf := Classify(train(600, 40))
	if conf < 0.5 {
		t.Fatalf("confidence for a clean 2.5 Hz train = %.2f, want >= 0.5", conf)
	}
}

func TestClassifyEventLabelsAirborneWhenTheHighBandIsAsLoud(t *testing.T) {
	env := bumpsAt(300, 50)
	c := Context{RatioDB: -3, JumpDB: 20, RiseDB: 15, Duration: 2 * time.Second, Known: true}
	if class, _ := ClassifyEvent(env, c); class != Airborne {
		t.Errorf("class = %q, want airborne at a ratio of -3 dB", class)
	}
	c.RatioDB = 0
	if class, _ := ClassifyEvent(env, c); class != Airborne {
		t.Errorf("class = %q, want airborne at a ratio of exactly 0 dB", class)
	}
}

func TestClassifyEventLabelsSteadyWhenTheLevelWasAlreadyThereOrRoseSlowly(t *testing.T) {
	env := bumpsAt(300, 50)
	small := Context{RatioDB: 14, JumpDB: 2, RiseDB: 15, Duration: 2 * time.Second, Known: true}
	if class, _ := ClassifyEvent(env, small); class != Steady {
		t.Errorf("class = %q, want steady for a 2 dB jump", class)
	}
	slow := Context{RatioDB: 14, JumpDB: 16, RiseDB: 4, CrestDB: 2, Duration: 2 * time.Second, Known: true}
	if class, _ := ClassifyEvent(env, slow); class != Steady {
		t.Errorf("class = %q, want steady for a 4 dB per second ramp with a flat top", class)
	}
}

// A furniture drag rises slowly but its peak stands well over its own mean.
// A fan's peak is within 2.5 dB of its mean. The crest tells them apart when
// the rise cannot.
func TestClassifyEventPeakedSlowRiseIsNotSteady(t *testing.T) {
	env := bumpsAt(700, 300)
	drag := Context{RatioDB: 28, JumpDB: 16, RiseDB: 3.5, CrestDB: 7, Duration: 7 * time.Second, Known: true}
	if class, _ := ClassifyEvent(env, drag); class != Impact {
		t.Errorf("class = %q, want impact for a slow rise with a 7 dB crest", class)
	}
	flat := Context{RatioDB: 28, JumpDB: 16, RiseDB: 3.5, CrestDB: 4.9, Duration: 7 * time.Second, Known: true}
	if class, _ := ClassifyEvent(env, flat); class != Steady {
		t.Errorf("class = %q, want steady for a slow rise with a crest under 5 dB", class)
	}
}

func TestClassifyEventLabelsAShortHitWithNoCadenceAsImpact(t *testing.T) {
	env := bumpsAt(100, 40) // one bump: no cadence, under 2 peaks
	c := Context{RatioDB: 36, JumpDB: 17, RiseDB: 13, Duration: 800 * time.Millisecond, Known: true}
	class, conf := ClassifyEvent(env, c)
	if class != Impact {
		t.Errorf("class = %q, want impact", class)
	}
	if conf <= 0 || conf > 1 {
		t.Errorf("confidence = %v, want in (0, 1]", conf)
	}
}

func TestClassifyEventKeepsCadenceClassesThatPassTheGates(t *testing.T) {
	env := train(700, 40) // 2.5 Hz for 7 s: running
	c := Context{RatioDB: 10, JumpDB: 15, RiseDB: 12, Duration: 7 * time.Second, Known: true}
	if class, _ := ClassifyEvent(env, c); class != Running {
		t.Errorf("class = %q, want running", class)
	}
}

func TestClassifyEventLeavesALongEventWithNoCadenceUnknown(t *testing.T) {
	env := bumpsAt(1500, 40)
	c := Context{RatioDB: 10, JumpDB: 15, RiseDB: 12, Duration: 15 * time.Second, Known: true}
	if class, _ := ClassifyEvent(env, c); class != Unknown {
		t.Errorf("class = %q, want unknown for a 15 s event with one bump", class)
	}
}

// The steady gates use a strict "<", so a value exactly at the gate must not
// be steady.
func TestClassifyEventJumpAtTheGateIsNotSteady(t *testing.T) {
	env := bumpsAt(100, 40)
	c := Context{RatioDB: 10, JumpDB: 6, RiseDB: 20, Duration: time.Second, Known: true}
	if class, _ := ClassifyEvent(env, c); class == Steady {
		t.Errorf("class = %q, want not steady at JumpDB exactly 6", class)
	}
}

func TestClassifyEventRiseAtTheGateIsNotSteady(t *testing.T) {
	env := bumpsAt(100, 40)
	c := Context{RatioDB: 10, JumpDB: 20, RiseDB: 5, CrestDB: 1, Duration: time.Second, Known: true}
	if class, _ := ClassifyEvent(env, c); class == Steady {
		t.Errorf("class = %q, want not steady at RiseDB exactly 5", class)
	}
}

func TestClassifyEventWithoutContextUsesTheEnvelopeAlone(t *testing.T) {
	env := train(700, 40)
	// RatioDB is positive so it does not also trigger the airborne gate,
	// which applies even without Known.
	c := Context{RatioDB: 10, Known: false}
	if class, _ := ClassifyEvent(env, c); class != Running {
		t.Errorf("class = %q, want running: with no context the old rules apply", class)
	}
}
