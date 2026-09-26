package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/testsignal"
)

// holdLock takes the lock a running collector holds, the way a second
// stompwatch would. The lock file and the kind of lock are written out here
// rather than taken from the program, so a change to either has to be meant.
func holdLock(t *testing.T, dbPath string) {
	t.Helper()
	f, err := os.OpenFile(dbPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the test cannot take the lock it needs to hold: %v", err)
	}
	t.Cleanup(func() { f.Close() })
}

// lockTaken reports whether something holds the lock beside the database.
func lockTaken(dbPath string) bool {
	f, err := os.OpenFile(dbPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// Two collectors on one data directory would both write the database and
// both cut clips. The second must refuse to start.
func TestRunRefusesWhenAnotherStompwatchHoldsTheLock(t *testing.T) {
	cfg, dir := writeConfig(t)
	db := filepath.Join(dir, "noise.db")
	holdLock(t, db)
	wav := writeWAV(t, dir, make([]float64, testsignal.Rate))

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "already running") {
		t.Errorf("stderr does not say another stompwatch is already running:\n%s", stderr)
	}
	if _, err := os.Stat(db); err == nil {
		t.Error("the collector that refused to start created the database anyway")
	}
}

// The lock is held for the whole life of the collector, not only tested at
// startup: reset takes the same lock, and a lock released early would let it
// remove the data under a running collector.
func TestRunHoldsTheLockWhileItRuns(t *testing.T) {
	cfg, dir := writeConfig(t)
	db := filepath.Join(dir, "noise.db")
	wav := thumps(t, dir)

	done := make(chan int, 1)
	go func() {
		code, _, _ := runCmd("run", "-config", cfg, "-input", wav)
		done <- code
	}()

	held := false
	for start := time.Now(); time.Since(start) < 30*time.Second; {
		if lockTaken(db) {
			held = true
			break
		}
		select {
		case code := <-done:
			t.Fatalf("the run finished with exit %d before the test saw the lock held", code)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	if !held {
		t.Error("the collector never held the lock while it ran")
	}
	if code := <-done; code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if lockTaken(db) {
		t.Error("the collector kept the lock after it stopped")
	}
}
