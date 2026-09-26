package verify

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/meter"
)

func defaults() config.Config {
	c := config.Default()
	c.ExpectedCaptureGain = "none"
	return c
}

func TestRunPassesWithDefaultSettings(t *testing.T) {
	checks := Run(defaults(), meter.NoCalibration{})
	if len(checks) < 25 {
		t.Fatalf("Run returned %d checks, want at least 25", len(checks))
	}
	names := map[string]bool{}
	for _, c := range checks {
		names[c.Name] = true
		if !c.Pass() {
			t.Errorf("%s: got %.3f %s, want %.3f to %.3f", c.Name, c.Got, c.Unit, c.Min, c.Max)
		}
	}
	for _, want := range []string{
		"A-weighting at 31.5 Hz", "A-weighting at 16000 Hz", "A-weighting at 1 kHz is 0 dB",
		"clip low-pass at 1000 Hz", "clip filter rejection above 1000 Hz", "clip filter passband to 500 Hz",
		"sine at sensitivity level", "level linearity over 20 dB", "Fast decay after 1 s",
		"S24_3LE 0x800000",
	} {
		if !names[want] {
			t.Errorf("no check named %q", want)
		}
	}
}

// verify-dsp must check the cutoff that is actually configured, and its
// report must name it. A report that always said 500 Hz would pass on a box
// running at 1 kHz and tell the owner nothing (SPEC.md section 15 decision 18).
func TestRunChecksTheConfiguredClipCutoff(t *testing.T) {
	s := defaults()
	s.ClipLowpassHz = 1000
	var buf bytes.Buffer
	checks := Run(s, meter.NoCalibration{})
	if Report(&buf, checks) > 0 {
		t.Errorf("checks failed at a 1000 Hz cutoff:\n%s", buf.String())
	}
	names := map[string]bool{}
	for _, c := range checks {
		names[c.Name] = true
	}
	for _, want := range []string{
		"clip low-pass at 1000 Hz", "clip filter rejection above 1000 Hz",
		"clip filter passband to 500 Hz",
	} {
		if !names[want] {
			t.Errorf("no check named %q; the report does not name the cutoff in force:\n%s", want, buf.String())
		}
	}
	for name := range names {
		if strings.Contains(name, "clip") && strings.Contains(name, "500 Hz") &&
			!strings.Contains(name, "passband") {
			t.Errorf("check %q still names 500 Hz at a 1000 Hz cutoff", name)
		}
	}
}

// The rejection check must really drive the filter above its cutoff. A
// 700 Hz tone survives a 1000 Hz clip filter, so a check that measured the
// same frequencies whatever the cutoff would report a pass that is not true
// of the band it claims.
func TestClipRejectionFollowsTheCutoff(t *testing.T) {
	if got := clipGainDB(1000, 700); got < -1 {
		t.Errorf("a 1000 Hz clip filter at 700 Hz = %.1f dB, want about 0 dB", got)
	}
	if got := clipGainDB(500, 700); got > -90 {
		t.Errorf("a 500 Hz clip filter at 700 Hz = %.1f dB, want at most -90 dB", got)
	}
}

// verify-dsp checks the live configuration. With sensitivity -10 dBFS and a
// calibration that removes +1 dB, a sine at -10 dBFS must read 94 - 1 = 93.
func TestRunUsesConfiguredSensitivityAndCalibration(t *testing.T) {
	s := defaults()
	s.SensitivityDBFS = -10
	cal, err := meter.NewScalarCalibration([]meter.CalPoint{{FreqHz: 1000, MagDB: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range Run(s, cal) {
		if c.Name != "sine at sensitivity level" {
			continue
		}
		if math.Abs(c.Got-93) > 0.05 || !c.Pass() {
			t.Fatalf("sine at sensitivity level: got %.3f, range %.3f to %.3f; want 93.00 and a pass", c.Got, c.Min, c.Max)
		}
		return
	}
	t.Fatal("no check named \"sine at sensitivity level\"")
}

func TestReportPrintsEachCheckAndCountsFailures(t *testing.T) {
	var buf bytes.Buffer
	failed := Report(&buf, []Check{
		{Name: "good", Got: 1, Min: 0, Max: 2, Unit: "dB"},
		{Name: "bad", Got: 5, Min: 0, Max: 2, Unit: "dB"},
		{Name: "not a number", Got: math.NaN(), Min: 0, Max: 2, Unit: "dB"},
	})
	out := buf.String()
	if failed != 2 {
		t.Errorf("Report returned %d failures, want 2", failed)
	}
	for _, want := range []string{"PASS  good", "FAIL  bad", "FAIL  not a number", "2 of 3 checks failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not contain %q:\n%s", want, out)
		}
	}
}
