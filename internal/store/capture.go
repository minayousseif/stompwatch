package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
)

// Where a sensitivity figure came from. They are the config package's own
// words, so the CHECK constraint in migration 6 and the config file can
// never drift apart.
const (
	SensitivityDatasheet = config.SensitivityDatasheet
	SensitivityMeasured  = config.SensitivityMeasured
)

// CaptureSettings is what the instrument was, over a span of time. Every
// level stompwatch reports rests on SensitivityDBFS, so an event has to be
// able to cite the row in force when it was recorded rather than the one in
// force now (SPEC.md section 15 decision 23).
type CaptureSettings struct {
	ID int64
	// From is when these settings came into force.
	From time.Time
	// SensitivityDBFS is the dBFS level a 94 dB SPL tone at 1 kHz produces,
	// and UncertaintyDB is the plus or minus on it.
	SensitivityDBFS float64
	UncertaintyDB   float64
	// Source is SensitivityDatasheet or SensitivityMeasured. MeasuredOn and
	// Reference are empty for the datasheet figure, which was not measured
	// on a day and not measured against anything.
	Source     string
	MeasuredOn string
	Reference  string
	// CalibrationFile is the base name of the calibration file, never the
	// path: no row and no response may name a place on disk. It is empty
	// when no calibration file was configured.
	CalibrationFile     string
	CalibrationOffsetDB float64
	CaptureDevice       string
	CaptureChannel      int
}

// same reports whether two rows describe the same instrument. From and ID
// are not part of it: they say when a row was written, not what it says.
func (c CaptureSettings) same(other CaptureSettings) bool {
	c.ID, c.From = 0, time.Time{}
	other.ID, other.From = 0, time.Time{}
	return c == other
}

// clean puts the row in the form it is stored in, so a comparison and an
// insert always see the same value. Only the base name of the calibration
// file is ever stored.
func (c CaptureSettings) clean() CaptureSettings {
	if c.CalibrationFile != "" {
		c.CalibrationFile = filepath.Base(c.CalibrationFile)
	}
	return c
}

// RecordCaptureSettings stores cs when it differs from the newest row, and
// reports whether it wrote one. The first call on an empty database always
// writes.
//
// A restart that changes nothing must add nothing. The table is a history of
// what the instrument was, and a row per restart would say the same thing
// over and over while hiding the changes among them.
func (s *Store) RecordCaptureSettings(ctx context.Context, cs CaptureSettings) (bool, error) {
	cs = cs.clean()
	newest, err := s.newestCaptureSettings(ctx)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if err == nil && newest.same(cs) {
		return false, nil
	}
	err = s.retryBusy(ctx, func() error {
		_, err := s.w.ExecContext(ctx, `INSERT INTO capture_settings
			(from_ms, sensitivity_dbfs, uncertainty_db, source, measured_on, reference,
			 calibration_file, calibration_offset_db, capture_device, capture_channel)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cs.From.UnixMilli(), cs.SensitivityDBFS, cs.UncertaintyDB, cs.Source,
			cs.MeasuredOn, cs.Reference, cs.CalibrationFile, cs.CalibrationOffsetDB,
			cs.CaptureDevice, cs.CaptureChannel)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("store: recording the capture settings: %w", err)
	}
	return true, nil
}

const captureSettingsColumns = `id, from_ms, sensitivity_dbfs, uncertainty_db, source,
	measured_on, reference, calibration_file, calibration_offset_db,
	capture_device, capture_channel`

func scanCaptureSettings(sc scanner) (CaptureSettings, error) {
	var c CaptureSettings
	var fromMS int64
	err := sc.Scan(&c.ID, &fromMS, &c.SensitivityDBFS, &c.UncertaintyDB, &c.Source,
		&c.MeasuredOn, &c.Reference, &c.CalibrationFile, &c.CalibrationOffsetDB,
		&c.CaptureDevice, &c.CaptureChannel)
	if err != nil {
		return CaptureSettings{}, err
	}
	c.From = time.UnixMilli(fromMS).UTC()
	return c, nil
}

// CaptureSettingsAt returns the settings in force at tMS, and ErrNotFound
// when the history does not reach back that far.
//
// ErrNotFound is the honest answer for every event recorded before this
// table existed. The caller must say the settings were not recorded and must
// never fall back to the newest row: an event from March citing a
// calibration performed in October is the fault this table was added to
// close.
func (s *Store) CaptureSettingsAt(ctx context.Context, tMS int64) (CaptureSettings, error) {
	row := s.r.QueryRowContext(ctx, `SELECT `+captureSettingsColumns+` FROM capture_settings
		WHERE from_ms <= ? ORDER BY from_ms DESC, id DESC LIMIT 1`, tMS)
	c, err := scanCaptureSettings(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CaptureSettings{}, fmt.Errorf("store: capture settings at %d: %w", tMS, ErrNotFound)
	}
	if err != nil {
		return CaptureSettings{}, fmt.Errorf("store: reading the capture settings at %d: %w", tMS, err)
	}
	return c, nil
}

// newestCaptureSettings returns the last row written, or ErrNotFound on an
// empty history.
func (s *Store) newestCaptureSettings(ctx context.Context) (CaptureSettings, error) {
	row := s.w.QueryRowContext(ctx, `SELECT `+captureSettingsColumns+` FROM capture_settings
		ORDER BY id DESC LIMIT 1`)
	c, err := scanCaptureSettings(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CaptureSettings{}, fmt.Errorf("store: capture settings: %w", ErrNotFound)
	}
	if err != nil {
		return CaptureSettings{}, fmt.Errorf("store: reading the newest capture settings: %w", err)
	}
	return c, nil
}

// CaptureHistory is the whole capture-settings history, oldest first.
type CaptureHistory []CaptureSettings

// At returns the settings in force at tMS, and false when the history does
// not reach back that far. False is the honest answer for every event
// recorded before the history began; a caller must say so rather than fall
// back to the newest row.
func (h CaptureHistory) At(tMS int64) (CaptureSettings, bool) {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].From.UnixMilli() <= tMS {
			return h[i], true
		}
	}
	return CaptureSettings{}, false
}

// CaptureSettingsHistory returns every row, oldest first. It is for a caller
// that has many events to place at once, such as the CSV export: the table
// holds one row per change of the instrument, so it is small enough to read
// whole and far cheaper than a query per event.
func (s *Store) CaptureSettingsHistory(ctx context.Context) (CaptureHistory, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+captureSettingsColumns+` FROM capture_settings
		ORDER BY from_ms, id`)
	if err != nil {
		return nil, fmt.Errorf("store: reading the capture settings history: %w", err)
	}
	defer rows.Close()
	var out CaptureHistory
	for rows.Next() {
		c, err := scanCaptureSettings(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reading the capture settings history: %w", err)
		}
		out = append(out, c)
	}
	return out, rowsErr(rows, "reading the capture settings history")
}
