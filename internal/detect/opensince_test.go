package detect

import (
	"testing"
	"time"
)

// The pipeline holds clip audio from the moment a candidate opens, so an
// event longer than the audio buffer keeps its start.
func TestOpenSinceReportsCandidateStart(t *testing.T) {
	f := newFeed(t, nil)
	if _, open := f.d.OpenSince(); open {
		t.Fatal("open before any frame")
	}
	f.send(10, quiet)
	f.send(1, loud)
	if start, open := f.d.OpenSince(); !open || !start.Equal(t0.Add(time.Second)) {
		t.Fatalf("OpenSince = %v, %v; want %v, true", start, open, t0.Add(time.Second))
	}
	f.send(30, quiet) // a 100 ms blip, discarded
	if _, open := f.d.OpenSince(); open {
		t.Fatal("still open after the candidate closed")
	}
	f.wantEvents(0)
}
