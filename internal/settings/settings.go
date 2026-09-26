package settings

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/minayousseif/stompwatch/internal/clip"
	"github.com/minayousseif/stompwatch/internal/config"
)

// Field describes one setting the dashboard may edit.
type Field struct {
	Key string
	// Type is "float", "int", "duration", "time" (HH:MM), "text", or "bool".
	Type  string
	Label string  // plain English, sentence case, as the owner would say it
	Unit  string  // "dB", "ms", "s", "" - for display
	Min   float64 // ignored for Type "time", "text", and "bool"
	Max   float64
	Live  bool // true when the running process applies it without a restart
	// Note is one sentence the interface must show with the field, or empty
	// when there is nothing to say beyond the label and the range. It is
	// here, and not in the web code, because it carries what a range cannot:
	// camera_audio changes what a recording may hold, and that warning must
	// not be something an interface can forget to draw.
	Note string

	// num reads the setting back out of a parsed Config, counted in Unit, so
	// the range check sees the value the config package read and not a second
	// reading of the same text. It is nil for the types that have no range:
	// "time", "text", and "bool". A nil num means no range check, never a
	// value of zero.
	num func(config.Config) float64
}

// in counts d in unit, which is the unit the dashboard shows.
func in(d, unit time.Duration) float64 { return float64(d) / float64(unit) }

// Editable lists every setting the dashboard may change, in display order.
// Capture, calibration, and path settings are absent on purpose: changing
// them from a phone at 2am would invalidate the measurements with no record
// of why.
//
// sensitivity_dbfs, sensitivity_source, sensitivity_uncertainty_db,
// sensitivity_measured_on and sensitivity_reference are absent for a second
// reason as well. They do not tune the instrument; they describe how it was
// calibrated, and every level it has ever reported rests on them. A web form
// that could set sensitivity_source to measured, with a date and a reference
// of its own choosing, could claim a calibration that never happened. They
// live in the config file only, where setting them takes the keyboard on the
// box and a restart (SPEC.md section 15 decision 23).
//
// A range here is narrower than config.Validate allows. It is the range in
// which the instrument still measures something useful, not the range in
// which the program still runs.
var Editable = []Field{{
	Key: "threshold_db", Type: "float", Live: true,
	Label: "Trigger level above baseline", Unit: "dB", Min: 1, Max: 60,
	num: func(c config.Config) float64 { return c.ThresholdDB },
}, {
	Key: "min_duration_ms", Type: "int", Live: true,
	Label: "Shortest noise that counts", Unit: "ms", Min: 0, Max: 10000,
	num: func(c config.Config) float64 { return in(c.MinDuration, time.Millisecond) },
}, {
	Key: "hangover_ms", Type: "int", Live: true,
	Label: "Quiet gap that ends an event", Unit: "ms", Min: 0, Max: 30000,
	num: func(c config.Config) float64 { return in(c.Hangover, time.Millisecond) },
}, {
	Key: "cooldown_ms", Type: "int", Live: true,
	Label: "Wait before the next event can start", Unit: "ms", Min: 0, Max: 60000,
	num: func(c config.Config) float64 { return in(c.Cooldown, time.Millisecond) },
}, {
	Key: "max_event_s", Type: "int", Live: true,
	Label: "Longest event", Unit: "s", Min: 1, Max: 3600,
	num: func(c config.Config) float64 { return in(c.MaxEvent, time.Second) },
}, {
	Key: "min_baseline_s", Type: "int", Live: true,
	Label: "Listening time before detection starts", Unit: "s", Min: 1, Max: 3600,
	num: func(c config.Config) float64 { return in(c.MinBaseline, time.Second) },
}, {
	// The audio buffer holds 30 s, so more than 25 s of pre-roll has nothing
	// to come from.
	Key: "pre_roll_s", Type: "int", Live: true,
	Label: "Audio kept from before the event", Unit: "s", Min: 0, Max: clip.MaxPreRoll.Seconds(),
	num: func(c config.Config) float64 { return in(c.PreRoll, time.Second) },
}, {
	Key: "post_roll_s", Type: "int", Live: true,
	Label: "Audio kept after the event", Unit: "s", Min: 0, Max: 60,
	num: func(c config.Config) float64 { return in(c.PostRoll, time.Second) },
}, {
	Key: "baseline_window", Type: "duration", Live: true,
	Label: "Window the baseline is measured over", Unit: "s", Min: 10, Max: 3600,
	num: func(c config.Config) float64 { return in(c.BaselineWindow, time.Second) },
}, {
	// The baseline is the quiet part of the window. A percentile above the
	// middle would make it the noise instead.
	Key: "baseline_percentile", Type: "float", Live: true,
	Label: "Percentile of the window that is the baseline", Unit: "%", Min: 1, Max: 50,
	num: func(c config.Config) float64 { return c.BaselinePercentile },
}, {
	Key: "quiet_start", Type: "time", Live: true,
	Label: "Quiet hours start",
}, {
	Key: "quiet_end", Type: "time", Live: true,
	Label: "Quiet hours end",
}, {
	Key: "recording_pause", Type: "text", Live: true,
	Label: "Daily recording pause",
	Note: "Written as 22:00-07:00, or as several windows separated by commas, such as 12:00-13:00, " +
		"21:00-23:00. Leave it empty for no pause. No two windows may overlap or touch. Inside a window " +
		"no event opens and no audio clip or video is recorded. The level of every second is still " +
		"measured, so the record has no gap.",
}, {
	Key: "batch_interval", Type: "duration", Live: false,
	Label: "How often measurements reach the disk", Unit: "s", Min: 10, Max: 30,
	num: func(c config.Config) float64 { return in(c.BatchInterval, time.Second) },
}, {
	Key: "disk_warn_free_mb", Type: "int", Live: false,
	Label: "Free space that raises a warning", Unit: "MB", Min: 100, Max: 1000000,
	num: func(c config.Config) float64 { return float64(c.DiskWarnFreeMB) },
}, {
	// retention_days is an operating choice, not a calibration claim, so
	// it is editable. The floor of a week is here and not in
	// config.Validate: one careless keystroke on a phone must not wipe the
	// recordings, but keeping them forever (0) is a decision the config
	// file may state. It is live because the retention job reads the
	// setting in force at each run.
	Key: "retention_days", Type: "int", Live: true,
	Label: "Days a recording is kept", Unit: "days", Min: 7, Max: 3650,
	Note: "After this many days the audio and video files of an event are deleted. " +
		"The event, its levels, its class and its review stay.",
	num: func(c config.Config) float64 { return float64(c.RetentionDays) },
}, {
	// The camera keys. All five need a restart, because the ring and the
	// clip writer are built once at start.
	//
	// camera_credentials_file and video_dir are missing on purpose. One
	// names the file the password is read from and the other the directory
	// the video is written to, and a web form must not be able to point the
	// server at any file it likes to read or any directory it likes to
	// write (SPEC.md section 3). camera_host and camera_port are missing for
	// the same reason: the login goes to them, and a web form must not be
	// able to send the password to any host it likes (SPEC.md section 15
	// decision 26). video_main_on_event is missing because it is not
	// implemented.
	Key: "camera_rtsp_path", Type: "text", Live: false,
	Label: "Sub-stream path",
}, {
	Key: "camera_rtsp_path_main", Type: "text", Live: false,
	Label: "Main-stream path",
}, {
	Key: "video_ring_minutes", Type: "int", Live: false,
	Label: "Video kept in the ring", Unit: "min", Min: 1, Max: 24 * 60,
	num: func(c config.Config) float64 { return float64(c.VideoRingMinutes) },
}, {
	Key: "video_segment_seconds", Type: "int", Live: false,
	Label: "Length of one segment", Unit: "s", Min: 1, Max: 60,
	num: func(c config.Config) float64 { return float64(c.VideoSegmentSeconds) },
}, {
	// camera_audio is editable, unlike clip_lowpass_hz in decision 18: it is
	// not a credential and not a path, so the reasons those are excluded do
	// not apply. What it decides is still what a recording may hold, so the
	// note says so in words and the interface has to show it
	// (SPEC.md section 15 decision 22).
	Key: "camera_audio", Type: "bool", Live: false,
	Label: "Record the camera's own audio",
	Note: "The camera will record speech in clear: its microphone is unfiltered. " +
		"The measuring microphone's own clips are not affected.",
}}

// fileOnly are the keys that only the config file may set. The dashboard
// could once store them, so a row can still be in the config table, and
// such a row must not reach the collector: the camera login goes to
// camera_host:camera_port.
var fileOnly = []string{"camera_host", "camera_port"}

// DropFileOnly removes from stored every key that only the config file may
// set, and returns the keys it removed, in order. The caller reports them.
func DropFileOnly(stored map[string]string) []string {
	var dropped []string
	for _, k := range fileOnly {
		if _, ok := stored[k]; ok {
			delete(stored, k)
			dropped = append(dropped, k)
		}
	}
	return dropped
}

var byKey = func() map[string]Field {
	m := make(map[string]Field, len(Editable))
	for _, f := range Editable {
		m[f.Key] = f
	}
	return m
}()

// Lookup returns the field for a key.
func Lookup(key string) (Field, bool) {
	f, ok := byKey[key]
	return f, ok
}

// check reports whether the setting c holds for this field is in range. Both
// ends of the range count as inside it.
func (f Field) check(c config.Config) error {
	if f.num == nil {
		return nil
	}
	v := f.num(c)
	if v < f.Min || v > f.Max {
		return fmt.Errorf("settings: %s must be between %s and %s%s, not %s",
			f.Key, number(f.Min), number(f.Max), unit(f.Unit), number(v))
	}
	return nil
}

func number(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func unit(u string) string {
	if u == "" {
		return ""
	}
	return " " + u
}

// Live holds the settings the collector is running with. Its methods are
// safe for concurrent use.
type Live struct {
	apply sync.Mutex // one Apply at a time, so OnChange sees changes in order

	mu        sync.RWMutex
	file      config.Config
	overrides map[string]string
	current   config.Config
	onChange  []func(config.Config)
}

// New merges the config-table overrides onto the file settings and returns
// the holder. It returns an error naming the key if an override does not
// parse or the merged settings do not validate.
//
// It takes any key the config file knows, not only the editable ones, so a
// row written by hand still reaches the process. Apply is the stricter door.
// The one exception is a key that only the config file may set: New refuses
// it, and the caller takes it out first with DropFileOnly.
func New(file config.Config, overrides map[string]string) (*Live, error) {
	for _, k := range fileOnly {
		if _, ok := overrides[k]; ok {
			return nil, fmt.Errorf("settings: %s is set in the config file only, not in the database", k)
		}
	}
	own := make(map[string]string, len(overrides))
	maps.Copy(own, overrides)
	merged, err := merge(file, own)
	if err != nil {
		return nil, err
	}
	return &Live{file: file, overrides: own, current: merged}, nil
}

// merge applies the overrides to the file settings and checks the result.
// The keys go in sorted order so that a file with two faults always names
// the same one.
func merge(file config.Config, overrides map[string]string) (config.Config, error) {
	c := file
	for _, k := range slices.Sorted(maps.Keys(overrides)) {
		if err := c.Set(k, overrides[k]); err != nil {
			return config.Config{}, err
		}
	}
	if err := c.Validate(); err != nil {
		return config.Config{}, err
	}
	return c, nil
}

// Current returns the settings in force now.
func (l *Live) Current() config.Config {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.current
}

// File returns the settings from the config file, without overrides.
func (l *Live) File() config.Config {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.file
}

// Overrides returns the config-table values in force, as strings.
func (l *Live) Overrides() map[string]string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return maps.Clone(l.overrides)
}

// Apply merges changes onto the current overrides and installs the result.
// A nil value removes an override so the file default applies again. It
// changes nothing and returns an error naming the key if any value does not
// parse, is out of the field's range, is not an editable key, or the merged
// settings fail config.Validate.
//
// Apply does not write to the database. The caller writes the rows with
// store.PutSettings after Apply returns nil, so a rejected change never
// reaches the disk.
func (l *Live) Apply(changes map[string]*string) error {
	l.apply.Lock()
	defer l.apply.Unlock()

	l.mu.RLock()
	file, next := l.file, maps.Clone(l.overrides)
	l.mu.RUnlock()

	keys := slices.Sorted(maps.Keys(changes))
	for _, k := range keys {
		if _, ok := Lookup(k); !ok {
			return fmt.Errorf("settings: %s cannot be changed from the dashboard", k)
		}
		if v := changes[k]; v == nil {
			delete(next, k)
		} else {
			next[k] = *v
		}
	}

	merged, err := merge(file, next)
	if err != nil {
		return err
	}
	// Check the range of every value the request set. A removed override
	// gives back a file value, which the owner chose at the keyboard.
	for _, k := range keys {
		if changes[k] == nil {
			continue
		}
		f, _ := Lookup(k)
		if err := f.check(merged); err != nil {
			return err
		}
	}

	l.mu.Lock()
	l.overrides, l.current = next, merged
	fns := slices.Clone(l.onChange)
	l.mu.Unlock()

	// Outside the lock: a handler is free to call Current.
	for _, fn := range fns {
		fn(merged)
	}
	return nil
}

// OnChange registers a function called after every successful Apply, with
// the new settings. It is called on the caller's goroutine.
func (l *Live) OnChange(fn func(config.Config)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onChange = append(l.onChange, fn)
}
