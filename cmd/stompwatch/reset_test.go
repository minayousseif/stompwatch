package main

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/store"
)

// box is a box laid out the way the real one is: the configuration in one
// directory and the data in another, so a test can tell the two apart.
type box struct {
	cfg     string // the config file
	cfgDir  string // /etc/stompwatch on the box
	dataDir string // /data on the box
}

func (b box) db() string        { return filepath.Join(b.dataDir, "noise.db") }
func (b box) clips() string     { return filepath.Join(b.dataDir, "clips") }
func (b box) video() string     { return filepath.Join(b.dataDir, "video") }
func (b box) snapshots() string { return filepath.Join(b.dataDir, "snapshots") }
func (b box) logs() string      { return filepath.Join(b.dataDir, "logs") }

// newBox writes a config file with every path inside the temporary
// directories. Every path is set: the defaults point at /data, and a test
// that left one there would act on the real box's directories.
func newBox(t *testing.T, extra ...string) box {
	t.Helper()
	root := t.TempDir()
	b := box{cfgDir: filepath.Join(root, "etc"), dataDir: filepath.Join(root, "data")}
	b.cfg = filepath.Join(b.cfgDir, "stompwatch.conf")
	for _, dir := range []string{b.cfgDir, b.dataDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	lines := []string{
		"expected_capture_gain = none",
		"db_path = " + b.db(),
		"clip_dir = " + b.clips(),
		"video_dir = " + b.video(),
		"log_dir = " + b.logs(),
		"http_addr = 127.0.0.1:0",
	}
	var kept []string
	for _, line := range lines {
		if !setsSameKey(extra, line) {
			kept = append(kept, line)
		}
	}
	kept = append(kept, extra...)
	if err := os.WriteFile(b.cfg, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

// set adds settings to the config file. The key must not be in it already:
// the config file refuses a key that is set twice.
func (b box) set(t *testing.T, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(b.cfg, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

// seed fills the data directory: two events, three measured seconds, one
// audio clip and one video clip with their files, a snapshot, and a log.
// Every count here is stated, so the report has something to be wrong about.
func (b box) seed(t *testing.T) {
	t.Helper()
	st, err := store.Open(b.db())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bins := []meter.Bin{}
	for i := range 3 {
		bins = append(bins, meter.Bin{
			Start:   time.Now().Add(time.Duration(i) * time.Second).Truncate(time.Second),
			Samples: 48000, LAeq: 40, LAmax: 45, LowBand: 35, HighBand: 20, Baseline: 30,
		})
	}
	if _, err := st.InsertBins(ctx, bins); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := range 2 {
		start := time.Now().Add(time.Duration(i) * time.Minute)
		e := detect.Event{
			Start: start, End: start.Add(time.Second),
			LAeq: 65, LAmax: 70, BaselineAtTrigger: 35,
			LowBand: 60, HighBand: 45, LowHighRatioDB: 15,
			Class: detect.Jumping, Confidence: 0.5, Envelope: []float64{0.5, 1},
		}
		id, err := st.InsertEvent(ctx, e, start)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, m := range []store.Media{
		{EventID: ids[0], Kind: store.KindAudio, Path: "2026-09-12/clip.wav", Bytes: 100, SHA256: "a"},
		{EventID: ids[0], Kind: store.KindVideo, Path: "2026-09-12/clip.mp4", Bytes: 200, SHA256: "b"},
	} {
		if err := st.InsertMedia(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{store.HealthCaptureGap, store.HealthDiskLow} {
		if err := st.AddHealth(ctx, time.Now(), kind, "seeded", 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// The two files SQLite keeps beside a database it was writing when the
	// collector was killed. What is in them does not matter here; that reset
	// takes them with the database does.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(b.db()+suffix, nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	b.write(t, filepath.Join(b.clips(), "2026-09-12", "clip.wav"), 100)
	b.write(t, filepath.Join(b.video(), "2026-09-12", "clip.mp4"), 200)
	b.write(t, filepath.Join(b.snapshots(), "noise-2026-09-12.sqlite"), 300)
	b.write(t, filepath.Join(b.logs(), "stompwatch.log"), 400)
}

// write makes a file of n bytes, with every directory above it at mode 0750,
// the mode the installer gives the data directories.
func (b box) write(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	for dir := filepath.Dir(path); strings.HasPrefix(dir, b.dataDir); dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, make([]byte, n), 0o640); err != nil {
		t.Fatal(err)
	}
}

// tree lists every path under dir with the size of each file. Directories are
// listed too, with a size of -1, so a directory that was removed along with
// its contents is caught.
func tree(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := make(map[string]int64)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[path] = -1
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// inode names a directory on the disk. Two directories at the same path with
// different inodes are not the same directory.
func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("this system does not report inodes")
	}
	return uint64(st.Ino)
}

// transient are the files beside the database that are not data: the lock
// every stompwatch takes, and the two files SQLite keeps beside a database in
// write-ahead mode. Reading the database to count it creates them, so they may
// appear where they were not before. Nothing else may.
var transient = map[string]bool{
	"noise.db.lock": true, "noise.db-wal": true, "noise.db-shm": true,
}

// scratch is the shared-memory index SQLite keeps beside the database. It
// holds no record of anything and SQLite rebuilds it to read, so its size is
// the one size that may change under a preview. The database itself and its
// write-ahead log may not.
func scratch(path string) bool { return filepath.Base(path) == "noise.db-shm" }

func sameTree(t *testing.T, what string, before, after map[string]int64) {
	t.Helper()
	for path, size := range before {
		if got, ok := after[path]; !ok {
			t.Errorf("%s: %s is gone", what, path)
		} else if got != size && !scratch(path) {
			t.Errorf("%s: %s is %d bytes, was %d", what, path, got, size)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok && !transient[filepath.Base(path)] {
			t.Errorf("%s: %s appeared", what, path)
		}
	}
}

// field reads what the report printed after a label, which is the rest of
// that line.
func field(t *testing.T, out, label string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); strings.HasPrefix(s, label) {
			return strings.TrimSpace(strings.TrimPrefix(s, label))
		}
	}
	t.Fatalf("the report has no line for %q:\n%s", label, out)
	return ""
}

func wantField(t *testing.T, out, label, want string) {
	t.Helper()
	if got := field(t, out, label); got != want {
		t.Errorf("%q = %q, want %q", label, got, want)
	}
}

// --- the preview ---

// Without -yes nothing is removed. That is the default, so a mistyped
// command is harmless, and the report is what the owner reads to decide.
func TestResetWithoutYesRemovesNothing(t *testing.T) {
	b := newBox(t)
	b.seed(t)
	before := tree(t, b.dataDir)

	code, stdout, stderr := runCmd("reset", "-config", b.cfg)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	sameTree(t, "after a preview", before, tree(t, b.dataDir))

	// The counts of what is in the database, each one its own.
	wantField(t, stdout, "events", "2")
	wantField(t, stdout, "measured seconds", "3")
	wantField(t, stdout, "audio clips", "1")
	wantField(t, stdout, "video clips", "1")
	wantField(t, stdout, "reviews", "0")
	wantField(t, stdout, "health rows", "2")

	// What it would remove, with the bytes of each directory.
	for label, want := range map[string]string{
		"the audio clips": b.clips() + " (1 file, 100 bytes)",
		"the video clips": b.video() + " (1 file, 200 bytes)",
		"the snapshots":   b.snapshots() + " (1 file, 300 bytes)",
		"the logs":        b.logs() + " (1 file, 400 bytes)",
	} {
		wantField(t, stdout, label, want)
	}
	if got := field(t, stdout, "the database"); !strings.HasPrefix(got, b.db()+" (") {
		t.Errorf("the database line = %q, want the path and its bytes", got)
	}
	wantField(t, stdout, "its -wal file", b.db()+"-wal (0 bytes)")
	if got := field(t, stdout, "its -shm file"); !strings.HasPrefix(got, b.db()+"-shm (") {
		t.Errorf("the -shm line = %q, want the path and its bytes", got)
	}
	if !strings.Contains(stdout, "Nothing was removed") {
		t.Errorf("the preview does not say that nothing was removed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "-yes") {
		t.Errorf("the preview does not name the -yes flag:\n%s", stdout)
	}
}

// --- what it removes, and what it keeps ---

func TestResetRemovesTheContentsAndKeepsEveryDirectory(t *testing.T) {
	b := newBox(t)
	b.seed(t)
	// A file in the data directory that belongs to nothing configured. Only
	// the three database files go from there.
	b.write(t, filepath.Join(b.dataDir, "notes.txt"), 7)
	t.Setenv("SUDO_USER", "tester")
	t.Setenv("USER", "root")
	inodes := map[string]uint64{}
	for _, dir := range []string{b.clips(), b.video(), b.snapshots(), b.logs()} {
		inodes[dir] = inode(t, dir)
	}

	code, stdout, stderr := runCmd("reset", "-config", b.cfg, "-yes")
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	for _, gone := range []string{
		b.db() + "-wal",
		b.db() + "-shm",
		filepath.Join(b.clips(), "2026-09-12", "clip.wav"),
		filepath.Join(b.clips(), "2026-09-12"),
		filepath.Join(b.video(), "2026-09-12", "clip.mp4"),
		filepath.Join(b.snapshots(), "noise-2026-09-12.sqlite"),
		filepath.Join(b.logs(), "stompwatch.log"),
	} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	// Every configured directory is the same directory afterwards, with the
	// mode it had, so its owner survives and the service user can still write
	// into it. The inode is the test for that: a directory removed and made
	// again has a new one, and belongs to whoever made it.
	for _, dir := range []string{b.clips(), b.video(), b.snapshots(), b.logs()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("%s was removed, not cleared: %v", dir, err)
			continue
		}
		if got := info.Mode().Perm(); got != 0o750 {
			t.Errorf("%s is mode %o, was 750", dir, got)
		}
		if got := inode(t, dir); got != inodes[dir] {
			t.Errorf("%s is inode %d, was %d: it was removed and made again, "+
				"so it belongs to whoever ran reset", dir, got, inodes[dir])
		}
	}
	if _, err := os.Stat(filepath.Join(b.dataDir, "notes.txt")); err != nil {
		t.Errorf("a file beside the database was removed: %v", err)
	}
	// The lock file stays. Removing it would let the next two stompwatchs lock
	// two different files and both believe they are alone.
	if _, err := os.Stat(b.db() + ".lock"); err != nil {
		t.Errorf("the lock file was removed: %v", err)
	}
	// The report says it is taking all three database files with it.
	for _, want := range []string{b.db() + "-wal", b.db() + "-shm"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not name %s:\n%s", want, stdout)
		}
	}

	// The database is new and holds one record of the reset, and nothing
	// else. The audit trail is the one thing that survives a wipe.
	db, err := sql.Open("sqlite", "file:"+b.db()+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for q, want := range map[string]int{
		`SELECT count(*) FROM events`:        0,
		`SELECT count(*) FROM samples_1s`:    0,
		`SELECT count(*) FROM event_media`:   0,
		`SELECT count(*) FROM system_health`: 1,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		} else if n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	var kind, detail string
	var tsMS int64
	if err := db.QueryRow(`SELECT kind, detail, ts_ms FROM system_health`).Scan(&kind, &detail, &tsMS); err != nil {
		t.Fatal(err)
	}
	if kind != "data_reset" {
		t.Errorf("the record of the reset is kind %q, want data_reset", kind)
	}
	if !strings.Contains(detail, "tester") {
		t.Errorf("the record does not say who reset the data: %q", detail)
	}
	for _, want := range []string{"2 events", "3 measured seconds", "1 audio clip", "1 video clip"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the record does not say it removed %s: %q", want, detail)
		}
	}
	if age := time.Since(time.UnixMilli(tsMS)); age < 0 || age > time.Minute {
		t.Errorf("the record is stamped %v ago", age)
	}
	if !strings.Contains(stdout, "Removed") {
		t.Errorf("the report does not say what it did:\n%s", stdout)
	}
}

// The configuration, the calibration, and the camera login are not data. One
// slip of an rm removes them and the box measures wrongly or not at all.
func TestResetKeepsTheConfigurationFiles(t *testing.T) {
	b := newBox(t)
	cal := filepath.Join(b.cfgDir, "mic.cal")
	creds := filepath.Join(b.cfgDir, "camera.env")
	b.set(t, "calibration_file = "+cal, "camera_credentials_file = "+creds)
	if err := os.WriteFile(cal, []byte("20 1.0 0\n1000 0.0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte("STOMPWATCH_CAMERA_USER=someone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.seed(t)
	before := tree(t, b.cfgDir)

	code, stdout, stderr := runCmd("reset", "-config", b.cfg, "-yes")
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	sameTree(t, "the config directory", before, tree(t, b.cfgDir))
	wantField(t, stdout, "the config file", b.cfg)
	wantField(t, stdout, "the calibration file", cal)
	wantField(t, stdout, "the credentials file", creds)
}

// A directory that holds the config file must never be cleared. The paths
// come from a file a person edits, and a config file inside log_dir would
// otherwise cost the configuration.
func TestResetRefusesToClearADirectoryThatHoldsTheConfigFile(t *testing.T) {
	b := newBox(t)
	b.seed(t)
	inside := filepath.Join(b.logs(), "stompwatch.conf")
	read, err := os.ReadFile(b.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, read, 0o600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, b.dataDir)

	code, stdout, stderr := runCmd("reset", "-config", inside, "-yes")
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "log_dir") {
		t.Errorf("stderr does not name the setting at fault:\n%s", stderr)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("the config file was removed: %v", err)
	}
	sameTree(t, "after a refusal", before, tree(t, b.dataDir))
}

// --- the guards ---

// The data must not be removed under a running collector: it is writing the
// database and cutting clips as the files go.
func TestResetRefusesWhileTheCollectorHoldsTheLock(t *testing.T) {
	b := newBox(t)
	b.seed(t)
	holdLock(t, b.db())
	before := tree(t, b.dataDir)

	code, stdout, stderr := runCmd("reset", "-config", b.cfg, "-yes")
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	sameTree(t, "after a refusal", before, tree(t, b.dataDir))
	if !strings.Contains(stderr, "systemctl stop stompwatch") {
		t.Errorf("stderr does not give the command to stop the service:\n%s", stderr)
	}
}

// A review is a person's decision about a night. Its presence means this is
// not test data, and the owner has to say so a second time.
func TestResetRefusesWhenAnEventIsReviewed(t *testing.T) {
	b := newBox(t)
	b.seed(t)
	st, err := store.Open(b.db())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetReview(context.Background(), store.Review{
		EventID: 1, Status: store.StatusVerified, Reviewer: "alex", At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	before := tree(t, b.dataDir)

	code, stdout, stderr := runCmd("reset", "-config", b.cfg, "-yes")
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	sameTree(t, "after a refusal", before, tree(t, b.dataDir))
	if !strings.Contains(stderr, "-reviewed") {
		t.Errorf("stderr does not name the flag that goes past it:\n%s", stderr)
	}
	if !strings.Contains(stderr, "1") {
		t.Errorf("stderr does not say how many events are reviewed:\n%s", stderr)
	}
	wantField(t, stdout, "reviews", "1")

	// The flag is the way past it, and it needs -yes as well.
	code, stdout, stderr = runCmd("reset", "-config", b.cfg, "-reviewed")
	if code != 0 {
		t.Fatalf("with -reviewed alone: exit %d, want the preview\nstderr:\n%s", code, stderr)
	}
	sameTree(t, "after -reviewed without -yes", before, tree(t, b.dataDir))

	code, stdout, stderr = runCmd("reset", "-config", b.cfg, "-reviewed", "-yes")
	if code != 0 {
		t.Fatalf("with -reviewed -yes: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(b.clips(), "2026-09-12", "clip.wav")); err == nil {
		t.Error("-reviewed -yes did not remove the clip")
	}
}

// A config file that does not parse, or one that does not say where the data
// is, leaves reset nothing safe to do. It must never guess a path.
func TestResetRefusesAConfigItCannotUse(t *testing.T) {
	good := newBox(t)
	good.seed(t)
	before := tree(t, good.dataDir)

	if err := os.WriteFile(filepath.Join(good.cfgDir, "broken.conf"),
		[]byte("db_path = "+good.db()+"\nnonsense\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCmd("reset", "-config", filepath.Join(good.cfgDir, "broken.conf"), "-yes")
	if code != 1 {
		t.Fatalf("a config file that does not parse: exit %d, want 1\nstderr:\n%s", code, stderr)
	}
	sameTree(t, "after a config that does not parse", before, tree(t, good.dataDir))

	code, _, stderr = runCmd("reset", "-config", filepath.Join(good.cfgDir, "missing.conf"), "-yes")
	if code != 1 {
		t.Fatalf("a config file that is not there: exit %d, want 1\nstderr:\n%s", code, stderr)
	}

	empty := newBox(t, "db_path =")
	code, _, stderr = runCmd("reset", "-config", empty.cfg, "-yes")
	if code != 1 {
		t.Fatalf("an empty db_path: exit %d, want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "db_path") {
		t.Errorf("stderr does not name db_path:\n%s", stderr)
	}

	noClips := newBox(t, "clip_dir =")
	code, _, stderr = runCmd("reset", "-config", noClips.cfg, "-yes")
	if code != 1 {
		t.Fatalf("an empty clip_dir: exit %d, want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "clip_dir") {
		t.Errorf("stderr does not name clip_dir:\n%s", stderr)
	}
}

// --- the containment check ---

// Every path reset removes is confirmed to be inside the directory it was
// read from. A path that leads out of it is refused, not removed.
func TestRemoveInsideRefusesAPathOutsideItsRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "keep", "clip.wav")
	if err := os.MkdirAll(filepath.Dir(inside), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "stompwatch.conf")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	for _, path := range []string{
		outside,
		filepath.Join(root, "..", filepath.Base(outside)),
		filepath.Join(root, "keep", "..", "..", filepath.Base(outside)),
	} {
		if err := removeInside(root, path); err == nil {
			t.Errorf("removeInside(%q, %q) removed it; want a refusal", root, path)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("%s was removed from outside %s", outside, root)
		}
	}
	// The root itself is not something inside it.
	if err := removeInside(root, root); err == nil {
		t.Error("removeInside removed the root directory itself")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the root directory was removed: %v", err)
	}
	// Something really inside it goes.
	if err := removeInside(root, filepath.Join(root, "keep")); err != nil {
		t.Errorf("removeInside on a directory inside the root: %v", err)
	}
	if _, err := os.Stat(inside); err == nil {
		t.Error("the file inside the root is still there")
	}
}

// --- who ran it ---

// The audit row names a person. stompwatch reset is run with sudo, so the
// process is root and the name worth recording is the one sudo kept.
func TestResetUserPrefersTheNameSudoKept(t *testing.T) {
	env := func(pairs map[string]string) func(string) string {
		return func(k string) string { return pairs[k] }
	}
	for _, c := range []struct {
		name  string
		pairs map[string]string
		want  string
	}{
		{"sudo", map[string]string{"SUDO_USER": "alex", "USER": "root"}, "alex"},
		{"no sudo", map[string]string{"USER": "alex"}, "alex"},
		{"only a login name", map[string]string{"LOGNAME": "alex"}, "alex"},
		{"nothing set", map[string]string{}, "unknown"},
		{"empty values", map[string]string{"SUDO_USER": "", "USER": "alex"}, "alex"},
	} {
		if got := resetUser(env(c.pairs)); got != c.want {
			t.Errorf("%s: resetUser = %q, want %q", c.name, got, c.want)
		}
	}
}

// --- the whole way round ---

// The collector writes the data, stops, and reset clears what it wrote. It is
// the real layout rather than a seeded one, and it proves the collector
// releases the lock when it stops.
func TestResetClearsWhatTheCollectorWrote(t *testing.T) {
	b := newBox(t)
	wav := writeWAV(t, b.dataDir, make([]float64, 2*48000))
	code, _, stderr := runCmd("run", "-config", b.cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("the collector exited %d\nstderr:\n%s", code, stderr)
	}
	log := filepath.Join(b.logs(), logFileName)
	if _, err := os.Stat(log); err != nil {
		t.Fatalf("the collector wrote no log file: %v", err)
	}

	code, stdout, stderr := runCmd("reset", "-config", b.cfg, "-yes")
	if code != 0 {
		t.Fatalf("reset exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("the log file the collector wrote is still there")
	}
	if _, err := os.Stat(wav); err != nil {
		t.Errorf("the input file beside the database was removed: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+b.db()+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM samples_1s`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the new database holds %d measured seconds, want 0", n)
	}
}

// --- the user that runs it ---

// reset is run with sudo, and root can remove anything and create anything.
// The database and the lock file it leaves behind would then belong to root,
// in directories the service user owns, and the collector could open neither.
func TestResetRefusesToRunAsRootOverAnotherUsersData(t *testing.T) {
	const dir, cfg = "/data", "/etc/stompwatch/stompwatch.conf"
	for _, c := range []struct {
		name         string
		euid, dirUID int
		wantRefusal  bool
	}{
		{"root over root's own data", 0, 0, false},
		{"the service user over its own data", 997, 997, false},
		{"a person over their own data", 501, 501, false},
		{"a person over another user's data", 501, 997, false},
		{"root over the service user's data", 0, 997, true},
	} {
		got := ownerRefusal(c.euid, c.dirUID, "stompwatch", dir, cfg, false)
		if (got != "") != c.wantRefusal {
			t.Errorf("%s: refusal %q, want a refusal: %v", c.name, got, c.wantRefusal)
			continue
		}
		if !c.wantRefusal {
			continue
		}
		for _, want := range []string{"stompwatch", dir, "sudo -u stompwatch stompwatch reset -config " + cfg + " -yes"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: the refusal does not mention %q:\n%s", c.name, want, got)
			}
		}
	}
}

// A refusal hands the owner the line to run, with the flags that were given,
// so nothing has to be worked out again.
func TestResetCommandRepeatsTheFlagsGiven(t *testing.T) {
	for _, c := range []struct {
		user, cfg string
		reviewed  bool
		want      string
	}{
		{"", "/etc/stompwatch/stompwatch.conf", false,
			"sudo stompwatch reset -config /etc/stompwatch/stompwatch.conf -yes"},
		{"", "/etc/stompwatch/stompwatch.conf", true,
			"sudo stompwatch reset -config /etc/stompwatch/stompwatch.conf -reviewed -yes"},
		{"stompwatch", "/tmp/other.conf", true,
			"sudo -u stompwatch stompwatch reset -config /tmp/other.conf -reviewed -yes"},
	} {
		if got := resetCommand(c.user, c.cfg, c.reviewed); got != c.want {
			t.Errorf("resetCommand(%q, %q, %v) = %q, want %q", c.user, c.cfg, c.reviewed, got, c.want)
		}
	}
}

// --- the settings saved from the dashboard ---

// saveSettings stores settings the way the dashboard does, in the config
// table of the database a reset removes.
func (b box) saveSettings(t *testing.T, values map[string]string) {
	t.Helper()
	st, err := store.Open(b.db())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	put := make(map[string]*string, len(values))
	for k, v := range values {
		put[k] = &v
	}
	if err := st.PutSettings(context.Background(), put, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// pasteBlock is the indented block of lines the report prints after the
// sentence that mentions the dashboard, trimmed, or nil when there is none.
func pasteBlock(out string) []string {
	lines := strings.Split(out, "\n")
	i := 0
	for i < len(lines) && !strings.Contains(lines[i], "dashboard") {
		i++
	}
	for i < len(lines) && !strings.HasPrefix(lines[i], "    ") {
		i++
	}
	var block []string
	for ; i < len(lines) && strings.HasPrefix(lines[i], "    "); i++ {
		block = append(block, strings.TrimSpace(lines[i]))
	}
	return block
}

// The database holds the settings saved from the dashboard, recording_pause
// among them, which keeps the box from recording in hours the owner
// excluded. A reset removes them, so the preview and the real run both
// print them as lines for the config file. camera_host is not one of them:
// only the config file may set it, and the collector never uses such a row.
func TestResetPrintsTheDashboardSettingsItRemoves(t *testing.T) {
	for _, args := range [][]string{nil, {"-yes"}} {
		b := newBox(t)
		b.seed(t)
		b.saveSettings(t, map[string]string{
			"recording_pause": "22:00-07:00",
			"threshold_db":    "21",
			"camera_host":     "attacker.example",
		})

		code, stdout, stderr := runCmd(append([]string{"reset", "-config", b.cfg}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
		}
		got := pasteBlock(stdout)
		want := []string{"recording_pause = 22:00-07:00", "threshold_db = 21"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%v: printed settings %q, want %q\nstdout:\n%s", args, got, want, stdout)
		}

		// The lines paste into the config file as they stand.
		b.set(t, got...)
		c, err := config.Load(b.cfg)
		if err != nil {
			t.Fatalf("%v: the config file with the printed lines does not load: %v", args, err)
		}
		if c.ThresholdDB != 21 || len(c.RecordingPause) != 1 {
			t.Errorf("%v: after pasting, threshold_db = %v and %d pause windows, want 21 and 1",
				args, c.ThresholdDB, len(c.RecordingPause))
		}
	}
}

// With no saved settings there is nothing to lose, and nothing extra is said.
func TestResetSaysNothingOfSettingsWhenNoneAreSaved(t *testing.T) {
	for _, args := range [][]string{nil, {"-yes"}} {
		b := newBox(t)
		b.seed(t)
		code, stdout, stderr := runCmd(append([]string{"reset", "-config", b.cfg}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
		}
		if strings.Contains(stdout, "dashboard") || strings.Contains(stdout, " = ") {
			t.Errorf("%v: the report speaks of saved settings when there are none:\n%s", args, stdout)
		}
	}
}
