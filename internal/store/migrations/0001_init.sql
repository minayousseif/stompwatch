-- Schema version 1. See SPEC.md section 8 and section 15.
--
-- Units: samples_1s and samples_1m are keyed by epoch seconds. Every other
-- time column is epoch milliseconds and ends in _ms. Levels are dB SPL.

-- Epoch seconds as INTEGER PRIMARY KEY make ts the rowid, so rows are stored
-- in time order and range scans need no extra index.
CREATE TABLE samples_1s (
  ts          INTEGER PRIMARY KEY,
  laeq        REAL NOT NULL,
  lamax       REAL NOT NULL,
  low_energy  REAL NOT NULL, -- unweighted level, 20-120 Hz
  high_energy REAL NOT NULL, -- unweighted level, above 500 Hz
  baseline    REAL NOT NULL
);

-- One row per minute, updated as seconds arrive. laeq_mean is the energy
-- mean of the seconds, not the mean of their dB values.
CREATE TABLE samples_1m (
  ts        INTEGER PRIMARY KEY,
  laeq_min  REAL NOT NULL,
  laeq_mean REAL NOT NULL,
  laeq_max  REAL NOT NULL,
  lamax_max REAL NOT NULL,
  n         INTEGER NOT NULL
);

CREATE TABLE events (
  id                  INTEGER PRIMARY KEY,
  started_ms          INTEGER NOT NULL,
  ended_ms            INTEGER NOT NULL,
  duration_ms         INTEGER NOT NULL,
  laeq                REAL NOT NULL,
  lamax               REAL NOT NULL,
  baseline_at_trigger REAL NOT NULL,
  low_energy          REAL NOT NULL,
  high_energy         REAL NOT NULL,
  low_high_ratio      REAL NOT NULL, -- dB: low_energy - high_energy
  class               TEXT NOT NULL CHECK (class IN ('running', 'jumping', 'stomping', 'unknown')),
  confidence          REAL NOT NULL,
  envelope            BLOB NOT NULL, -- float32 little-endian, 100 samples per second
  forced              INTEGER NOT NULL DEFAULT 0 CHECK (forced IN (0, 1)), -- closed by max_event_s
  created_ms          INTEGER NOT NULL
);
CREATE INDEX idx_events_started ON events(started_ms);

CREATE TABLE event_media (
  event_id    INTEGER NOT NULL REFERENCES events(id),
  kind        TEXT NOT NULL CHECK (kind IN ('audio', 'video')),
  path        TEXT NOT NULL,
  bytes       INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL,
  sha256      TEXT NOT NULL,
  truncated   INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
  PRIMARY KEY (event_id, kind)
);

-- Review decisions live here and never change events or samples.
CREATE TABLE event_review (
  event_id    INTEGER PRIMARY KEY REFERENCES events(id),
  status      TEXT NOT NULL CHECK (status IN ('verified', 'rejected', 'unsure')),
  note        TEXT NOT NULL DEFAULT '',
  reviewer    TEXT NOT NULL DEFAULT '',
  reviewed_ms INTEGER NOT NULL
);

CREATE TABLE mute_windows (
  id       INTEGER PRIMARY KEY,
  start_ms INTEGER NOT NULL,
  end_ms   INTEGER NOT NULL,
  reason   TEXT NOT NULL
);

CREATE TABLE config (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_ms INTEGER
);

CREATE TABLE system_health (
  id          INTEGER PRIMARY KEY,
  ts_ms       INTEGER NOT NULL,
  kind        TEXT NOT NULL,
  detail      TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER
);
CREATE INDEX idx_system_health_ts ON system_health(ts_ms);

-- Raw records and the health log are immutable (SPEC.md section 3.3). A future
-- retention job needs its own migration to allow logged deletes.
CREATE TRIGGER samples_1s_no_update BEFORE UPDATE ON samples_1s
BEGIN SELECT RAISE(ABORT, 'samples_1s rows are immutable'); END;
CREATE TRIGGER samples_1s_no_delete BEFORE DELETE ON samples_1s
BEGIN SELECT RAISE(ABORT, 'samples_1s rows are immutable'); END;

CREATE TRIGGER events_no_update BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events rows are immutable'); END;
CREATE TRIGGER events_no_delete BEFORE DELETE ON events
BEGIN SELECT RAISE(ABORT, 'events rows are immutable'); END;

CREATE TRIGGER event_media_no_update BEFORE UPDATE ON event_media
BEGIN SELECT RAISE(ABORT, 'event_media rows are immutable'); END;
CREATE TRIGGER event_media_no_delete BEFORE DELETE ON event_media
BEGIN SELECT RAISE(ABORT, 'event_media rows are immutable'); END;

CREATE TRIGGER system_health_no_update BEFORE UPDATE ON system_health
BEGIN SELECT RAISE(ABORT, 'system_health rows are immutable'); END;
CREATE TRIGGER system_health_no_delete BEFORE DELETE ON system_health
BEGIN SELECT RAISE(ABORT, 'system_health rows are immutable'); END;
