package detect

import (
	"testing"
	"time"
)

// setConfig installs new settings on the running detector, keeping the feed's
// own OnEvent so the test still sees the events.
func (f *feed) setConfig(change func(*Config)) {
	f.t.Helper()
	cfg := DefaultConfig()
	cfg.OnEvent = func(e Event) { f.events = append(f.events, e) }
	change(&cfg)
	if err := f.d.SetConfig(cfg); err != nil {
		f.t.Fatalf("SetConfig: %v", err)
	}
}

// A settings change must not lose an event that is open at the time. The
// candidate keeps its start and the loud time it has already added up; the new
// threshold applies to the frames that follow.
func TestSetConfigKeepsTheOpenCandidate(t *testing.T) {
	f := newFeed(t, func(c *Config) { c.MinDuration = 600 * time.Millisecond })

	// 400 ms above the old threshold of 15 dB. On its own that is too short.
	f.send(4, 20)
	if _, open := f.d.OpenSince(); !open {
		t.Fatal("no candidate is open after the loud frames")
	}

	f.setConfig(func(c *Config) {
		c.MinDuration = 600 * time.Millisecond
		c.ThresholdDB = 12
	})
	// 300 ms that only the new threshold counts as loud. 400 + 300 = 700 ms.
	f.send(3, 13)
	f.send(30, quiet)

	f.wantEvents(1)
	e := f.events[0]
	if !e.Start.Equal(t0) {
		t.Errorf("event starts at %v, want %v: the change lost the open candidate", e.Start, t0)
	}
	if want := t0.Add(700 * time.Millisecond); !e.End.Equal(want) {
		t.Errorf("event ends at %v, want %v", e.End, want)
	}
}

// A candidate that has not reached the minimum duration is still dropped after
// the change, so a change cannot invent an event.
func TestSetConfigDoesNotKeepAShortCandidate(t *testing.T) {
	f := newFeed(t, func(c *Config) { c.MinDuration = 600 * time.Millisecond })
	f.send(4, 20) // 400 ms
	f.setConfig(func(c *Config) { c.MinDuration = 600 * time.Millisecond })
	f.send(30, quiet)
	f.wantEvents(0)
}

// Bad settings are refused and change nothing, so a value typed into a web
// form cannot stop the detector from working.
func TestSetConfigRefusesBadSettings(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"no OnEvent":          func(c *Config) { c.OnEvent = nil },
		"threshold 0":         func(c *Config) { c.ThresholdDB = 0 },
		"negative hangover":   func(c *Config) { c.Hangover = -time.Second },
		"max event too short": func(c *Config) { c.MaxEvent = time.Millisecond },
		"no baseline":         func(c *Config) { c.MinBaselineBins = 0 },
	} {
		f := newFeed(t, nil)
		cfg := DefaultConfig()
		cfg.OnEvent = func(Event) {}
		change(&cfg)
		if err := f.d.SetConfig(cfg); err == nil {
			t.Errorf("%s: SetConfig returned no error", name)
			continue
		}
		// The settings from New still apply: 15 dB is loud, 14.9 dB is not,
		// and the feed's own OnEvent still gets the event.
		f.send(10, 14.9)
		f.send(30, quiet)
		f.wantEvents(0)
		f.send(10, 15.0)
		f.send(30, quiet)
		f.wantEvents(1)
	}
}
