package health

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The expected value comes from df, not from the code under test.
func TestFreeMBMatchesDF(t *testing.T) {
	dir := t.TempDir()
	out, err := exec.Command("df", "-Pk", dir).Output()
	if err != nil {
		t.Skipf("df not available: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	availKB, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		t.Fatalf("parsing df output %q: %v", out, err)
	}

	got, err := FreeMB(dir)
	if err != nil {
		t.Fatalf("FreeMB: %v", err)
	}
	want := availKB / 1024
	if diff := got - want; diff < -64 || diff > 64 {
		t.Fatalf("FreeMB = %d MB, df says %d MB", got, want)
	}
}

func TestFreeMBOfMissingPathIsAnError(t *testing.T) {
	if _, err := FreeMB("/nonexistent/stompwatch-test"); err == nil {
		t.Fatal("FreeMB of a missing path returned no error")
	}
}
