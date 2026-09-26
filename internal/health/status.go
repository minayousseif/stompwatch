package health

import (
	"fmt"
	"sync/atomic"
	"time"
)

// Status records when audio last arrived and when bins were last committed
// to the database. Its methods are safe for concurrent use.
type Status struct {
	lastAudio  atomic.Int64 // Unix nanoseconds
	lastCommit atomic.Int64
}

// NewStatus returns a status that counts both times from start, so a
// collector that never produces anything is caught once the limits pass.
func NewStatus(start time.Time) *Status {
	s := &Status{}
	s.lastAudio.Store(start.UnixNano())
	s.lastCommit.Store(start.UnixNano())
	return s
}

// AudioArrived records that audio arrived at t.
func (s *Status) AudioArrived(t time.Time) { s.lastAudio.Store(t.UnixNano()) }

// BinsCommitted records that bins were committed at t.
func (s *Status) BinsCommitted(t time.Time) { s.lastCommit.Store(t.UnixNano()) }

// LastAudio returns when audio last arrived. The system page shows it, so
// the owner can see how long the instrument has been deaf.
func (s *Status) LastAudio() time.Time { return time.Unix(0, s.lastAudio.Load()) }

// LastCommit returns when bins were last committed.
func (s *Status) LastCommit() time.Time { return time.Unix(0, s.lastCommit.Load()) }

// Check returns nil if collection is healthy at now: audio arrived within
// audioLimit and bins were committed within commitLimit. Otherwise it
// returns an error that says what has stalled.
func (s *Status) Check(now time.Time, commitLimit, audioLimit time.Duration) error {
	if d := now.Sub(time.Unix(0, s.lastAudio.Load())); d > audioLimit {
		return fmt.Errorf("no audio for %v", d.Round(time.Second))
	}
	if d := now.Sub(time.Unix(0, s.lastCommit.Load())); d > commitLimit {
		return fmt.Errorf("no bins written for %v", d.Round(time.Second))
	}
	return nil
}
