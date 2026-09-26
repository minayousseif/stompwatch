package logs

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const name = "stompwatch.log"

// line is exactly 20 bytes long, including the newline, so a test can say
// how many of them fit in a file.
func line(n int) string {
	s := fmt.Sprintf("line-%014d\n", n)
	if len(s) != 20 {
		panic("test line is " + fmt.Sprint(len(s)) + " bytes, want 20")
	}
	return s
}

// read returns the lines of one log file, oldest first, each with its
// newline. It fails if the file is missing.
func read(t *testing.T, dir, file string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	lines := strings.SplitAfter(string(b), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

func TestNewWriterCreatesTheDirectoryAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 1000, Keep: 3})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o750 {
		t.Errorf("directory mode = %v, want -rwxr-x---", di.Mode().Perm())
	}
	fi, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("file mode = %v, want -rw-r-----", fi.Mode().Perm())
	}
}

// The current file must never pass MaxBytes, and a line must never be split
// across two files.
func TestWriterRotatesBeforeTheWriteThatWouldOverflow(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 100, Keep: 3})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// Five 20-byte lines fill the file exactly. The sixth rotates it.
	for i := 1; i <= 12; i++ {
		if _, err := w.Write([]byte(line(i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	want := map[string][]string{
		name:        {line(11), line(12)},
		name + ".1": {line(6), line(7), line(8), line(9), line(10)},
		name + ".2": {line(1), line(2), line(3), line(4), line(5)},
	}
	for file, lines := range want {
		got := read(t, dir, file)
		if strings.Join(got, "") != strings.Join(lines, "") {
			t.Errorf("%s holds %q, want %q", file, got, lines)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// Keep counts the current file too, so the oldest is deleted, not kept.
func TestWriterKeepsTheAgreedNumberOfFiles(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 100, Keep: 3})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	for i := 1; i <= 16; i++ {
		if _, err := w.Write([]byte(line(i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	want := map[string][]string{
		name:        {line(16)},
		name + ".1": {line(11), line(12), line(13), line(14), line(15)},
		name + ".2": {line(6), line(7), line(8), line(9), line(10)},
	}
	for file, lines := range want {
		got := read(t, dir, file)
		if strings.Join(got, "") != strings.Join(lines, "") {
			t.Errorf("%s holds %q, want %q", file, got, lines)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, name+".3")); !os.IsNotExist(err) {
		t.Errorf("%s.3 exists; Keep = 3 means three files in total", name)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("the directory holds %d files, want 3", len(entries))
	}
}

// Keep = 1 means no history at all.
func TestWriterKeepOneDropsTheOldFile(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 100, Keep: 1})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	for i := 1; i <= 6; i++ {
		if _, err := w.Write([]byte(line(i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if got := read(t, dir, name); strings.Join(got, "") != line(6) {
		t.Errorf("%s holds %q, want %q", name, got, line(6))
	}
	if _, err := os.Stat(filepath.Join(dir, name+".1")); !os.IsNotExist(err) {
		t.Errorf("%s.1 exists; Keep = 1 keeps only the current file", name)
	}
}

// A line longer than MaxBytes is written whole. A log that cuts the message
// in half hides the thing that went wrong.
func TestWriterWritesALongLineWhole(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 100, Keep: 3})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	long := strings.Repeat("x", 250) + "\n"
	if _, err := w.Write([]byte(line(1))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write([]byte(long)); err != nil {
		t.Fatalf("Write long: %v", err)
	}
	if _, err := w.Write([]byte(line(2))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := read(t, dir, name); strings.Join(got, "") != line(2) {
		t.Errorf("%s holds %q, want %q", name, got, line(2))
	}
	if got := read(t, dir, name+".1"); strings.Join(got, "") != long {
		t.Errorf("%s.1 holds %d bytes, want the whole %d-byte line", name, len(strings.Join(got, "")), len(long))
	}
	if got := read(t, dir, name+".2"); strings.Join(got, "") != line(1) {
		t.Errorf("%s.2 holds %q, want %q", name, got, line(1))
	}
}

// One Write may carry several records. Each one still has to land whole.
func TestWriterSplitsAMultiRecordWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 100, Keep: 3})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	var b strings.Builder
	for i := 1; i <= 7; i++ {
		b.WriteString(line(i))
	}
	n, err := w.Write([]byte(b.String()))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 140 {
		t.Errorf("Write returned %d, want 140", n)
	}
	if got := read(t, dir, name); strings.Join(got, "") != line(6)+line(7) {
		t.Errorf("%s holds %q, want lines 6 and 7", name, got)
	}
	if got := read(t, dir, name+".1"); len(got) != 5 {
		t.Errorf("%s.1 holds %d lines, want 5", name, len(got))
	}
}

// A write with no trailing newline must not be joined to the next one.
func TestWriterFinishesAPartialRecord(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 1000, Keep: 3})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("no newline here")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write([]byte(line(1))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(b) != "no newline here\n"+line(1) {
		t.Errorf("the file holds %q", b)
	}
}

// Restarting the program must continue the same file, not lose it.
func TestWriterAppendsToAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Dir: dir, Name: name, MaxBytes: 100, Keep: 3}
	w, err := NewWriter(cfg)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := w.Write([]byte(line(i))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w2, err := NewWriter(cfg)
	if err != nil {
		t.Fatalf("NewWriter again: %v", err)
	}
	defer w2.Close()
	// The file already holds 80 bytes, so the second line here rotates it.
	for i := 5; i <= 6; i++ {
		if _, err := w2.Write([]byte(line(i))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if got := read(t, dir, name); strings.Join(got, "") != line(6) {
		t.Errorf("%s holds %q, want line 6", name, got)
	}
	if got := read(t, dir, name+".1"); len(got) != 5 {
		t.Errorf("%s.1 holds %d lines, want 5", name, len(got))
	}
}

func TestNewWriterRejectsABadConfig(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		c    Config
	}{
		{"no name", Config{Dir: dir, Name: "", MaxBytes: 100, Keep: 3}},
		{"name with a path", Config{Dir: dir, Name: "a/b.log", MaxBytes: 100, Keep: 3}},
		{"no size limit", Config{Dir: dir, Name: name, MaxBytes: 0, Keep: 3}},
		{"keeps nothing", Config{Dir: dir, Name: name, MaxBytes: 100, Keep: 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWriter(tc.c); err == nil {
				t.Fatalf("NewWriter(%+v) returned no error", tc.c)
			}
		})
	}
}

// The writer exists to be the destination of the program's JSON logger.
func TestWriterTakesSlogRecords(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 1 << 20, Keep: 3})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	log := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Warn("free space is low", "dir", "/data", "free_mb", 900)
	log.Info("started")

	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(got) != 2 {
		t.Fatalf("the file holds %d lines, want 2: %q", len(got), b)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(got[0]), &rec); err != nil {
		t.Fatalf("the first line is not JSON: %v", err)
	}
	if rec["level"] != "WARN" || rec["msg"] != "free space is low" || rec["dir"] != "/data" {
		t.Errorf("the first record is %v", rec)
	}
}

func TestWriterIsSafeForConcurrentUse(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 200, Keep: 4})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if _, err := w.Write([]byte(line(i))); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Every line that survived rotation must still be 20 bytes long.
	for i := 0; i < 4; i++ {
		file := name
		if i > 0 {
			file = fmt.Sprintf("%s.%d", name, i)
		}
		b, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			continue
		}
		for _, l := range strings.SplitAfter(string(b), "\n") {
			if l != "" && len(l) != 20 {
				t.Fatalf("%s holds a %d-byte line %q", file, len(l), l)
			}
		}
	}
}
