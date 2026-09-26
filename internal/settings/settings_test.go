package settings

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
)

// base is a valid config file with nothing but the one required key, so the
// rest of the values are the defaults.
func base(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Parse(strings.NewReader("expected_capture_gain = 100\n"))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	return c
}

func ptr(s string) *string { return &s }

// sameConfig reports whether two configs hold the same values. RecordingPause
// is a slice, so Config cannot be compared with ==.
func sameConfig(a, b config.Config) bool { return reflect.DeepEqual(a, b) }

// The dashboard shows these keys, in this order. Capture, calibration, and
// path settings must not be here.
func TestEditableListsTheAgreedKeysInOrder(t *testing.T) {
	want := []struct {
		key  string
		typ  string
		unit string
		live bool
	}{
		{"threshold_db", "float", "dB", true},
		{"min_duration_ms", "int", "ms", true},
		{"hangover_ms", "int", "ms", true},
		{"cooldown_ms", "int", "ms", true},
		{"max_event_s", "int", "s", true},
		{"min_baseline_s", "int", "s", true},
		{"pre_roll_s", "int", "s", true},
		{"post_roll_s", "int", "s", true},
		{"baseline_window", "duration", "s", true},
		{"baseline_percentile", "float", "%", true},
		{"quiet_start", "time", "", true},
		{"quiet_end", "time", "", true},
		{"recording_pause", "text", "", true},
		{"batch_interval", "duration", "s", false},
		{"disk_warn_free_mb", "int", "MB", false},
		{"retention_days", "int", "days", true},
		{"camera_rtsp_path", "text", "", false},
		{"camera_rtsp_path_main", "text", "", false},
		{"video_ring_minutes", "int", "min", false},
		{"video_segment_seconds", "int", "s", false},
		{"camera_audio", "bool", "", false},
	}
	if len(Editable) != len(want) {
		t.Fatalf("Editable has %d fields, want %d", len(Editable), len(want))
	}
	for i, w := range want {
		got := Editable[i]
		if got.Key != w.key || got.Type != w.typ || got.Unit != w.unit || got.Live != w.live {
			t.Errorf("Editable[%d] = %+v, want key %q type %q unit %q live %v",
				i, got, w.key, w.typ, w.unit, w.live)
		}
		if got.Label == "" {
			t.Errorf("Editable[%d] (%s) has no label", i, got.Key)
		}
		// A range means nothing for a clock time, a text value, or a
		// yes-or-no. Every other type must carry one.
		if !ranged(got.Type) {
			continue
		}
		if got.Min >= got.Max {
			t.Errorf("Editable[%d] (%s) has min %v and max %v", i, got.Key, got.Min, got.Max)
		}
	}
}

func ranged(typ string) bool {
	switch typ {
	case "time", "text", "bool":
		return false
	}
	return true
}

// A text or a yes-or-no has no range, so the range check must let it past
// rather than read a missing number as zero and reject everything.
func TestApplyDoesNotRangeCheckATextOrBoolField(t *testing.T) {
	for _, f := range Editable {
		if ranged(f.Type) {
			continue
		}
		if f.Min != 0 || f.Max != 0 {
			t.Errorf("%s is type %s and should carry no range, but has min %v and max %v",
				f.Key, f.Type, f.Min, f.Max)
		}
	}
	l, err := New(base(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// camera_rtsp_path is text with no range. A range check that read the
	// missing number as zero would reject this.
	if err := l.Apply(map[string]*string{"camera_rtsp_path": ptr("Preview_01_sub")}); err != nil {
		t.Fatalf("Apply camera_rtsp_path: %v", err)
	}
	if got := l.Current().CameraRTSPPath; got != "Preview_01_sub" {
		t.Errorf("camera_rtsp_path = %q, want \"Preview_01_sub\"", got)
	}
}

// A field with no number to read out of the config has no range, whatever
// Min and Max hold. The check must skip it rather than read the missing
// number as zero and reject every value.
func TestCheckSkipsAFieldWithNoNumber(t *testing.T) {
	for _, typ := range []string{"time", "text", "bool"} {
		f := Field{Key: "camera_host", Type: typ, Min: 1, Max: 60}
		if err := f.check(base(t)); err != nil {
			t.Errorf("a %s field was range-checked: %v", typ, err)
		}
	}
}

// The dashboard must not be able to name the file the password is read
// from, or the directory the video is written to (SPEC.md section 3).
func TestCameraCredentialsFileAndVideoDirAreNotEditable(t *testing.T) {
	for _, key := range []string{"camera_credentials_file", "video_dir", "video_main_on_event"} {
		if _, ok := Lookup(key); ok {
			t.Errorf("%s is editable from the dashboard", key)
		}
	}
}

// The dashboard must not be able to send the camera login to a host of its
// choosing. The login goes to camera_host:camera_port, so a web form that
// could set them could collect the password (SPEC.md section 15 decision 26).
func TestCameraHostAndPortAreNotEditable(t *testing.T) {
	for key, value := range map[string]string{"camera_host": "attacker.example", "camera_port": "8554"} {
		if _, ok := Lookup(key); ok {
			t.Errorf("%s is editable from the dashboard", key)
		}
		file := base(t)
		file.CameraHost = "cam.local"
		l, err := New(file, nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		err = l.Apply(map[string]*string{key: ptr(value)})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("Apply %s = %q: err %v, want a refusal that names the key", key, value, err)
		}
		if got := l.Current(); got.CameraHost != "cam.local" || got.CameraPort != 554 {
			t.Errorf("after Apply %s the camera is %s:%d, want cam.local:554", key, got.CameraHost, got.CameraPort)
		}
	}
}

// A row the dashboard stored before camera_host and camera_port stopped
// being editable must not reach the collector. New refuses it, and
// DropFileOnly takes it out first so the other stored settings still apply.
func TestAStoredCameraHostOrPortIsDropped(t *testing.T) {
	file := base(t)
	file.CameraHost = "cam.local"
	for _, key := range []string{"camera_host", "camera_port"} {
		stored := map[string]string{key: "8554", "threshold_db": "20"}
		if key == "camera_host" {
			stored[key] = "attacker.example"
		}
		if _, err := New(file, stored); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("New with a stored %s: err %v, want a refusal that names it", key, err)
		}
		dropped := DropFileOnly(stored)
		if len(dropped) != 1 || dropped[0] != key {
			t.Errorf("DropFileOnly dropped %v, want [%s]", dropped, key)
		}
		if _, left := stored[key]; left || stored["threshold_db"] != "20" {
			t.Errorf("after DropFileOnly the rows are %v, want only threshold_db", stored)
		}
		l, err := New(file, stored)
		if err != nil {
			t.Fatalf("New after DropFileOnly: %v", err)
		}
		if got := l.Current(); got.CameraHost != "cam.local" || got.CameraPort != 554 || got.ThresholdDB != 20 {
			t.Errorf("settings = %s:%d threshold %v, want cam.local:554 threshold 20",
				got.CameraHost, got.CameraPort, got.ThresholdDB)
		}
	}
}

// A text setting is still checked. config.Validate is the only thing that
// decides what the RTSP paths accept, and Apply must run it.
func TestApplyValidatesATextSetting(t *testing.T) {
	bad := map[string]string{
		"camera_rtsp_path":      "admin:pass@cam/x",
		"camera_rtsp_path_main": "a b",
	}
	for key, value := range bad {
		l, err := New(base(t), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		before := l.Current()
		err = l.Apply(map[string]*string{key: ptr(value)})
		if err == nil {
			t.Errorf("Apply %s = %q was accepted", key, value)
		} else if !strings.Contains(err.Error(), key) {
			t.Errorf("error %v does not name %q", err, key)
		}
		if !sameConfig(l.Current(), before) {
			t.Errorf("Apply %s = %q changed the settings", key, value)
		}
	}
}

// Every editable key must be a key the config file itself knows.
func TestEditableKeysAreConfigKeys(t *testing.T) {
	samples := map[string]string{
		"threshold_db":          "15",
		"min_duration_ms":       "400",
		"hangover_ms":           "2000",
		"cooldown_ms":           "5000",
		"max_event_s":           "300",
		"min_baseline_s":        "30",
		"pre_roll_s":            "30",
		"post_roll_s":           "5",
		"baseline_window":       "10m",
		"baseline_percentile":   "10",
		"quiet_start":           "22:00",
		"quiet_end":             "07:00",
		"recording_pause":       "22:00-07:00",
		"batch_interval":        "15s",
		"disk_warn_free_mb":     "10240",
		"retention_days":        "90",
		"camera_rtsp_path":      "Preview_01_sub",
		"camera_rtsp_path_main": "Preview_01_main",
		"video_ring_minutes":    "10",
		"video_segment_seconds": "10",
		"camera_audio":          "true",
	}
	for _, f := range Editable {
		v, ok := samples[f.Key]
		if !ok {
			t.Errorf("%s has no sample value in this test", f.Key)
			continue
		}
		c := base(t)
		if err := c.Set(f.Key, v); err != nil {
			t.Errorf("config rejects editable key %s: %v", f.Key, err)
		}
	}
}

func TestLookup(t *testing.T) {
	f, ok := Lookup("threshold_db")
	if !ok {
		t.Fatal("Lookup(threshold_db) found nothing")
	}
	if f.Key != "threshold_db" || f.Unit != "dB" || !f.Live {
		t.Errorf("Lookup(threshold_db) = %+v", f)
	}
	if _, ok := Lookup("capture_device"); ok {
		t.Error("Lookup(capture_device) found a field; capture settings are not editable")
	}
	if _, ok := Lookup(""); ok {
		t.Error("Lookup(\"\") found a field")
	}
}

func TestNewMergesOverridesOntoFile(t *testing.T) {
	file := base(t)
	l, err := New(file, map[string]string{"threshold_db": "9", "quiet_start": "23:15"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := l.Current()
	if got.ThresholdDB != 9 {
		t.Errorf("ThresholdDB = %v, want 9", got.ThresholdDB)
	}
	if got.Quiet.Start != 23*time.Hour+15*time.Minute {
		t.Errorf("Quiet.Start = %v, want 23h15m0s", got.Quiet.Start)
	}
	if got.Hangover != 2*time.Second {
		t.Errorf("Hangover = %v, want the file value 2s", got.Hangover)
	}
	if l.File().ThresholdDB != 15 {
		t.Errorf("File().ThresholdDB = %v, want the file value 15", l.File().ThresholdDB)
	}
	if o := l.Overrides(); len(o) != 2 || o["threshold_db"] != "9" || o["quiet_start"] != "23:15" {
		t.Errorf("Overrides() = %v", o)
	}
}

func TestNewRejectsBadOverrides(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
		wantErr   string
	}{
		{"not a number", map[string]string{"threshold_db": "loud"}, "threshold_db"},
		{"unknown key", map[string]string{"treshold_db": "12"}, "treshold_db"},
		{"fails validate", map[string]string{"threshold_db": "-5"}, "threshold_db"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(base(t), tc.overrides)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("New error = %v, want an error naming %q", err, tc.wantErr)
			}
		})
	}
}

func TestApplyInstallsAndNotifies(t *testing.T) {
	l, err := New(base(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var seen []float64
	l.OnChange(func(c config.Config) { seen = append(seen, c.ThresholdDB) })

	if err := l.Apply(map[string]*string{"threshold_db": ptr("12")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if l.Current().ThresholdDB != 12 {
		t.Errorf("ThresholdDB = %v, want 12", l.Current().ThresholdDB)
	}
	if l.File().ThresholdDB != 15 {
		t.Errorf("File().ThresholdDB = %v, want 15; Apply must not change the file settings",
			l.File().ThresholdDB)
	}
	if len(seen) != 1 || seen[0] != 12 {
		t.Errorf("OnChange saw %v, want [12]", seen)
	}

	// A second Apply keeps the first override and adds the second.
	if err := l.Apply(map[string]*string{"hangover_ms": ptr("1500")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := l.Current()
	if got.ThresholdDB != 12 || got.Hangover != 1500*time.Millisecond {
		t.Errorf("after the second Apply: threshold %v hangover %v, want 12 and 1.5s",
			got.ThresholdDB, got.Hangover)
	}
	if len(seen) != 2 {
		t.Errorf("OnChange was called %d times, want 2", len(seen))
	}
}

func TestApplyNilRemovesTheOverride(t *testing.T) {
	l, err := New(base(t), map[string]string{"threshold_db": "9"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Apply(map[string]*string{"threshold_db": nil}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if l.Current().ThresholdDB != 15 {
		t.Errorf("ThresholdDB = %v, want the file value 15 back", l.Current().ThresholdDB)
	}
	if o := l.Overrides(); len(o) != 0 {
		t.Errorf("Overrides() = %v, want none", o)
	}
}

// The range comes from the Field. Both ends count as inside it.
func TestApplyChecksTheFieldRange(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		value  string
		accept bool
	}{
		{"at the minimum", "threshold_db", "1", true},
		{"at the maximum", "threshold_db", "60", true},
		{"below the minimum", "threshold_db", "0.5", false},
		{"above the maximum", "threshold_db", "61", false},
		{"pre-roll at the maximum", "pre_roll_s", "60", true},
		{"pre-roll above the maximum", "pre_roll_s", "61", false},
		{"window at the minimum", "baseline_window", "10s", true},
		{"window below the minimum", "baseline_window", "9s", false},
		{"batch interval at the maximum", "batch_interval", "30s", true},
		{"batch interval above the maximum", "batch_interval", "31s", false},
		// A floor of a week: one careless keystroke must not wipe the
		// recordings. Keeping them forever is a config file decision.
		{"retention at the minimum", "retention_days", "7", true},
		{"retention below the minimum", "retention_days", "6", false},
		{"retention off", "retention_days", "0", false},
		{"retention at the maximum", "retention_days", "3650", true},
		{"retention above the maximum", "retention_days", "3651", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, err := New(base(t), nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = l.Apply(map[string]*string{tc.key: ptr(tc.value)})
			if tc.accept && err != nil {
				t.Fatalf("Apply %s = %s: %v", tc.key, tc.value, err)
			}
			if !tc.accept {
				if err == nil {
					t.Fatalf("Apply %s = %s was accepted", tc.key, tc.value)
				}
				if !strings.Contains(err.Error(), tc.key) {
					t.Errorf("error %v does not name %q", err, tc.key)
				}
			}
		})
	}
}

func TestApplyRejectsKeysThatAreNotEditable(t *testing.T) {
	// clip_lowpass_hz is on this list on purpose. Changing what a recording
	// may hold, from a phone at 2am with no record of why, is exactly what
	// the editable list excludes (SPEC.md section 15 decision 18).
	for _, key := range []string{"capture_device", "sensitivity_dbfs", "db_path", "clip_dir",
		"calibration_file", "auth_mode", "clip_lowpass_hz", "treshold_db"} {
		l, err := New(base(t), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		before := l.Current()
		err = l.Apply(map[string]*string{key: ptr("x")})
		if err == nil {
			t.Errorf("Apply %s was accepted", key)
		} else if !strings.Contains(err.Error(), key) {
			t.Errorf("error %v does not name %q", err, key)
		}
		if !sameConfig(l.Current(), before) {
			t.Errorf("Apply %s changed the settings", key)
		}
	}
}

// Both checks must be able to reject: the field range, and config.Validate on
// the merged settings.
func TestApplyRunsConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		changes map[string]*string
		wantErr string
	}{
		{"event shorter than the shortest noise",
			map[string]*string{"min_duration_ms": ptr("5000"), "max_event_s": ptr("1")},
			"max_event_s"},
		{"quiet hours of zero length",
			map[string]*string{"quiet_start": ptr("22:00"), "quiet_end": ptr("22:00")},
			"quiet_end"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, err := New(base(t), nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			before := l.Current()
			err = l.Apply(tc.changes)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Apply error = %v, want an error containing %q", err, tc.wantErr)
			}
			if !sameConfig(l.Current(), before) {
				t.Errorf("a rejected Apply changed the settings to %+v", l.Current())
			}
		})
	}
}

// One bad value must reject the whole request. Nothing may be half applied.
func TestApplyIsAllOrNothing(t *testing.T) {
	l, err := New(base(t), map[string]string{"cooldown_ms": "4000"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before := l.Current()
	var calls int
	l.OnChange(func(config.Config) { calls++ })

	err = l.Apply(map[string]*string{
		"threshold_db":    ptr("12"),
		"hangover_ms":     ptr("1500"),
		"min_duration_ms": ptr("nonsense"),
		"cooldown_ms":     nil,
	})
	if err == nil {
		t.Fatal("Apply with one bad value was accepted")
	}
	if !strings.Contains(err.Error(), "min_duration_ms") {
		t.Errorf("error %v does not name min_duration_ms", err)
	}
	if !sameConfig(l.Current(), before) {
		t.Errorf("Current() = %+v, want it unchanged at %+v", l.Current(), before)
	}
	if o := l.Overrides(); len(o) != 1 || o["cooldown_ms"] != "4000" {
		t.Errorf("Overrides() = %v, want only cooldown_ms = 4000", o)
	}
	if calls != 0 {
		t.Errorf("OnChange was called %d times after a rejected Apply", calls)
	}
}

// The caller must not be able to change the settings behind Live's back.
func TestOverridesReturnsACopy(t *testing.T) {
	l, err := New(base(t), map[string]string{"threshold_db": "9"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	o := l.Overrides()
	o["threshold_db"] = "40"
	o["hangover_ms"] = "1"
	if again := l.Overrides(); len(again) != 1 || again["threshold_db"] != "9" {
		t.Errorf("Overrides() = %v, want only threshold_db = 9", again)
	}
	if l.Current().ThresholdDB != 9 {
		t.Errorf("ThresholdDB = %v, want 9", l.Current().ThresholdDB)
	}
}

// New must not keep the caller's map either.
func TestNewCopiesTheOverrides(t *testing.T) {
	given := map[string]string{"threshold_db": "9"}
	l, err := New(base(t), given)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	given["threshold_db"] = "40"
	if o := l.Overrides(); o["threshold_db"] != "9" {
		t.Errorf("Overrides() = %v, want threshold_db = 9", o)
	}
}

// The collector reads the settings while the dashboard writes them.
func TestConcurrentUse(t *testing.T) {
	l, err := New(base(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = l.Current()
				_ = l.Overrides()
				_ = l.File()
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if err := l.Apply(map[string]*string{"threshold_db": ptr("12")}); err != nil {
					t.Errorf("Apply: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// camera_audio is editable, unlike clip_lowpass_hz: it is neither a
// credential nor a path. But it decides whether a recording can hold speech,
// so the field has to carry the sentence that says so, and the collector has
// to be restarted before it takes effect (SPEC.md section 15 decision 22).
func TestCameraAudioCarriesItsWarningAndNeedsARestart(t *testing.T) {
	f, ok := Lookup("camera_audio")
	if !ok {
		t.Fatal("camera_audio is not editable, so the dashboard cannot change it")
	}
	if f.Live {
		t.Error("camera_audio is marked live, but the ring is built once at start")
	}
	if f.Note == "" {
		t.Fatal("camera_audio has no note, so an interface could show it as a bare checkbox")
	}
	for _, want := range []string{"speech", "microphone"} {
		if !strings.Contains(f.Note, want) {
			t.Errorf("the note %q does not mention %q", f.Note, want)
		}
	}

	l, err := New(base(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if l.Current().CameraAudio {
		t.Fatal("camera_audio is on before anything set it")
	}
	if err := l.Apply(map[string]*string{"camera_audio": ptr("true")}); err != nil {
		t.Fatalf("Apply camera_audio: %v", err)
	}
	if !l.Current().CameraAudio {
		t.Error("camera_audio is still off after it was applied")
	}
	// Only true and false. "yes" read as off would be a privacy setting
	// changing in silence.
	if err := l.Apply(map[string]*string{"camera_audio": ptr("yes")}); err == nil {
		t.Error("camera_audio = yes was accepted")
	}
}

// retention_days decides when the recording of an event is deleted. Its
// note has to say what is kept and what is lost: the recording goes and
// the measurement stays. The retention job reads the setting in force at
// each run, so a change applies without a restart.
func TestRetentionDaysSaysWhatIsKeptAndIsLive(t *testing.T) {
	f, ok := Lookup("retention_days")
	if !ok {
		t.Fatal("retention_days is not editable, so the dashboard cannot change it")
	}
	if !f.Live {
		t.Error("retention_days is marked as needing a restart, but the job reads it at each run")
	}
	if f.Note == "" {
		t.Fatal("retention_days has no note, so an interface could show it as a bare number")
	}
	for _, want := range []string{"deleted", "stay"} {
		if !strings.Contains(f.Note, want) {
			t.Errorf("the note %q does not say %q", f.Note, want)
		}
	}

	l, err := New(base(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Apply(map[string]*string{"retention_days": ptr("30")}); err != nil {
		t.Fatalf("Apply retention_days: %v", err)
	}
	if got := l.Current().RetentionDays; got != 30 {
		t.Errorf("retention_days = %d after it was applied, want 30", got)
	}
}
