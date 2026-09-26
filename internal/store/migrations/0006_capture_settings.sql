-- Schema version 6. The history of the capture and calibration settings, so
-- that every event can cite the ones in force when it was recorded.
--
-- Two faults share one root, and this table closes both
-- (SPEC.md section 15 decision 23).
--
-- The first: every level stompwatch reports is dBFS - sensitivity_dbfs + 94,
-- and sensitivity_dbfs is the EM-01 datasheet's figure for the model, which
-- the datasheet gives with plus or minus 2 dB of per-unit tolerance. The
-- dashboard printed 71.8 dB and nothing on screen said the truth was
-- somewhere between 69.8 and 73.8.
--
-- The second, and the worse one: the printed evidence sheet read the
-- sensitivity and the calibration file from GET /api/system, which reports
-- what is in force now. Reprint an event from three months ago and the sheet
-- silently claimed today's calibration. The day the owner measures the real
-- sensitivity, every reprinted older event would carry the new figure and be
-- wrong.
--
-- from_ms is when this set of settings came into force: the moment the
-- collector started with them. A row is written only when something differs
-- from the newest row, so a restart that changes nothing adds nothing and the
-- table stays a history rather than a start-up log.
--
-- calibration_file is the base name only, never the path. No row and no
-- response may name a place on disk (SPEC.md section 3).
--
-- An event older than the first row here has no settings to cite, and must
-- say so. Borrowing the newest row would be the same lie in a new place.
CREATE TABLE capture_settings (
  id                  INTEGER PRIMARY KEY,
  from_ms             INTEGER NOT NULL,
  sensitivity_dbfs    REAL NOT NULL,
  uncertainty_db      REAL NOT NULL,
  source              TEXT NOT NULL CHECK (source IN ('datasheet', 'measured')),
  measured_on         TEXT NOT NULL DEFAULT '',
  reference           TEXT NOT NULL DEFAULT '',
  calibration_file    TEXT NOT NULL DEFAULT '',
  calibration_offset_db REAL NOT NULL DEFAULT 0,
  capture_device      TEXT NOT NULL DEFAULT '',
  capture_channel     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_capture_settings_from ON capture_settings(from_ms);

-- The same immutability as the other raw tables (SPEC.md section 3 rule 3).
-- A row that could be changed afterwards would let the database claim a
-- calibration that was never in force, which is exactly what this table
-- exists to prevent.
CREATE TRIGGER capture_settings_no_update BEFORE UPDATE ON capture_settings
BEGIN SELECT RAISE(ABORT, 'capture_settings rows are immutable'); END;
CREATE TRIGGER capture_settings_no_delete BEFORE DELETE ON capture_settings
BEGIN SELECT RAISE(ABORT, 'capture_settings rows are immutable'); END;
