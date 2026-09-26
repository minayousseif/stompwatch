package web

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/store"
)

// addCaptureSettings records one row of the capture-settings history at t0
// plus an offset, the way the collector does at start.
func (e *env) addCaptureSettings(offset time.Duration, change ...func(*store.CaptureSettings)) {
	e.t.Helper()
	cs := store.CaptureSettings{
		From:            t0.Add(offset),
		SensitivityDBFS: -13,
		UncertaintyDB:   2,
		Source:          store.SensitivityDatasheet,
		CaptureDevice:   "hw:EM01,0",
	}
	for _, fn := range change {
		fn(&cs)
	}
	if _, err := e.store.RecordCaptureSettings(e.t.Context(), cs); err != nil {
		e.t.Fatal(err)
	}
}

// The System screen answers for today, so the levels block is what is in
// force now (SPEC.md section 15 decision 23).
func TestSystemSaysHowWellTheLevelsAreKnown(t *testing.T) {
	e := newEnv(t, func(_ *Config, file *config.Config) {
		file.SensitivityDBFS = -12.47
		file.SensitivitySource = config.SensitivityMeasured
		file.SensitivityUncertaintyDB = 0.5
		file.SensitivityMeasuredOn = "2026-10-03"
		file.SensitivityReference = "B and K 4231 at 94 dB SPL"
	})
	levels := object(t, e.getJSON("/api/system"), "levels")
	if got := num(t, levels, "uncertainty_db"); got != 0.5 {
		t.Errorf("uncertainty_db = %v, want 0.5", got)
	}
	if got := str(t, levels, "source"); got != "measured" {
		t.Errorf("source = %q, want measured", got)
	}
	if got := str(t, levels, "measured_on"); got != "2026-10-03" {
		t.Errorf("measured_on = %q, want 2026-10-03", got)
	}
	if got := str(t, levels, "reference"); got != "B and K 4231 at 94 dB SPL" {
		t.Errorf("reference = %q", got)
	}
}

// A fresh install reports the datasheet figure and its per-unit tolerance,
// not an exact number.
func TestSystemReportsTheDatasheetToleranceByDefault(t *testing.T) {
	e := newEnv(t)
	levels := object(t, e.getJSON("/api/system"), "levels")
	if got := num(t, levels, "uncertainty_db"); got != 2 {
		t.Errorf("uncertainty_db = %v, want 2", got)
	}
	if got := str(t, levels, "source"); got != "datasheet" {
		t.Errorf("source = %q, want datasheet", got)
	}
	for _, key := range []string{"measured_on", "reference"} {
		if got := str(t, levels, key); got != "" {
			t.Errorf("%s = %q, want empty for the datasheet figure", key, got)
		}
	}
}

// An event cites the settings in force when it was recorded, not the ones in
// force now. Reprinting an event from three months ago must not claim
// today's calibration.
func TestAnEventCitesTheSettingsItWasRecordedUnder(t *testing.T) {
	e := newEnv(t)
	e.addCaptureSettings(-2 * time.Hour)
	old := e.addEvent(-time.Hour, time.Second, 60, detect.Running)
	e.addCaptureSettings(-30*time.Minute, func(cs *store.CaptureSettings) {
		cs.SensitivityDBFS = -12.47
		cs.UncertaintyDB = 0.5
		cs.Source = store.SensitivityMeasured
		cs.MeasuredOn = "2026-10-03"
		cs.Reference = "B and K 4231 at 94 dB SPL"
		cs.CalibrationFile = "em01.txt"
		cs.CalibrationOffsetDB = 0.5
	})
	recent := e.addEvent(-time.Minute, time.Second, 60, detect.Running)

	before := object(t, e.getJSON("/api/events/"+strconv.FormatInt(old, 10)), "capture")
	if got := num(t, before, "sensitivity_dbfs"); got != -13 {
		t.Errorf("the older event says %v dBFS; it borrowed the newer row", got)
	}
	if got := num(t, before, "uncertainty_db"); got != 2 {
		t.Errorf("the older event says plus or minus %v dB, want 2", got)
	}
	if got := str(t, before, "source"); got != "datasheet" {
		t.Errorf("the older event says source %q, want datasheet", got)
	}
	if got := str(t, before, "calibration"); got != "" {
		t.Errorf("the older event names calibration file %q; there was none", got)
	}

	after := object(t, e.getJSON("/api/events/"+strconv.FormatInt(recent, 10)), "capture")
	if got := num(t, after, "sensitivity_dbfs"); got != -12.47 {
		t.Errorf("the newer event says %v dBFS, want -12.47", got)
	}
	if got := num(t, after, "uncertainty_db"); got != 0.5 {
		t.Errorf("the newer event says plus or minus %v dB, want 0.5", got)
	}
	if got := str(t, after, "measured_on"); got != "2026-10-03" {
		t.Errorf("measured_on = %q", got)
	}
	if got := str(t, after, "calibration"); got != "em01.txt" {
		t.Errorf("calibration = %q, want em01.txt", got)
	}
	if got := num(t, after, "calibration_offset_db"); got != 0.5 {
		t.Errorf("calibration_offset_db = %v, want 0.5", got)
	}
}

// An event recorded before the history began has no settings to cite. It
// must say so, not borrow today's: that is the fault the history was added
// to close.
func TestAnEventOlderThanTheHistoryCitesNothing(t *testing.T) {
	e := newEnv(t)
	old := e.addEvent(-2*time.Hour, time.Second, 60, detect.Running)
	e.addCaptureSettings(-time.Hour)

	m := e.getJSON("/api/events/" + strconv.FormatInt(old, 10))
	v, ok := m["capture"]
	if !ok {
		t.Fatalf("the detail has no capture block at all: %v", m)
	}
	if v != nil {
		t.Errorf("capture = %v, want null for an event older than the history", v)
	}
}

// No response names a place on disk. The history stores the base name, and
// the handler must not be able to put a path back.
func TestTheEventCaptureBlockNamesNoPath(t *testing.T) {
	e := newEnv(t)
	e.addCaptureSettings(-time.Hour, func(cs *store.CaptureSettings) {
		cs.CalibrationFile = "/etc/stompwatch/cal/em01.txt"
	})
	id := e.addEvent(-time.Minute, time.Second, 60, detect.Running)
	body := e.get("/api/events/" + strconv.FormatInt(id, 10)).Body.String()
	if strings.Contains(body, "/etc/stompwatch") {
		t.Errorf("the response names a directory:\n%s", body)
	}
	capture := object(t, e.getJSON("/api/events/"+strconv.FormatInt(id, 10)), "capture")
	if got := str(t, capture, "calibration"); got != "em01.txt" {
		t.Errorf("calibration = %q, want em01.txt", got)
	}
}

// A CSV has no comment syntax, so the uncertainty is a column on every row.
// It comes from that event's own settings, so one file may carry rows
// measured under different calibrations.
func TestCSVCarriesTheUncertaintyOfEachRow(t *testing.T) {
	e := newEnv(t)
	e.addCaptureSettings(-2 * time.Hour)
	old := e.addEvent(-time.Hour, time.Second, 60, detect.Running)
	e.addCaptureSettings(-30*time.Minute, func(cs *store.CaptureSettings) {
		cs.UncertaintyDB = 0.5
		cs.Source = store.SensitivityMeasured
		cs.MeasuredOn = "2026-10-03"
	})
	recent := e.addEvent(-time.Minute, time.Second, 60, detect.Running)
	e.addReview(old, "verified", "", "me@example.com")
	e.addReview(recent, "verified", "", "me@example.com")

	rows := readCSV(t, e.get(csvURL(-3*time.Hour, time.Hour, "")).Body.String())
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want the header and two events: %v", len(rows), rows)
	}
	unc, src := columnAt(t, rows[0], "level_uncertainty_db"), columnAt(t, rows[0], "sensitivity_source")
	if rows[1][unc] != "2.0" || rows[1][src] != "datasheet" {
		t.Errorf("the older row says %q %q, want 2.0 datasheet", rows[1][unc], rows[1][src])
	}
	if rows[2][unc] != "0.5" || rows[2][src] != "measured" {
		t.Errorf("the newer row says %q %q, want 0.5 measured", rows[2][unc], rows[2][src])
	}
}

// An event older than the history has no uncertainty to state. The cell is
// empty, which says the settings were not recorded; a number there would be
// one nobody measured.
func TestCSVLeavesTheUncertaintyEmptyWhenItIsNotRecorded(t *testing.T) {
	e := newEnv(t)
	old := e.addEvent(-2*time.Hour, time.Second, 60, detect.Running)
	e.addReview(old, "verified", "", "me@example.com")
	e.addCaptureSettings(-time.Hour)

	rows := readCSV(t, e.get(csvURL(-3*time.Hour, time.Hour, "")).Body.String())
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the header and one event: %v", len(rows), rows)
	}
	unc, src := columnAt(t, rows[0], "level_uncertainty_db"), columnAt(t, rows[0], "sensitivity_source")
	if rows[1][unc] != "" || rows[1][src] != "" {
		t.Errorf("the row says %q %q, want both empty", rows[1][unc], rows[1][src])
	}
}

// columnAt finds a column by name in the header row.
func columnAt(t *testing.T, header []string, name string) int {
	t.Helper()
	for i, got := range header {
		if got == name {
			return i
		}
	}
	t.Fatalf("the header has no %q column: %v", name, header)
	return -1
}

// The picture leaves the dashboard and has to stand on its own, so the
// caption says the plus or minus on every level in it.
func TestTheTimelineCaptionSaysTheUncertainty(t *testing.T) {
	text := caption(t0, t0.Add(12*time.Hour), 1, []string{"verified"}, time.UTC, 2)
	if !strings.Contains(text, "levels +/- 2.0 dB") {
		t.Errorf("the caption %q does not say the uncertainty", text)
	}
}
