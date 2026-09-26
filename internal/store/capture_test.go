package store

import (
	"errors"
	"testing"
	"time"
)

// settingsAt is the settings a test records at a given instant.
func settingsAt(at time.Time) CaptureSettings {
	return CaptureSettings{
		From:            at,
		SensitivityDBFS: -13,
		UncertaintyDB:   2,
		Source:          SensitivityDatasheet,
		CaptureDevice:   "hw:EM01,0",
		CaptureChannel:  0,
	}
}

func countCaptureSettings(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM capture_settings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The first start on an empty database always writes a row. Without it no
// event recorded by this collector could ever cite the settings it was
// measured under.
func TestTheFirstStartRecordsTheCaptureSettings(t *testing.T) {
	s, _ := openTemp(t)
	wrote, err := s.RecordCaptureSettings(ctx, settingsAt(t0))
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("the first start wrote no capture_settings row")
	}
	got, err := s.CaptureSettingsAt(ctx, t0.Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatalf("CaptureSettingsAt: %v", err)
	}
	if got.SensitivityDBFS != -13 || got.UncertaintyDB != 2 {
		t.Errorf("read %v dBFS plus or minus %v", got.SensitivityDBFS, got.UncertaintyDB)
	}
	if got.Source != "datasheet" {
		t.Errorf("source = %q, want datasheet", got.Source)
	}
	if got.CaptureDevice != "hw:EM01,0" {
		t.Errorf("capture_device = %q", got.CaptureDevice)
	}
}

// A restart that changes nothing must add no row. The table would otherwise
// grow by one every time the service is restarted, and a history of
// identical rows says nothing.
func TestARestartThatChangesNothingWritesNoRow(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.RecordCaptureSettings(ctx, settingsAt(t0)); err != nil {
		t.Fatal(err)
	}
	wrote, err := s.RecordCaptureSettings(ctx, settingsAt(t0.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("a restart that changed nothing wrote a second row")
	}
	if n := countCaptureSettings(t, s); n != 1 {
		t.Errorf("capture_settings holds %d rows, want 1", n)
	}
}

// Every field is part of the comparison. A change in any one of them is a
// change in what the levels mean, so each must open a new row.
func TestEveryChangedFieldWritesANewRow(t *testing.T) {
	changes := map[string]func(*CaptureSettings){
		"sensitivity_dbfs":      func(c *CaptureSettings) { c.SensitivityDBFS = -12.47 },
		"uncertainty_db":        func(c *CaptureSettings) { c.UncertaintyDB = 0.5 },
		"source":                func(c *CaptureSettings) { c.Source = SensitivityMeasured; c.MeasuredOn = "2026-10-03" },
		"measured_on":           func(c *CaptureSettings) { c.Source = SensitivityMeasured; c.MeasuredOn = "2026-10-04" },
		"reference":             func(c *CaptureSettings) { c.Reference = "B and K 4231 at 94 dB SPL" },
		"calibration_file":      func(c *CaptureSettings) { c.CalibrationFile = "em01.txt" },
		"calibration_offset_db": func(c *CaptureSettings) { c.CalibrationOffsetDB = 0.42 },
		"capture_device":        func(c *CaptureSettings) { c.CaptureDevice = "hw:2,0" },
		"capture_channel":       func(c *CaptureSettings) { c.CaptureChannel = 1 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, _ := openTemp(t)
			if _, err := s.RecordCaptureSettings(ctx, settingsAt(t0)); err != nil {
				t.Fatal(err)
			}
			next := settingsAt(t0.Add(time.Hour))
			change(&next)
			wrote, err := s.RecordCaptureSettings(ctx, next)
			if err != nil {
				t.Fatal(err)
			}
			if !wrote {
				t.Fatalf("a change of %s wrote no row", name)
			}
			if n := countCaptureSettings(t, s); n != 2 {
				t.Fatalf("capture_settings holds %d rows, want 2", n)
			}
			// The old event still cites the old row, and the new one the new.
			old, err := s.CaptureSettingsAt(ctx, t0.Add(time.Minute).UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			if old.SensitivityDBFS != -13 || old.Source != "datasheet" || old.CaptureChannel != 0 {
				t.Errorf("an event before the change reads the new settings: %+v", old)
			}
			now, err := s.CaptureSettingsAt(ctx, t0.Add(2*time.Hour).UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			if now.ID == old.ID {
				t.Errorf("an event after the change reads the old row %d", old.ID)
			}
		})
	}
}

// An event recorded before the history began must say the settings are not
// recorded. Falling back to the newest row would make an event from March
// claim a calibration performed in October, which is the whole fault this
// table exists to close.
func TestAnEventOlderThanTheHistoryHasNoSettings(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.RecordCaptureSettings(ctx, settingsAt(t0)); err != nil {
		t.Fatal(err)
	}
	_, err := s.CaptureSettingsAt(ctx, t0.Add(-time.Second).UnixMilli())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("an event one second before the first row gave %v, want ErrNotFound", err)
	}
	// An empty history is the same answer, not a different one.
	empty, _ := openTemp(t)
	if _, err := empty.CaptureSettingsAt(ctx, t0.UnixMilli()); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty history gave %v, want ErrNotFound", err)
	}
}

// No row and no response may name a place on disk. The base name says which
// calibration file it was, which is the part that matters.
func TestOnlyTheBaseNameOfTheCalibrationFileIsStored(t *testing.T) {
	s, _ := openTemp(t)
	cs := settingsAt(t0)
	cs.CalibrationFile = "/etc/stompwatch/cal/em01-response.txt"
	if _, err := s.RecordCaptureSettings(ctx, cs); err != nil {
		t.Fatal(err)
	}
	got, err := s.CaptureSettingsAt(ctx, t0.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if got.CalibrationFile != "em01-response.txt" {
		t.Errorf("calibration_file = %q, want em01-response.txt", got.CalibrationFile)
	}

	// And the same path, written twice, is not a change.
	wrote, err := s.RecordCaptureSettings(ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("the same calibration file, written again, opened a second row")
	}
}

// The history is a raw record (SPEC.md section 3 rule 3). Once a row says
// what the instrument was, nothing may change it into something else.
func TestCaptureSettingsRowsAreImmutable(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.RecordCaptureSettings(ctx, settingsAt(t0)); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE capture_settings SET sensitivity_dbfs = -12`,
		`UPDATE capture_settings SET source = 'measured'`,
		`DELETE FROM capture_settings`,
	} {
		if _, err := s.w.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s succeeded", stmt)
		}
	}
}
