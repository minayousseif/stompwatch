package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// captureRow is one row of the capture_settings history, read straight out
// of the database the collector wrote.
type captureRow struct {
	sensitivity, uncertainty, offset float64
	source, measuredOn, reference    string
	calibrationFile, device          string
	channel                          int
}

func newestCapture(t *testing.T, db *sql.DB) captureRow {
	t.Helper()
	var r captureRow
	err := db.QueryRow(`SELECT sensitivity_dbfs, uncertainty_db, source, measured_on, reference,
		calibration_file, calibration_offset_db, capture_device, capture_channel
		FROM capture_settings ORDER BY id DESC LIMIT 1`).
		Scan(&r.sensitivity, &r.uncertainty, &r.source, &r.measuredOn, &r.reference,
			&r.calibrationFile, &r.offset, &r.device, &r.channel)
	if err != nil {
		t.Fatalf("reading the newest capture_settings row: %v", err)
	}
	return r
}

// The collector records what the instrument was, at every start, so that an
// event can cite the settings it was measured under
// (SPEC.md section 15 decision 23).
func TestRunRecordsTheCaptureSettings(t *testing.T) {
	cfg, dir := writeConfig(t,
		"sensitivity_dbfs = -12.47",
		"sensitivity_source = measured",
		"sensitivity_measured_on = 2026-10-03",
		"sensitivity_reference = B and K 4231 at 94 dB SPL",
		"sensitivity_uncertainty_db = 0.5",
		"capture_channel = 1",
	)
	wav := thumps(t, dir)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	db := readDB(t, dir)
	if n := rows(t, db, `SELECT count(*) FROM capture_settings`); n != 1 {
		t.Fatalf("capture_settings holds %d rows, want 1", n)
	}
	got := newestCapture(t, db)
	want := captureRow{
		sensitivity: -12.47, uncertainty: 0.5, source: "measured",
		measuredOn: "2026-10-03", reference: "B and K 4231 at 94 dB SPL",
		device: "hw:EM01,0", channel: 1,
	}
	if got != want {
		t.Errorf("recorded %+v\nwant     %+v", got, want)
	}
}

// A restart that changes nothing must add no row, or the table grows by one
// every time the service restarts and the history says nothing.
func TestARestartThatChangesNothingRecordsNothing(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)
	for i := range 3 {
		if code, _, stderr := runCmd("run", "-config", cfg, "-input", wav); code != 0 {
			t.Fatalf("run %d: exit %d, stderr:\n%s", i, code, stderr)
		}
	}
	if n := rows(t, readDB(t, dir), `SELECT count(*) FROM capture_settings`); n != 1 {
		t.Errorf("three identical starts left %d rows, want 1", n)
	}
}

// A changed sensitivity opens a new row, so the events on either side of the
// change cite different settings.
func TestAChangedSensitivityRecordsANewRow(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)
	if code, _, stderr := runCmd("run", "-config", cfg, "-input", wav); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	cfg2, _ := writeConfigIn(t, dir, "sensitivity_dbfs = -12.47")
	if code, _, stderr := runCmd("run", "-config", cfg2, "-input", wav); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	db := readDB(t, dir)
	if n := rows(t, db, `SELECT count(*) FROM capture_settings`); n != 2 {
		t.Fatalf("capture_settings holds %d rows, want 2", n)
	}
	if got := newestCapture(t, db).sensitivity; got != -12.47 {
		t.Errorf("the new row says %v dBFS, want -12.47", got)
	}
}

// The row records the settings that are in force, which is the config file
// with the stored overrides merged on top and the whole thing validated. A
// row written from the file alone would say the instrument was something it
// was not for the whole of that run.
func TestTheRecordedSettingsAreTheOnesInForce(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)
	// One start, so the database exists and the config table can be written.
	if code, _, stderr := runCmd("run", "-config", cfg, "-input", wav); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	putOverride(t, dir, "capture_channel", "1")
	if code, _, stderr := runCmd("run", "-config", cfg, "-input", wav); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if got := newestCapture(t, readDB(t, dir)).channel; got != 1 {
		t.Errorf("the row says channel %d, but the override put the collector on channel 1", got)
	}
}

// A stored setting that does not validate is dropped and the file alone is
// in force. The history must record the file's value, not the value that was
// refused: writing the row before the settings are checked would put a
// setting in the record that the collector never ran with.
func TestARefusedOverrideIsNotRecorded(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)
	if code, _, stderr := runCmd("run", "-config", cfg, "-input", wav); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	// capture_channel must be 0 or 1, so this is refused by config.Validate.
	putOverride(t, dir, "capture_channel", "7")
	if code, _, stderr := runCmd("run", "-config", cfg, "-input", wav); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	db := readDB(t, dir)
	if got := newestCapture(t, db).channel; got != 0 {
		t.Errorf("the row says channel %d; the refused override reached the history", got)
	}
	if n := rows(t, db, `SELECT count(*) FROM capture_settings`); n != 1 {
		t.Errorf("a refused override opened a new row: %d rows, want 1", n)
	}
}

// The calibration file is recorded by name, never by path: no row and no
// response may name a place on disk (SPEC.md section 3).
func TestTheCalibrationFileIsRecordedByNameAndCorrection(t *testing.T) {
	dir := t.TempDir()
	cal := writeCalibration(t, dir, "em01-response.txt", -0.5)
	cfg, _ := writeConfigIn(t, dir, "calibration_file = "+cal)
	wav := thumps(t, dir)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	// The same description reaches the dashboard as capture.calibration, and
	// no response may name a place on disk. The startup line is where it can
	// be read without a server.
	if got := logAttr(t, stderr, "starting", "calibration"); got != "em01-response.txt (correction 0.50 dB)" {
		t.Errorf("the calibration is described as %q; it must name the file, not where it is", got)
	}
	got := newestCapture(t, readDB(t, dir))
	if got.calibrationFile != "em01-response.txt" {
		t.Errorf("calibration_file = %q, want em01-response.txt", got.calibrationFile)
	}
	// The file says the microphone reads 0.5 dB low across the band, so the
	// correction is plus 0.5 dB.
	if got.offset != 0.5 {
		t.Errorf("calibration_offset_db = %v, want 0.5", got.offset)
	}
}

// logAttr reads one attribute of the first JSON log line whose msg matches.
func logAttr(t *testing.T, stderr, msg, key string) string {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || rec["msg"] != msg {
			continue
		}
		v, ok := rec[key].(string)
		if !ok {
			t.Fatalf("the %q line has no %q string: %s", msg, key, line)
		}
		return v
	}
	t.Fatalf("no %q line in:\n%s", msg, stderr)
	return ""
}
