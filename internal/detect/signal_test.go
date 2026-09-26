package detect

import (
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/testsignal"
)

// SPEC.md section 11: audio-level detection and classification. Each signal is
// pink noise at -60 dBFS with 80 Hz thumps at -6 dBFS peak. The first 40 s
// are background only, because the detector needs 30 s of baseline.
const (
	noiseRMS  = 0.001
	thumpPeak = 0.5
	warmup    = 40.0
)

func noise(seconds float64, seed uint64) []float64 {
	return testsignal.Pink(int(seconds*testsignal.Rate), noiseRMS, seed)
}

// detectAudio runs audio through the meter and the detector in 100 ms chunks.
func detectAudio(t *testing.T, x []float64) []Event {
	t.Helper()
	var events []Event
	cfg := DefaultConfig()
	cfg.OnEvent = func(e Event) { events = append(events, e) }
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m, err := meter.New(meter.Config{
		SampleRate:      meter.SampleRate,
		SensitivityDBFS: -13,
		Calibration:     meter.NoCalibration{},
		OnFrame:         d.Frame,
		OnEnvelope:      d.Envelope,
	})
	if err != nil {
		t.Fatal(err)
	}
	const chunk = meter.SampleRate / 10
	for i := 0; i < len(x); i += chunk {
		end := min(i+chunk, len(x))
		m.Process(x[i:end], t0.Add(time.Duration(i)*time.Second/meter.SampleRate))
	}
	m.Flush()
	d.Flush()
	return events
}

func thumpTimes(start float64, gaps ...float64) []float64 {
	at := []float64{start}
	for _, g := range gaps {
		at = append(at, at[len(at)-1]+g)
	}
	return at
}

func repeat(v float64, n int) []float64 {
	g := make([]float64, n)
	for i := range g {
		g[i] = v
	}
	return g
}

func TestSteadyNoiseGivesNoEvent(t *testing.T) {
	if events := detectAudio(t, noise(90, 1)); len(events) != 0 {
		t.Fatalf("steady pink noise gave %d events, want 0: %+v", len(events), events)
	}
}

func TestAudioImpactsAreDetectedAndClassified(t *testing.T) {
	tests := []struct {
		name  string
		times []float64
		want  Class
	}{
		{"2.5 Hz thumps for 8 s are running", thumpTimes(warmup, repeat(0.4, 19)...), Running},
		{"thumps 2 s apart are jumping", thumpTimes(warmup, 2, 2, 2), Jumping},
		{"irregular thumps are stomping", thumpTimes(warmup, 0.3, 0.7, 0.45, 0.9, 0.25, 0.6, 0.35, 0.8, 0.55), Stomping},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x := noise(warmup+30, uint64(10+i))
			testsignal.AddThumps(x, thumpPeak, tc.times...)
			events := detectAudio(t, x)
			if len(events) != 1 {
				t.Fatalf("got %d events, want 1: %+v", len(events), summarize(events))
			}
			e := events[0]
			first := t0.Add(time.Duration(tc.times[0] * float64(time.Second)))
			if d := e.Start.Sub(first); d < -200*time.Millisecond || d > 200*time.Millisecond {
				t.Errorf("event starts %v from the first thump, want within +/-200 ms", d)
			}
			if e.Class != tc.want {
				t.Errorf("class = %q (confidence %.2f), want %q", e.Class, e.Confidence, tc.want)
			}
			// An 80 Hz thump puts far more energy in 20-120 Hz than above
			// 500 Hz. This ratio supports a structure-borne source.
			if e.LowHighRatioDB < 10 {
				t.Errorf("low - high band = %.1f dB, want above 10 dB", e.LowHighRatioDB)
			}
		})
	}
}

type eventSummary struct {
	Start, End time.Duration
	Class      Class
	LAmax      float64
	Baseline   float64
}

func summarize(events []Event) []eventSummary {
	var s []eventSummary
	for _, e := range events {
		s = append(s, eventSummary{e.Start.Sub(t0), e.End.Sub(t0), e.Class, e.LAmax, e.BaselineAtTrigger})
	}
	return s
}

// SPEC.md section 11: the baseline follows a slow ambient ramp without triggering.
// Frames rise 10 dB over an hour and sit 2 dB above the per-second level, as
// Fast weighting does on noise. The baseline lags by 1.5 dB, far under 15 dB.
func TestSlowAmbientRampDoesNotTrigger(t *testing.T) {
	var events []Event
	cfg := DefaultConfig()
	cfg.OnEvent = func(e Event) { events = append(events, e) }
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := meter.NewBaseline(meter.DefaultBaselineWindow, meter.DefaultBaselinePercentile)
	var baseline float64
	for s := 0; s < 3600; s++ {
		level := 40 + 10*float64(s)/3600
		for k := 0; k < 10; k++ {
			d.Frame(meter.Frame{
				Start: t0.Add(time.Duration(s)*time.Second + time.Duration(k)*100*time.Millisecond), Samples: 4800,
				LAeq: level, LAmax: level + 2, LowBand: level, HighBand: level,
				Baseline: baseline, BaselineN: b.Len(),
			})
		}
		baseline = b.Add(level)
	}
	d.Flush()
	if len(events) != 0 {
		t.Fatalf("slow ramp gave %d events, want 0", len(events))
	}
}
