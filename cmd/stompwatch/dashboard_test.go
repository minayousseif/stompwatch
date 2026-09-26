package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/minayousseif/stompwatch/internal/store"
	"github.com/minayousseif/stompwatch/internal/testsignal"
)

// thumps is a minute of quiet room with four jumps in it, which the default
// settings detect as one event.
func thumps(t *testing.T, dir string) string {
	t.Helper()
	x := testsignal.Pink(60*testsignal.Rate, 0.001, 7)
	testsignal.AddThumps(x, 0.5, 40, 42, 44, 46)
	return writeWAV(t, dir, x)
}

func readDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "noise.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func rows(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// putOverride writes one row into the config table of a fresh database, the
// way the dashboard's settings page does.
func putOverride(t *testing.T, dir, key, value string) {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "noise.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutSettings(context.Background(), map[string]*string{key: &value}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// The whole program with the dashboard switched on: the server listens, the
// log reaches the file as well as the journal, and the measurement is stored.
func TestRunServesTheDashboardAndWritesTheLogFile(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "the dashboard is listening") {
		t.Errorf("the log does not say the dashboard is listening:\n%s", stderr)
	}
	if !strings.Contains(stderr, "auth_mode") {
		t.Errorf("the log does not say which auth mode is in force:\n%s", stderr)
	}

	b, err := os.ReadFile(filepath.Join(dir, "logs", "stompwatch.log"))
	if err != nil {
		t.Fatalf("no log file: %v", err)
	}
	if !strings.Contains(string(b), "starting") {
		t.Errorf("the log file does not hold the startup line:\n%s", b)
	}
	if n := rows(t, readDB(t, dir), `SELECT count(*) FROM samples_1s`); n != 60 {
		t.Errorf("samples_1s has %d rows, want 60", n)
	}
}

// A listener that cannot start must not stop the collector. 192.0.2.1 is the
// documentation range, so no machine has it.
func TestRunCarriesOnWhenTheDashboardCannotListen(t *testing.T) {
	cfg, dir := writeConfig(t, "http_addr = 192.0.2.1:8080")
	wav := thumps(t, dir)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "the dashboard did not start") {
		t.Errorf("the log does not report the failed listener:\n%s", stderr)
	}
	if !strings.Contains(stderr, "loopback") {
		t.Errorf("the log does not warn about the address that is not loopback:\n%s", stderr)
	}
	if n := rows(t, readDB(t, dir), `SELECT count(*) FROM samples_1s`); n != 60 {
		t.Errorf("samples_1s has %d rows, want 60: the measurement stopped with the dashboard", n)
	}
}

// A log file that cannot be opened must not stop the collector either. Here
// log_dir names a file, so the directory cannot be created.
func TestRunCarriesOnWhenTheLogFileCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, dir2 := writeConfig(t, "log_dir = "+blocked)
	wav := thumps(t, dir2)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "cannot write the log file") {
		t.Errorf("the log does not report the log file it could not open:\n%s", stderr)
	}
	if n := rows(t, readDB(t, dir2), `SELECT count(*) FROM samples_1s`); n != 60 {
		t.Errorf("samples_1s has %d rows, want 60: the measurement stopped with the log file", n)
	}
}

// A setting stored by the dashboard reaches the collector at startup. With a
// trigger level of 60 dB the four jumps are not loud enough to count.
func TestAStoredSettingReachesTheCollector(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)
	putOverride(t, dir, "threshold_db", "60")

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	db := readDB(t, dir)
	if n := rows(t, db, `SELECT count(*) FROM events`); n != 0 {
		t.Errorf("events has %d rows, want 0: the stored trigger level did not reach the detector", n)
	}
	if n := rows(t, db, `SELECT count(*) FROM samples_1s`); n != 60 {
		t.Errorf("samples_1s has %d rows, want 60", n)
	}
}

// The same audio and no stored setting gives one event, so the test above
// measures the setting and not the audio.
func TestTheSameAudioGivesAnEventWithNoStoredSetting(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if n := rows(t, readDB(t, dir), `SELECT count(*) FROM events`); n != 1 {
		t.Errorf("events has %d rows, want 1", n)
	}
}

// The dashboard could once set camera_host and camera_port, and a row it
// stored then must not send the camera login to that host now. The row is
// reported, recorded, and deleted; the other stored settings still apply.
func TestAStoredCameraHostIsDroppedAndTheRestKept(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)
	putOverride(t, dir, "camera_host", "attacker.example")
	putOverride(t, dir, "threshold_db", "60")

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "video is off; camera_host is empty") {
		t.Errorf("the stored camera_host reached the collector:\n%s", stderr)
	}
	if !strings.Contains(stderr, "camera_host") || !strings.Contains(stderr, "dropped") {
		t.Errorf("the log does not say the stored camera_host was dropped:\n%s", stderr)
	}
	db := readDB(t, dir)
	if n := rows(t, db, `SELECT count(*) FROM system_health WHERE detail LIKE '%camera_host%'`); n != 1 {
		t.Errorf("%d health rows name the dropped camera_host, want 1", n)
	}
	if n := rows(t, db, `SELECT count(*) FROM config WHERE key = 'camera_host'`); n != 0 {
		t.Errorf("the camera_host row is still stored")
	}
	// threshold_db = 60 is still in force, so the four jumps are no event.
	if n := rows(t, db, `SELECT count(*) FROM events`); n != 0 {
		t.Errorf("events has %d rows, want 0: the other stored setting was dropped too", n)
	}
}

// A stored setting that does not parse must not stop the collector. It is
// reported and dropped, and the config file alone stays in force, so the owner
// can fix it from the dashboard or the file.
func TestABrokenStoredSettingDoesNotStopTheRun(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)
	putOverride(t, dir, "threshold_db", "banana")

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "threshold_db") {
		t.Errorf("the log does not name the setting it dropped:\n%s", stderr)
	}
	db := readDB(t, dir)
	if n := rows(t, db, `SELECT count(*) FROM samples_1s`); n != 60 {
		t.Errorf("samples_1s has %d rows, want 60: the measurement stopped for a bad setting", n)
	}
	// The file settings are in force, so the four jumps are still an event.
	if n := rows(t, db, `SELECT count(*) FROM events`); n != 1 {
		t.Errorf("events has %d rows, want 1", n)
	}
	if n := rows(t, db, `SELECT count(*) FROM system_health WHERE detail LIKE '%threshold_db%'`); n != 1 {
		t.Errorf("%d health rows name the dropped setting, want 1", n)
	}
}
