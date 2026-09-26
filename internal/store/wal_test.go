package store

import (
	"testing"

	"github.com/minayousseif/stompwatch/internal/meter"
)

// SPEC.md section 6.9.1: watch the -wal file and truncate it when idle.
func TestCheckpointTruncatesWAL(t *testing.T) {
	s, _ := openTemp(t)
	batch := make([]meter.Bin, 600)
	for i := range batch {
		batch[i] = bin(i, 60)
	}
	if _, err := s.InsertBins(ctx, batch); err != nil {
		t.Fatal(err)
	}
	before, err := s.WALSize()
	if err != nil || before == 0 {
		t.Fatalf("WAL size after 600 inserts = %d, %v; want above 0", before, err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	after, err := s.WALSize()
	if err != nil || after != 0 {
		t.Fatalf("WAL size after checkpoint = %d, %v; want 0", after, err)
	}
}
