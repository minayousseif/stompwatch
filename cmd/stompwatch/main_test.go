package main

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/testsignal"
)

// writeConfig writes a config file with paths in a temporary directory and
// any extra lines. An extra line replaces the default for the same key,
// because the config file refuses a key that is set twice.
func writeConfig(t *testing.T, extra ...string) (path, dir string) {
	t.Helper()
	return writeConfigIn(t, t.TempDir(), extra...)
}

// writeConfigIn is writeConfig into a directory the caller already has, so a
// second config file can point at the database the first one made. Each one
// gets its own name, so two configs can live side by side.
func writeConfigIn(t *testing.T, dir string, extra ...string) (path, out string) {
	t.Helper()
	var lines []string
	for _, line := range []string{
		"expected_capture_gain = none",
		"db_path = " + filepath.Join(dir, "noise.db"),
		"clip_dir = " + filepath.Join(dir, "clips"),
		"log_dir = " + filepath.Join(dir, "logs"),
		// Port 0 asks the kernel for a free port, so two tests never fight
		// over one and none needs the port the box uses.
		"http_addr = 127.0.0.1:0",
	} {
		if !setsSameKey(extra, line) {
			lines = append(lines, line)
		}
	}
	lines = append(lines, extra...)
	path = filepath.Join(dir, fmt.Sprintf("stompwatch-%d.conf", len(extra)))
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

// writeCalibration writes a REW-format calibration file that says the
// microphone reads magDB out across the band the scalar correction is taken
// from, so the correction is the negative of it.
func writeCalibration(t *testing.T, dir, name string, magDB float64) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("* REW calibration\n")
	for _, hz := range []int{20, 100, 800, 1000, 1250, 8000, 20000} {
		fmt.Fprintf(&b, "%d %.4f 0.0\n", hz, magDB)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setsSameKey reports whether one of the lines sets the key that line sets.
func setsSameKey(lines []string, line string) bool {
	key, _, _ := strings.Cut(line, "=")
	for _, other := range lines {
		if k, _, _ := strings.Cut(other, "="); k == key {
			return true
		}
	}
	return false
}

func writeWAV(t *testing.T, dir string, x []float64) string {
	t.Helper()
	var buf bytes.Buffer
	if err := testsignal.WriteWAV24(&buf, x); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCmd(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(append([]string{"stompwatch"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestNoSubcommandPrintsUsage(t *testing.T) {
	code, _, stderr := runCmd()
	if code != 2 || !strings.Contains(stderr, "usage") {
		t.Fatalf("exit %d, stderr %q; want 2 and usage", code, stderr)
	}
	if code, _, _ := runCmd("frobnicate"); code != 2 {
		t.Fatalf("unknown subcommand: exit %d, want 2", code)
	}
}

func TestVerifyDSPPassesWithValidConfig(t *testing.T) {
	cfg, _ := writeConfig(t)
	code, stdout, stderr := runCmd("verify-dsp", "-config", cfg)
	if code != 0 || !strings.Contains(stdout, "checks passed") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

func TestRunRefusesNonHardwareDevice(t *testing.T) {
	cfg, _ := writeConfig(t, "capture_device = default")
	code, _, stderr := runCmd("run", "-config", cfg)
	if code != 1 || !strings.Contains(stderr, "hw:") {
		t.Fatalf("exit %d, stderr %q; want 1 and a message about hw: devices", code, stderr)
	}
}

// SPEC.md section 6.9: refuse to start below the free-space floor.
func TestRunRefusesWhenDiskIsBelowFloor(t *testing.T) {
	cfg, dir := writeConfig(t, "disk_min_free_mb = 999999999999", "disk_warn_free_mb = 999999999999")
	wav := writeWAV(t, dir, make([]float64, testsignal.Rate))
	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 1 || !strings.Contains(stderr, "free") {
		t.Fatalf("exit %d, stderr %q; want 1 and a message about free space", code, stderr)
	}
}

// The whole program on a WAV file: config, store, pipeline, clips, and a
// clean shutdown at the end of the file.
func TestRunProcessesWAVInput(t *testing.T) {
	cfg, dir := writeConfig(t)
	x := testsignal.Pink(60*testsignal.Rate, 0.001, 7)
	testsignal.AddThumps(x, 0.5, 40, 42, 44, 46)
	wav := writeWAV(t, dir, x)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "noise.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for q, want := range map[string]int{
		`SELECT count(*) FROM samples_1s`:  60,
		`SELECT count(*) FROM events`:      1,
		`SELECT count(*) FROM event_media`: 1,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d, %v; want %d", q, n, err, want)
		}
	}
}

// calibrate reads a 94 dB SPL calibrator tone and prints the sensitivity.
func TestCalibrateFromWAVPrintsSensitivity(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := writeWAV(t, dir, calibratorTone(-12.5))
	code, stdout, stderr := runCmd("calibrate", "-config", cfg, "-input", wav)
	if code != 0 || !strings.Contains(stdout, "sensitivity_dbfs = -12.5") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// calibratorTone is a 1 kHz sine at the given AES17 level in dBFS, five
// seconds of it, which is what a calibrator on the microphone gives.
func calibratorTone(dbfs float64) []float64 {
	x := make([]float64, 5*testsignal.Rate)
	a := math.Pow(10, dbfs/20)
	for i := range x {
		x[i] = a * math.Sin(2*math.Pi*1000*float64(i)/testsignal.Rate)
	}
	return x
}

// Pasting what calibrate prints must leave the config file truthful. One
// sensitivity_dbfs line on its own would leave sensitivity_source saying
// datasheet beside a figure that was measured, so the command prints the
// whole block (SPEC.md section 15 decision 23).
func TestCalibratePrintsTheWholeSensitivityBlock(t *testing.T) {
	clockFor(t, time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC))
	cfg, dir := writeConfig(t)
	wav := writeWAV(t, dir, calibratorTone(-12.47))

	code, stdout, stderr := runCmd("calibrate", "-config", cfg, "-input", wav,
		"-reference", "B and K 4231 at 94 dB SPL")
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr)
	}
	for _, want := range []string{
		"sensitivity_dbfs = -12.47",
		"sensitivity_source = measured",
		"sensitivity_measured_on = 2026-10-03",
		"sensitivity_reference = B and K 4231 at 94 dB SPL",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the block has no %q line:\n%s", want, stdout)
		}
	}

	// The block it prints has to be one the config file accepts. A block
	// that will not parse is worse than no block: the owner finds out at
	// the next restart.
	block := sensitivityBlock(t, stdout)
	if _, err := config.Parse(strings.NewReader("expected_capture_gain = none\n" + block)); err != nil {
		t.Errorf("the printed block does not parse:\n%s\n%v", block, err)
	}
}

// With no -reference the line is still printed, empty, and the command says
// what to write on it. A blank line with no hint teaches the owner to leave
// it blank, and then nothing records what the figure was measured against.
func TestCalibrateExplainsTheReferenceLineWhenItIsEmpty(t *testing.T) {
	clockFor(t, time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC))
	cfg, dir := writeConfig(t)
	wav := writeWAV(t, dir, calibratorTone(-12.47))

	code, stdout, stderr := runCmd("calibrate", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "sensitivity_reference =") {
		t.Errorf("the block has no sensitivity_reference line:\n%s", stdout)
	}
	for _, want := range []string{"sensitivity_reference", "held against"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("nothing said what to write on the reference line (%q):\n%s", want, stdout)
		}
	}
	if _, err := config.Parse(strings.NewReader("expected_capture_gain = none\n" + sensitivityBlock(t, stdout))); err != nil {
		t.Errorf("the printed block does not parse: %v", err)
	}
}

// clockFor fixes the clock the commands read, and puts it back afterwards.
func clockFor(t *testing.T, at time.Time) {
	t.Helper()
	was := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = was })
}

// sensitivityBlock pulls the config lines out of what calibrate printed, so
// a test can feed them to the config parser exactly as a paste would.
func sensitivityBlock(t *testing.T, stdout string) string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "sensitivity_") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 4 {
		t.Fatalf("want 4 sensitivity_ lines, got %d:\n%s", len(lines), stdout)
	}
	return strings.Join(lines, "\n") + "\n"
}

// The owner must never be able to forget which mode the box is in. A clip
// filter above the speech-safe cutoff is warned about at every start, by
// name and with what it means. The default is above it, so the warning
// fires on an ordinary install (SPEC.md section 15 decisions 18 and 19).
func TestRunWarnsWhenTheClipFilterIsAboveTheSpeechSafeCutoff(t *testing.T) {
	x := make([]float64, 2*testsignal.Rate)

	cfg, dir := writeConfig(t) // the default, which is above the safe cutoff
	code, _, stderr := runCmd("run", "-config", cfg, "-input", writeWAV(t, dir, x))
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	warning := ""
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "clip") {
			warning = line
		}
	}
	if warning == "" {
		t.Fatalf("no WARN line about the clip filter:\n%s", stderr)
	}
	for _, want := range []string{"1000", "speech"} {
		if !strings.Contains(warning, want) {
			t.Errorf("the warning %q does not mention %q", warning, want)
		}
	}

	// At the speech-safe cutoff there is nothing to warn about, and a
	// warning that appeared every night would teach the owner to ignore it.
	cfg, dir = writeConfig(t, "clip_lowpass_hz = 500")
	code, _, stderr = runCmd("run", "-config", cfg, "-input", writeWAV(t, dir, x))
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "clip_lowpass_hz") {
			t.Errorf("the default cutoff warned anyway: %s", line)
		}
	}
}

// video_main_on_event is parsed, so an old config file still loads, but
// nothing is built behind it. Set to true, it must say so at start rather
// than let the owner believe main-stream clips are being kept.
func TestRunWarnsThatVideoMainOnEventDoesNothing(t *testing.T) {
	x := make([]float64, 2*testsignal.Rate)
	warningsFor := func(extra ...string) []string {
		t.Helper()
		cfg, dir := writeConfig(t, extra...)
		code, _, stderr := runCmd("run", "-config", cfg, "-input", writeWAV(t, dir, x))
		if code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", code, stderr)
		}
		var found []string
		for _, line := range strings.Split(stderr, "\n") {
			if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "video_main_on_event") {
				found = append(found, line)
			}
		}
		return found
	}

	found := warningsFor("video_main_on_event = true")
	if len(found) != 1 {
		t.Fatalf("%d WARN lines name video_main_on_event, want 1: %q", len(found), found)
	}
	for _, want := range []string{"not built", "does nothing"} {
		if !strings.Contains(found[0], want) {
			t.Errorf("the warning %q does not say %q", found[0], want)
		}
	}
	for _, extra := range [][]string{nil, {"video_main_on_event = false"}} {
		if found := warningsFor(extra...); len(found) != 0 {
			t.Errorf("%v warned anyway: %q", extra, found)
		}
	}
}

// The whole program on a WAV file, with the clip filter widened. The clip on
// disk must be at twice the configured cutoff. A collector that read the
// setting, warned about it, and then recorded at the default anyway would
// still pass every other test here.
func TestRunRecordsClipsAtTheConfiguredCutoff(t *testing.T) {
	cfg, dir := writeConfig(t) // the default, which is above the safe cutoff
	x := testsignal.Pink(60*testsignal.Rate, 0.001, 7)
	testsignal.AddThumps(x, 0.5, 40, 42, 44, 46)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", writeWAV(t, dir, x))
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}

	var clips []string
	err := filepath.WalkDir(filepath.Join(dir, "clips"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".wav") {
			clips = append(clips, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(clips) != 1 {
		t.Fatalf("found %d clips, want 1", len(clips))
	}
	b, err := os.ReadFile(clips[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 44 {
		t.Fatalf("%s is %d bytes, too short to be a WAV", clips[0], len(b))
	}
	if got := binary.LittleEndian.Uint32(b[24:]); got != 2000 {
		t.Errorf("the stored clip is at %d Hz, want 2000: twice the configured cutoff", got)
	}
}
