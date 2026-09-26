package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/meter"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "noise.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func bin(sec int, laeq float64) meter.Bin {
	return meter.Bin{
		Start: t0.Add(time.Duration(sec) * time.Second), Samples: 48000,
		LAeq: laeq, LAmax: laeq + 3, LowBand: laeq - 5, HighBand: laeq - 20, Baseline: 30,
	}
}

func TestOpenAppliesMigrationsToEmptyDatabase(t *testing.T) {
	s, _ := openTemp(t)
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != 7 {
		t.Fatalf("schema version = %d, %v; want 7", v, err)
	}
	for _, table := range []string{
		"samples_1s", "samples_1m", "events", "event_media", "event_review",
		"mute_windows", "config", "system_health", "media_purge", "schema_version",
	} {
		var n int
		err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
		if err != nil || n != 1 {
			t.Errorf("table %s: count %d, err %v", table, n, err)
		}
	}
}

func TestOpenTwiceKeepsDataAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noise.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertBins(ctx, []meter.Bin{bin(0, 50)}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()
	var n int
	s.r.QueryRowContext(ctx, `SELECT count(*) FROM samples_1s`).Scan(&n)
	v, _ := s.SchemaVersion(ctx)
	if n != 1 || v != 7 {
		t.Fatalf("after reopen: %d rows, version %d; want 1 row and version 7", n, v)
	}
}

// seedVersionOne builds a database that has only migration 1 applied and
// already holds a row in each immutable table, plus the review of its one
// event. It is the state of a collector that has been running since before
// the clip start was recorded. The review row is there so that a migration
// which rebuilds events has something pointing at it.
func seedVersionOne(t *testing.T, path string) {
	t.Helper()
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations(sub)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		ms[0].sql,
		`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_ms INTEGER NOT NULL)`,
		`INSERT INTO schema_version (version, applied_ms) VALUES (1, 0)`,
		`INSERT INTO samples_1s (ts, laeq, lamax, low_energy, high_energy, baseline)
			VALUES (1757559600, 50, 55, 45, 30, 35)`,
		`INSERT INTO events (id, started_ms, ended_ms, duration_ms, laeq, lamax, baseline_at_trigger,
			low_energy, high_energy, low_high_ratio, class, confidence, envelope, forced, created_ms)
			VALUES (7, 1757559600000, 1757559602000, 2000, 50, 55, 35, 45, 30, 15, 'running', 0.5, x'00', 0, 1757559603000)`,
		`INSERT INTO event_media (event_id, kind, path, bytes, duration_ms, sha256, truncated)
			VALUES (7, 'audio', 'old.wav', 30044, 15000, 'abc', 0)`,
		`INSERT INTO event_review (event_id, status, note, reviewer, reviewed_ms)
			VALUES (7, 'verified', 'heard it', 'a@example.com', 1757559700000)`,
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seeding version 1: %v", err)
		}
	}
}

// A database with rows in it must reach the newest version with every row
// still there. event_media carries immutability triggers, so this also
// states that adding a column to it is allowed while changing a row is not.
// Raise the number by hand when a migration is added, so a migration that
// rebuilds a table and loses its rows cannot slip through.
func TestMigratingAnOldDatabaseKeepsEveryRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noise.db")
	seedVersionOne(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a version 1 database: %v", err)
	}
	defer s.Close()
	if v, err := s.SchemaVersion(ctx); err != nil || v != 7 {
		t.Fatalf("schema version = %d, %v; want 7", v, err)
	}
	for table, want := range map[string]int{"samples_1s": 1, "events": 1, "event_media": 1} {
		var n int
		if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != want {
			t.Errorf("%s holds %d rows, %v; want %d", table, n, err, want)
		}
	}

	var startedMS, bytes, durMS int64
	var kind, sum string
	err = s.r.QueryRowContext(ctx, `SELECT started_ms, kind, bytes, duration_ms, sha256
		FROM event_media WHERE event_id = 7`).Scan(&startedMS, &kind, &bytes, &durMS, &sum)
	if err != nil {
		t.Fatalf("reading the migrated media row: %v", err)
	}
	// A row written before the column existed says 0, which means "not
	// recorded". Every other value is the one that was already there.
	if startedMS != 0 || kind != "audio" || bytes != 30044 || durMS != 15000 || sum != "abc" {
		t.Errorf("migrated media row = %d %q %d %d %q", startedMS, kind, bytes, durMS, sum)
	}

	var jump sql.NullFloat64
	if err := s.r.QueryRowContext(ctx, `SELECT jump_db FROM events WHERE id = 7`).Scan(&jump); err != nil {
		t.Fatalf("reading jump_db of the migrated event: %v", err)
	}
	if jump.Valid {
		t.Errorf("jump_db of an event recorded before schema 7 = %v, want NULL: nothing measured it", jump.Float64)
	}
}

// The rebuild that widened the class list must keep every value of every
// old row and its media and review rows, and the immutability triggers.
func TestMigrationSevenKeepsRowsMediaAndTriggers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noise.db")
	seedVersionOne(t, path)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var class string
	var laeq float64
	var media, review int
	err = s.r.QueryRowContext(ctx, `SELECT e.class, e.laeq,
		(SELECT count(*) FROM event_media m WHERE m.event_id = e.id),
		(SELECT count(*) FROM event_review r WHERE r.event_id = e.id)
		FROM events e WHERE e.id = 7`).Scan(&class, &laeq, &media, &review)
	if err != nil {
		t.Fatalf("reading the migrated event: %v", err)
	}
	if class != "running" || laeq != 50 || media != 1 || review != 1 {
		t.Errorf("event 7 after the rebuild = %q, %v, %d media rows, %d review rows; want running, 50, 1, 1",
			class, laeq, media, review)
	}
	if _, err := s.w.ExecContext(ctx, `DELETE FROM events WHERE id = 7`); err == nil {
		t.Errorf("a DELETE on events succeeded after the rebuild; the trigger is gone")
	}
	if _, err := s.w.ExecContext(ctx, `UPDATE events SET laeq = 1 WHERE id = 7`); err == nil {
		t.Errorf("an UPDATE on events succeeded after the rebuild; the trigger is gone")
	}
	// PRAGMA foreign_key_check returns a table|rowid|parent|fkid row for every
	// broken reference and no rows at all when every reference is intact, so
	// the check is whether a row comes back, not whether the query errors.
	rows, err := s.r.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if rows.Next() {
		t.Errorf("foreign_key_check reported a broken reference after the rebuild")
	}
	if err := rows.Err(); err != nil {
		t.Errorf("foreign_key_check: %v", err)
	}
	rows.Close()
	e := testEvent()
	e.Class = detect.Impact
	if _, err := s.InsertEvent(ctx, e, t0); err != nil {
		t.Errorf("inserting an impact event after the rebuild: %v", err)
	}
}

func TestLoadMigrationsRejectsGapsAndBadNames(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"gap":      {"0001_a.sql": {Data: []byte("SELECT 1;")}, "0003_c.sql": {Data: []byte("SELECT 1;")}},
		"bad name": {"0001_a.sql": {Data: []byte("SELECT 1;")}, "two.sql": {Data: []byte("SELECT 1;")}},
		"empty":    {},
	} {
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("%s: loadMigrations returned no error", name)
		}
	}
	got, err := loadMigrations(fstest.MapFS{
		"0002_b.sql": {Data: []byte("B")},
		"0001_a.sql": {Data: []byte("A")},
	})
	if err != nil || len(got) != 2 || got[0].version != 1 || got[1].sql != "B" {
		t.Fatalf("loadMigrations = %+v, %v; want versions 1 then 2", got, err)
	}
}

// SPEC.md section 6.9.1: WAL, NORMAL sync, a busy timeout, foreign keys, one writer
// connection, and a read-only reader.
func TestConnectionSettings(t *testing.T) {
	s, _ := openTemp(t)
	for _, c := range []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"synchronous", "1"},
		{"busy_timeout", "5000"},
		{"foreign_keys", "1"},
	} {
		var got string
		if err := s.w.QueryRowContext(ctx, "PRAGMA "+c.pragma).Scan(&got); err != nil || got != c.want {
			t.Errorf("writer PRAGMA %s = %q, %v; want %q", c.pragma, got, err, c.want)
		}
	}
	if got := s.w.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("writer max open connections = %d, want 1", got)
	}
	if _, err := s.r.ExecContext(ctx, `INSERT INTO config(key, value) VALUES ('x', 'y')`); err == nil {
		t.Error("insert through the reader succeeded, want a read-only error")
	}
}

// A minute's LAeq is the energy mean of its seconds:
// 10*log10((10^6 + 10^7 + 10^8) / 3) = 75.682 dB.
func TestInsertBinsWritesRowsAndMinuteRollup(t *testing.T) {
	s, _ := openTemp(t)
	n, err := s.InsertBins(ctx, []meter.Bin{bin(0, 60), bin(1, 70), bin(2, 80), bin(61, 55)})
	if err != nil || n != 0 {
		t.Fatalf("InsertBins = %d conflicts, %v", n, err)
	}

	var laeq, lamax, low, high, base float64
	err = s.r.QueryRowContext(ctx, `SELECT laeq, lamax, low_energy, high_energy, baseline FROM samples_1s WHERE ts = ?`,
		t0.Add(time.Second).Unix()).Scan(&laeq, &lamax, &low, &high, &base)
	if err != nil || laeq != 70 || lamax != 73 || low != 65 || high != 50 || base != 30 {
		t.Errorf("second 1 = %v %v %v %v %v, %v", laeq, lamax, low, high, base, err)
	}

	var lmin, lmean, lmax, amax float64
	var cnt int
	err = s.r.QueryRowContext(ctx, `SELECT laeq_min, laeq_mean, laeq_max, lamax_max, n FROM samples_1m WHERE ts = ?`,
		t0.Unix()).Scan(&lmin, &lmean, &lmax, &amax, &cnt)
	if err != nil || lmin != 60 || math.Abs(lmean-75.682) > 0.001 || lmax != 80 || amax != 83 || cnt != 3 {
		t.Errorf("minute 0 = min %v mean %.3f max %v lamax %v n %d, %v", lmin, lmean, lmax, amax, cnt, err)
	}
	err = s.r.QueryRowContext(ctx, `SELECT n FROM samples_1m WHERE ts = ?`, t0.Add(time.Minute).Unix()).Scan(&cnt)
	if err != nil || cnt != 1 {
		t.Errorf("minute 1 n = %d, %v; want 1", cnt, err)
	}
}

// A repeated second must not lose the rest of the batch, must not change the
// stored row, and must not be counted twice in the rollup. It is reported.
func TestInsertBinsReportsDuplicateSecondsAndKeepsTheRest(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.InsertBins(ctx, []meter.Bin{bin(0, 60)}); err != nil {
		t.Fatal(err)
	}
	n, err := s.InsertBins(ctx, []meter.Bin{bin(0, 99), bin(1, 60)})
	if err != nil || n != 1 {
		t.Fatalf("InsertBins = %d conflicts, %v; want 1 conflict", n, err)
	}
	var laeq float64
	var rows, cnt int
	s.r.QueryRowContext(ctx, `SELECT laeq FROM samples_1s WHERE ts = ?`, t0.Unix()).Scan(&laeq)
	s.r.QueryRowContext(ctx, `SELECT count(*) FROM samples_1s`).Scan(&rows)
	s.r.QueryRowContext(ctx, `SELECT n FROM samples_1m WHERE ts = ?`, t0.Unix()).Scan(&cnt)
	if laeq != 60 || rows != 2 || cnt != 2 {
		t.Fatalf("laeq %v, rows %d, minute n %d; want 60, 2, 2", laeq, rows, cnt)
	}
}

func testEvent() detect.Event {
	return detect.Event{
		Start: t0.Add(1500 * time.Millisecond), End: t0.Add(4200 * time.Millisecond),
		LAeq: 71.5, LAmax: 80.25, BaselineAtTrigger: 35,
		LowBand: 68, HighBand: 50, LowHighRatioDB: 18,
		Forced: true, Class: detect.Running, Confidence: 0.75,
		Envelope:   []float64{0.5, -1.25, 3},
		JumpDB:     17.1,
		RiseDB:     13.3,
		HasContext: true,
	}
}

func TestInsertEventStoresAllFields(t *testing.T) {
	s, _ := openTemp(t)
	id, err := s.InsertEvent(ctx, testEvent(), t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var (
		start, end, dur, created, forced int64
		laeq, lamax, base, low, high     float64
		ratio, conf                      float64
		jump, rise                       float64
		class                            string
		env                              []byte
	)
	err = s.r.QueryRowContext(ctx, `SELECT started_ms, ended_ms, duration_ms, laeq, lamax, baseline_at_trigger,
		low_energy, high_energy, low_high_ratio, class, confidence, envelope, forced, created_ms,
		jump_db, rise_db
		FROM events WHERE id = ?`, id).Scan(&start, &end, &dur, &laeq, &lamax, &base,
		&low, &high, &ratio, &class, &conf, &env, &forced, &created, &jump, &rise)
	if err != nil {
		t.Fatal(err)
	}
	if start != t0.UnixMilli()+1500 || end != t0.UnixMilli()+4200 || dur != 2700 || created != t0.UnixMilli()+60000 {
		t.Errorf("times %d %d %d %d", start, end, dur, created)
	}
	if laeq != 71.5 || lamax != 80.25 || base != 35 || low != 68 || high != 50 || ratio != 18 ||
		class != "running" || conf != 0.75 || forced != 1 {
		t.Errorf("fields %v %v %v %v %v %v %q %v %d", laeq, lamax, base, low, high, ratio, class, conf, forced)
	}
	// float32 little-endian: 0.5 = 00 00 00 3F, -1.25 = 00 00 A0 BF, 3 = 00 00 40 40.
	if got := hex.EncodeToString(env); got != "0000003f0000a0bf00004040" {
		t.Errorf("envelope blob = %s", got)
	}
	back, err := DecodeEnvelope(env)
	if err != nil || len(back) != 3 || back[1] != -1.25 {
		t.Errorf("DecodeEnvelope = %v, %v", back, err)
	}
	if jump != 17.1 || rise != 13.3 {
		t.Errorf("jump_db, rise_db = %v, %v; want 17.1 and 13.3", jump, rise)
	}
}

// An event with less than 10 s of levels before it has no rise to report.
// The columns must say "not measured" with NULL, never 0, which would read
// as a level that did not move.
func TestInsertEventWithoutContextStoresNull(t *testing.T) {
	s, _ := openTemp(t)
	e := testEvent()
	e.HasContext, e.JumpDB, e.RiseDB = false, 5, 5
	id, err := s.InsertEvent(ctx, e, t0)
	if err != nil {
		t.Fatal(err)
	}
	var jump, rise sql.NullFloat64
	if err := s.r.QueryRowContext(ctx, `SELECT jump_db, rise_db FROM events WHERE id = ?`, id).Scan(&jump, &rise); err != nil {
		t.Fatal(err)
	}
	if jump.Valid || rise.Valid {
		t.Errorf("jump_db, rise_db = %v, %v; want NULL for an event with no level history", jump, rise)
	}
}

func TestInsertMediaOncePerKind(t *testing.T) {
	s, _ := openTemp(t)
	id, _ := s.InsertEvent(ctx, testEvent(), t0)
	m := Media{EventID: id, Kind: KindAudio, Path: "/data/clips/audio/2026/09/11/1.wav",
		Bytes: 20044, Duration: 10 * time.Second, SHA256: strings.Repeat("ab", 32), Truncated: true}
	if err := s.InsertMedia(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertMedia(ctx, m); err == nil {
		t.Error("second audio row for the same event succeeded")
	}
	m.EventID = id + 100
	if err := s.InsertMedia(ctx, m); err == nil {
		t.Error("media for a missing event succeeded, want a foreign key error")
	}
}

// tableHash reads every row of a table in rowid order and hashes the values.
func tableHash(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	h := sha256.New()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%#v\n", vals)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SPEC.md section 3.3 and section 11: review decisions never change events or samples.
func TestReviewDoesNotAlterEventsOrSamples(t *testing.T) {
	s, _ := openTemp(t)
	s.InsertBins(ctx, []meter.Bin{bin(0, 60), bin(1, 70)})
	id, _ := s.InsertEvent(ctx, testEvent(), t0)
	s.InsertMedia(ctx, Media{EventID: id, Kind: KindAudio, Path: "a.wav", SHA256: "x"})

	before := map[string]string{}
	for _, tbl := range []string{"events", "samples_1s", "event_media"} {
		before[tbl] = tableHash(t, s.r, tbl)
	}
	for _, r := range []Review{
		{EventID: id, Status: StatusUnsure, Note: "maybe", Reviewer: "a@example.com", At: t0.Add(time.Hour)},
		{EventID: id, Status: StatusVerified, Note: "heard it", Reviewer: "b@example.com", At: t0.Add(2 * time.Hour)},
	} {
		if err := s.SetReview(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for tbl, h := range before {
		if got := tableHash(t, s.r, tbl); got != h {
			t.Errorf("table %s changed after review", tbl)
		}
	}

	var status, note, reviewer string
	var at int64
	err := s.r.QueryRowContext(ctx, `SELECT status, note, reviewer, reviewed_ms FROM event_review WHERE event_id = ?`, id).
		Scan(&status, &note, &reviewer, &at)
	if err != nil || status != "verified" || note != "heard it" || reviewer != "b@example.com" || at != t0.Add(2*time.Hour).UnixMilli() {
		t.Errorf("review = %q %q %q %d, %v", status, note, reviewer, at, err)
	}
}

func TestReviewRejectsUnknownStatus(t *testing.T) {
	s, _ := openTemp(t)
	id, _ := s.InsertEvent(ctx, testEvent(), t0)
	if err := s.SetReview(ctx, Review{EventID: id, Status: "maybe", At: t0}); err == nil {
		t.Fatal("SetReview with status \"maybe\" succeeded")
	}
}

// The database itself refuses changes to raw records, so no future code path
// can alter them by mistake.
func TestRawRecordsRejectUpdateAndDelete(t *testing.T) {
	s, _ := openTemp(t)
	s.InsertBins(ctx, []meter.Bin{bin(0, 60)})
	id, _ := s.InsertEvent(ctx, testEvent(), t0)
	s.InsertMedia(ctx, Media{EventID: id, Kind: KindAudio, Path: "a.wav", SHA256: "x"})
	s.AddHealth(ctx, t0, HealthCaptureGap, "arecord exited", time.Second)
	s.RecordPurge(ctx, []Purge{{EventID: id, Kind: KindAudio, Bytes: 16, At: t0, By: "alex"}})
	s.RecordCaptureSettings(ctx, settingsAt(t0))
	for _, stmt := range []string{
		`UPDATE samples_1s SET laeq = 0`,
		`DELETE FROM samples_1s`,
		`UPDATE events SET laeq = 0`,
		`DELETE FROM events`,
		`UPDATE event_media SET sha256 = 'y'`,
		`DELETE FROM event_media`,
		`UPDATE system_health SET kind = 'nothing happened'`,
		`DELETE FROM system_health`,
		`UPDATE media_purge SET purged_by = 'somebody else'`,
		`DELETE FROM media_purge`,
		`UPDATE capture_settings SET sensitivity_dbfs = 0`,
		`DELETE FROM capture_settings`,
	} {
		if _, err := s.w.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s succeeded", stmt)
		}
	}
}

func TestAddHealthStoresRow(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.AddHealth(ctx, t0, HealthCaptureGap, "arecord exited", 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	var ts, dur int64
	var kind, detail string
	err := s.r.QueryRowContext(ctx, `SELECT ts_ms, kind, detail, duration_ms FROM system_health`).Scan(&ts, &kind, &detail, &dur)
	if err != nil || ts != t0.UnixMilli() || kind != "capture_gap" || detail != "arecord exited" || dur != 1500 {
		t.Fatalf("health row = %d %q %q %d, %v", ts, kind, detail, dur, err)
	}
}

// busy_timeout must make a write wait for another writer instead of failing.
func TestWriteWaitsForAnotherWriter(t *testing.T) {
	s, path := openTemp(t)
	other, err := sql.Open(driverName, "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO config(key, value, updated_ms) VALUES ('lock', 'held', 0)`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		tx.Commit()
		close(released)
	}()

	begin := time.Now()
	_, err = s.InsertBins(ctx, []meter.Bin{bin(0, 60)})
	waited := time.Since(begin)
	<-released
	if err != nil {
		t.Fatalf("InsertBins while another writer held the lock: %v", err)
	}
	if waited < 250*time.Millisecond {
		t.Errorf("InsertBins returned after %v; the other writer held the lock for 300ms", waited)
	}
}

func TestReadsRunDuringWrites(t *testing.T) {
	s, _ := openTemp(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			batch := make([]meter.Bin, 20)
			for k := range batch {
				batch[k] = bin(i*20+k, 60)
			}
			if _, err := s.InsertBins(ctx, batch); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			var n int
			if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM samples_1s`).Scan(&n); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestIntegrityCheckAndSnapshot(t *testing.T) {
	s, _ := openTemp(t)
	s.InsertBins(ctx, []meter.Bin{bin(0, 60), bin(1, 61)})
	if err := s.IntegrityCheck(ctx); err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}

	snap := filepath.Join(t.TempDir(), "2026-09-11.sqlite")
	sum, err := s.Snapshot(ctx, snap)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	b, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	if h := sha256.Sum256(b); sum != hex.EncodeToString(h[:]) {
		t.Errorf("Snapshot hash %s does not match file %x", sum, h)
	}
	copyDB, err := sql.Open(driverName, "file:"+snap+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var n int
	if err := copyDB.QueryRowContext(ctx, `SELECT count(*) FROM samples_1s`).Scan(&n); err != nil || n != 2 {
		t.Errorf("snapshot holds %d rows, %v; want 2", n, err)
	}
	if _, err := s.Snapshot(ctx, snap); err == nil {
		t.Error("second Snapshot to the same path succeeded")
	}
}
