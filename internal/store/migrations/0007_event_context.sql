-- Schema version 7. Three more classes, and how far and how fast an event
-- rose over the level before it.
--
-- The classifier could not tell a hit through the ceiling from an air
-- conditioner starting: both are low-frequency and both stand 15 dB over a
-- baseline that lags. What separates them is the level just before the
-- event and the speed of the rise. A hit rises 13 dB within one second; a
-- fan ramps up over several (SPEC.md section 15 decision 25).
--
-- jump_db is LAmax over the median level of the 30 s before the event.
-- rise_db is the largest step between two consecutive one-second means.
-- Both are NULL for events recorded before this version and for events with
-- less than 10 s of levels before them. NULL means "not measured", never 0.
--
-- The class column's CHECK lists the allowed classes, and SQLite cannot
-- change a CHECK in place, so the table is rebuilt: the rows are copied
-- out, the table is dropped and created again with the wider CHECK and the
-- two new columns, and the rows are copied back with their ids. Every row
-- keeps every value it had (SPEC.md section 3 rule 3). The old table is
-- not renamed, because with foreign keys on a rename would point
-- event_media, event_review and media_purge at the old name.
--
-- Dropping events deletes its rows first, and event_media, event_review and
-- media_purge point at them. defer_foreign_keys puts that check off until
-- the commit, by which time the rows are back under the same ids. DROP
-- TABLE's implicit row deletion fires no triggers, so the immutability
-- triggers do not block the rebuild; they are dropped with the table and
-- created again below.
PRAGMA defer_foreign_keys = ON;

CREATE TABLE events_copy AS SELECT * FROM events;
DROP TABLE events;

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
  class               TEXT NOT NULL CHECK (class IN ('running', 'jumping', 'stomping', 'impact', 'steady', 'airborne', 'unknown')),
  confidence          REAL NOT NULL,
  envelope            BLOB NOT NULL, -- float32 little-endian, 100 samples per second
  forced              INTEGER NOT NULL DEFAULT 0 CHECK (forced IN (0, 1)), -- closed by max_event_s
  created_ms          INTEGER NOT NULL,
  jump_db             REAL, -- LAmax over the median level of the 30 s before; NULL when not measured
  rise_db             REAL  -- largest one-second step; NULL when not measured
);

INSERT INTO events (id, started_ms, ended_ms, duration_ms, laeq, lamax, baseline_at_trigger,
  low_energy, high_energy, low_high_ratio, class, confidence, envelope, forced, created_ms)
SELECT id, started_ms, ended_ms, duration_ms, laeq, lamax, baseline_at_trigger,
  low_energy, high_energy, low_high_ratio, class, confidence, envelope, forced, created_ms
FROM events_copy;

DROP TABLE events_copy;

CREATE INDEX idx_events_started ON events(started_ms);
CREATE TRIGGER events_no_update BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events rows are immutable'); END;
CREATE TRIGGER events_no_delete BEFORE DELETE ON events
BEGIN SELECT RAISE(ABORT, 'events rows are immutable'); END;
