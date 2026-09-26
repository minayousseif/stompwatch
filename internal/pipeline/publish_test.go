package pipeline

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/audio"
	"github.com/minayousseif/stompwatch/internal/clip"
	"github.com/minayousseif/stompwatch/internal/dsp"
	"github.com/minayousseif/stompwatch/internal/health"
	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/store"
)

func openRead(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?mode=ro")
}

// published collects the bins the live feed was given.
type published struct {
	mu   sync.Mutex
	bins []meter.Bin
}

func (p *published) add(b meter.Bin) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bins = append(p.bins, b)
}

func (p *published) all() []meter.Bin {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]meter.Bin(nil), p.bins...)
}

// The dashboard's live meter must show the same measurement the database
// keeps. Every bin that reaches samples_1s also reaches Publish, with the
// same second and the same level.
func TestPublishSeesTheSameBinsAsTheStore(t *testing.T) {
	dir := t.TempDir()
	settings := testSettings(dir)
	st, err := store.Open(settings.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec, err := clip.NewRecorder(settings.ClipDir, 30*time.Second, dsp.DefaultClipLowpassHz)
	if err != nil {
		t.Fatal(err)
	}
	chunks := make(chan audio.Chunk, 8)
	var live published

	p, err := New(Config{
		Settings:    settings,
		Store:       st,
		Recorder:    rec,
		Calibration: meter.NoCalibration{},
		Chunks:      chunks,
		Status:      health.NewStatus(t0),
		Log:         quietLog(),
		Now:         func() time.Time { return t0 },
		Publish:     live.add,
	})
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for i := range 3 {
			chunks <- silentChunk(t0.Add(time.Duration(i)*time.Second), int64(i)*48000, 48000)
		}
		close(chunks)
	}()
	if err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := live.all()
	if len(got) != 3 {
		t.Fatalf("Publish received %d bins, want 3", len(got))
	}

	db, err := openRead(settings.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT ts, laeq, lamax, baseline FROM samples_1s ORDER BY ts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var ts int64
		var laeq, lamax, baseline float64
		if err := rows.Scan(&ts, &laeq, &lamax, &baseline); err != nil {
			t.Fatal(err)
		}
		if i >= len(got) {
			t.Fatalf("the database holds more seconds than Publish saw: row %d at ts %d", i, ts)
		}
		b := got[i]
		if b.Start.Unix() != ts || b.LAeq != laeq || b.LAmax != lamax || b.Baseline != baseline {
			t.Errorf("bin %d: Publish saw start %d laeq %v lamax %v baseline %v; the database holds %d, %v, %v, %v",
				i, b.Start.Unix(), b.LAeq, b.LAmax, b.Baseline, ts, laeq, lamax, baseline)
		}
		i++
	}
	if i != len(got) {
		t.Errorf("Publish saw %d bins but the database holds %d", len(got), i)
	}
	// Seconds must not repeat, or the live meter would show one twice.
	if got[0].Start.Equal(got[1].Start) || got[1].Start.Equal(got[2].Start) {
		t.Errorf("Publish saw the same second twice: %v", []time.Time{got[0].Start, got[1].Start, got[2].Start})
	}
}

// The live meter must not stop when the database queue is full. Publish runs
// before the bin is queued, so a blocked writer loses the row but not the
// reading on the screen.
func TestPublishStillRunsWhenTheBinQueueIsFull(t *testing.T) {
	dir := t.TempDir()
	settings := testSettings(dir)
	st, err := store.Open(settings.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec, err := clip.NewRecorder(settings.ClipDir, 30*time.Second, dsp.DefaultClipLowpassHz)
	if err != nil {
		t.Fatal(err)
	}
	chunks := make(chan audio.Chunk, 8)
	var live published

	p, err := New(Config{
		Settings:    settings,
		Store:       st,
		Recorder:    rec,
		Calibration: meter.NoCalibration{},
		Chunks:      chunks,
		Status:      health.NewStatus(t0),
		Log:         quietLog(),
		Now:         func() time.Time { return t0 },
		Publish:     live.add,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Room for one bin and nothing reading it, so the rest are dropped.
	p.bins = make(chan meter.Bin, 1)

	for i := range 3 {
		chunks <- silentChunk(t0.Add(time.Duration(i)*time.Second), int64(i)*48000, 48000)
	}
	close(chunks)
	if err := p.dspLoop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := len(live.all()); got != 3 {
		t.Errorf("Publish received %d bins with a full queue, want 3", got)
	}
	if got := p.Stats().BinsDropped; got != 2 {
		t.Errorf("BinsDropped = %d, want 2; the test did not fill the queue as it meant to", got)
	}
}

// A nil Publish is the normal case for a collector with no dashboard.
func TestNilPublishDoesNothing(t *testing.T) {
	e := newEnv(t)
	go func() {
		e.chunks <- silentChunk(t0, 0, 48000)
		close(e.chunks)
	}()
	if err := e.pipe.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}
