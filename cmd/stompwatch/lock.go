package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockSuffix names the lock file beside the database. One data directory has
// one lock, and the database path is the one path every command already
// knows.
const lockSuffix = ".lock"

// errLocked means another stompwatch holds the lock.
var errLocked = errors.New("another stompwatch holds the lock")

// dataLock is the exclusive lock over one data directory. The collector holds
// it for its whole life, and reset takes it before it removes anything, so
// the data can never be removed under a running collector.
//
// It is an advisory flock on a file beside the database. The kernel drops it
// when the file is closed and when the process dies, however it dies, so a
// collector that is killed leaves no lock behind to clear by hand.
type dataLock struct{ f *os.File }

// lockData takes the lock and fails at once if another stompwatch holds it,
// rather than waiting: a caller that has to wait for the collector to stop
// needs to be told to stop it, not left hanging.
func lockData(dbPath string) (*dataLock, error) {
	if dbPath == "" {
		return nil, errors.New("no database path, so there is nothing to lock")
	}
	path := dbPath + lockSuffix
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("cannot create the data directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("cannot open the lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", errLocked, path)
		}
		return nil, fmt.Errorf("cannot lock %s: %w", path, err)
	}
	return &dataLock{f: f}, nil
}

// release drops the lock.
//
// The lock file itself stays. Removing it would let the next two stompwatchs
// lock two different files, each believing it is alone, which is the fault
// the lock exists to prevent.
func (l *dataLock) release() {
	if l != nil && l.f != nil {
		l.f.Close()
	}
}

// lockPath is the lock file for a database path, for a message that names it.
func lockPath(dbPath string) string { return dbPath + lockSuffix }
