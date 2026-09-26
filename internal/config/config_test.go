package config

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const minimal = "expected_capture_gain = 100\n"

// SPEC.md section 6: the defaults a file with only the required key must give.
func TestParseMinimalFileGivesSpecDefaults(t *testing.T) {
	c, err := Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"capture_device", c.CaptureDevice, "hw:EM01,0"},
		{"capture_channel", c.CaptureChannel, 0},
		{"sensitivity_dbfs", c.SensitivityDBFS, -13.0},
		{"baseline_window", c.BaselineWindow, 10 * time.Minute},
		{"baseline_percentile", c.BaselinePercentile, 10.0},
		{"threshold_db", c.ThresholdDB, 15.0},
		{"min_duration_ms", c.MinDuration, 400 * time.Millisecond},
		{"hangover_ms", c.Hangover, 2 * time.Second},
		{"cooldown_ms", c.Cooldown, 5 * time.Second},
		{"max_event_s", c.MaxEvent, 300 * time.Second},
		{"pre_roll_s", c.PreRoll, 30 * time.Second},
		{"post_roll_s", c.PostRoll, 30 * time.Second},
		{"heartbeat_url", c.HeartbeatURL, ""},
		{"heartbeat_interval", c.HeartbeatInterval, 5 * time.Minute},
		{"heartbeat_stall_factor", c.HeartbeatStallFactor, 2.0},
		{"stuck_limit", c.StuckLimit, 5 * time.Second},
		{"db_path", c.DBPath, "/data/noise.db"},
		{"clip_dir", c.ClipDir, "/data/clips/audio"},
		{"expected_capture_gain", c.ExpectedCaptureGain, "100"},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.name, ch.got, ch.want)
		}
	}
}

func TestParseReadsValuesCommentsAndBlankLines(t *testing.T) {
	in := `# capture
capture_device = hw:1,0
   capture_channel=1

expected_capture_gain = none
threshold_db = 12.5
hangover_ms = 1500
heartbeat_url = https://hc-ping.com/abc-123?x=1
heartbeat_interval = 2m
`
	c, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.CaptureDevice != "hw:1,0" || c.CaptureChannel != 1 || c.ExpectedCaptureGain != "none" ||
		c.ThresholdDB != 12.5 || c.Hangover != 1500*time.Millisecond ||
		c.HeartbeatURL != "https://hc-ping.com/abc-123?x=1" || c.HeartbeatInterval != 2*time.Minute {
		t.Fatalf("parsed %+v", c)
	}
}

// A typo in a key must not silently fall back to the default.
func TestParseRejectsMistakes(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"unknown key", minimal + "treshold_db = 10\n", "line 2"},
		{"bad number", minimal + "threshold_db = loud\n", "threshold_db"},
		{"bad duration", minimal + "heartbeat_interval = soon\n", "heartbeat_interval"},
		{"no equals sign", minimal + "threshold_db 10\n", "line 2"},
		{"duplicate key", minimal + "threshold_db = 10\nthreshold_db = 11\n", "line 3"},
		{"missing expected gain", "threshold_db = 10\n", "expected_capture_gain"},
		{"default device", minimal + "capture_device = default\n", "hw:"},
		{"channel 2", minimal + "capture_channel = 2\n", "capture_channel"},
		{"positive sensitivity", minimal + "sensitivity_dbfs = 3\n", "sensitivity_dbfs"},
		{"percentile 0", minimal + "baseline_percentile = 0\n", "baseline_percentile"},
		{"percentile 101", minimal + "baseline_percentile = 101\n", "baseline_percentile"},
		{"threshold 0", minimal + "threshold_db = 0\n", "threshold_db"},
		{"pre-roll longer than buffer", minimal + "pre_roll_s = 61\n", "pre_roll_s"},
		{"batch interval 5 s", minimal + "batch_interval = 5s\n", "batch_interval"},
		{"batch interval 60 s", minimal + "batch_interval = 60s\n", "batch_interval"},
		{"stall factor below 1", minimal + "heartbeat_stall_factor = 0.5\n", "heartbeat_stall_factor"},
		{"warn below floor", minimal + "disk_min_free_mb = 5000\ndisk_warn_free_mb = 1000\n", "disk_warn_free_mb"},
		{"max event below min duration", minimal + "max_event_s = 0\n", "max_event_s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Parse error = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// Set is how the database overrides reach a Config, so it must read a value
// exactly as a config file line does.
func TestSetAppliesOneKey(t *testing.T) {
	c := Default()
	if err := c.Set("threshold_db", "12.5"); err != nil {
		t.Fatalf("Set threshold_db: %v", err)
	}
	if err := c.Set("quiet_start", "22:30"); err != nil {
		t.Fatalf("Set quiet_start: %v", err)
	}
	if err := c.Set("min_duration_ms", "750"); err != nil {
		t.Fatalf("Set min_duration_ms: %v", err)
	}
	if c.ThresholdDB != 12.5 {
		t.Errorf("ThresholdDB = %v, want 12.5", c.ThresholdDB)
	}
	if c.Quiet.Start != 22*time.Hour+30*time.Minute {
		t.Errorf("Quiet.Start = %v, want 22h30m0s", c.Quiet.Start)
	}
	if c.MinDuration != 750*time.Millisecond {
		t.Errorf("MinDuration = %v, want 750ms", c.MinDuration)
	}
}

// A typo must be an error here too, or a database row could silently do
// nothing while the dashboard reports that it was applied.
func TestSetRejectsMistakesAndChangesNothing(t *testing.T) {
	tests := []struct {
		name     string
		key, val string
		wantErr  string
	}{
		{"unknown key", "treshold_db", "12", "treshold_db"},
		{"bad number", "threshold_db", "loud", "loud"},
		{"bad time of day", "quiet_start", "25:00", "25:00"},
		{"bad duration", "baseline_window", "soon", "soon"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			before := c
			err := c.Set(tc.key, tc.val)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Set error = %v, want an error containing %q", err, tc.wantErr)
			}
			if !reflect.DeepEqual(c, before) {
				t.Errorf("a rejected Set changed the config to %+v", c)
			}
		})
	}
}

// A clip of about a minute needs a pre-roll longer than the 25 s the first
// version allowed. The bound is the recorder's buffer, not a round number.
func TestPreRollReachesTheBufferLength(t *testing.T) {
	if _, err := Parse(strings.NewReader(minimal + "pre_roll_s = 60\n")); err != nil {
		t.Errorf("pre_roll_s = 60 was refused: %v", err)
	}
	if _, err := Parse(strings.NewReader(minimal + "pre_roll_s = 61\n")); err == nil {
		t.Error("pre_roll_s = 61 was accepted, but the buffer cannot hold it")
	}
}

// clip_lowpass_hz is the one privacy setting in the file. It defaults to the
// value SPEC.md section 3.2 sets, so a fresh install keeps today's behavior, and
// it is not on the dashboard's editable list (SPEC.md section 15 decision 18).
func TestClipLowpassDefaultsToOneKilohertz(t *testing.T) {
	c, err := Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.ClipLowpassHz != 1000 {
		t.Errorf("clip_lowpass_hz = %g, want 1000", c.ClipLowpassHz)
	}
}

func TestParseReadsEveryClipLowpassChoice(t *testing.T) {
	for _, hz := range []float64{500, 1000, 2000, 4000, 6000, 8000, 12000} {
		in := minimal + "clip_lowpass_hz = " + strconv.FormatFloat(hz, 'f', -1, 64) + "\n"
		c, err := Parse(strings.NewReader(in))
		if err != nil {
			t.Errorf("clip_lowpass_hz = %g: %v", hz, err)
			continue
		}
		if c.ClipLowpassHz != hz {
			t.Errorf("clip_lowpass_hz = %g, want %g", c.ClipLowpassHz, hz)
		}
	}
}

// A cutoff off the list would ask for a decimation that is not a whole
// number of samples. The message must name the key and list what may be
// used, so the owner does not have to read the source to fix the file.
func TestParseRefusesAClipLowpassThatIsNotAChoice(t *testing.T) {
	for _, v := range []string{"0", "250", "750", "1500", "16000", "24000", "-500", "loud"} {
		_, err := Parse(strings.NewReader(minimal + "clip_lowpass_hz = " + v + "\n"))
		if err == nil {
			t.Errorf("clip_lowpass_hz = %s was accepted", v)
			continue
		}
		if !strings.Contains(err.Error(), "clip_lowpass_hz") {
			t.Errorf("clip_lowpass_hz = %s: error %q does not name the key", v, err)
		}
		if v == "loud" {
			continue // a value that is not a number cannot be checked against the list
		}
		for _, want := range []string{"500", "1000", "2000", "4000", "6000", "8000", "12000"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("clip_lowpass_hz = %s: error %q does not list %s", v, err, want)
			}
		}
	}
}

// camera_audio is the second privacy setting in the file. A fresh install
// must force -an on every ffmpeg command line, so the default is false and
// nothing but the text "true" turns it on (SPEC.md section 15 decision 22).
func TestCameraAudioDefaultsToOff(t *testing.T) {
	c, err := Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.CameraAudio {
		t.Error("camera_audio = true on a fresh install; the camera's microphone would be recorded")
	}
}

func TestParseReadsCameraAudio(t *testing.T) {
	for text, want := range map[string]bool{"true": true, "false": false} {
		c, err := Parse(strings.NewReader(minimal + "camera_audio = " + text + "\n"))
		if err != nil {
			t.Errorf("camera_audio = %s: %v", text, err)
			continue
		}
		if c.CameraAudio != want {
			t.Errorf("camera_audio = %s gave %v", text, c.CameraAudio)
		}
	}
	// "yes" read as false would switch a privacy setting off in silence,
	// and "on" read as true would switch it on.
	for _, text := range []string{"yes", "no", "on", "1", "0", "True", ""} {
		if _, err := Parse(strings.NewReader(minimal + "camera_audio = " + text + "\n")); err == nil {
			t.Errorf("camera_audio = %q was accepted", text)
		}
	}
}

// Every level rests on sensitivity_dbfs, and the datasheet figure carries a
// per-unit tolerance of plus or minus 2 dB. A fresh install must say so
// rather than present the figure as exact (SPEC.md section 15 decision 23).
func TestSensitivityStartsAsTheDatasheetFigure(t *testing.T) {
	c, err := Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"sensitivity_source", c.SensitivitySource, "datasheet"},
		{"sensitivity_uncertainty_db", c.SensitivityUncertaintyDB, 2.0},
		{"sensitivity_measured_on", c.SensitivityMeasuredOn, ""},
		{"sensitivity_reference", c.SensitivityReference, ""},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.name, ch.got, ch.want)
		}
	}
}

// A measured sensitivity is a claim about a day and a reference. The two
// states must not blur: measured needs the date, and datasheet must carry
// neither the date nor the reference.
func TestParseKeepsTheTwoSensitivitySourcesApart(t *testing.T) {
	ok := minimal + "sensitivity_source = measured\n" +
		"sensitivity_measured_on = 2026-10-03\n" +
		"sensitivity_reference = B and K 4231 at 94 dB SPL\n" +
		"sensitivity_uncertainty_db = 0.5\n"
	c, err := Parse(strings.NewReader(ok))
	if err != nil {
		t.Fatalf("a measured sensitivity was refused: %v", err)
	}
	if c.SensitivitySource != "measured" || c.SensitivityMeasuredOn != "2026-10-03" {
		t.Errorf("read %q on %q", c.SensitivitySource, c.SensitivityMeasuredOn)
	}
	if c.SensitivityReference != "B and K 4231 at 94 dB SPL" {
		t.Errorf("sensitivity_reference = %q", c.SensitivityReference)
	}
	if c.SensitivityUncertaintyDB != 0.5 {
		t.Errorf("sensitivity_uncertainty_db = %v, want 0.5", c.SensitivityUncertaintyDB)
	}

	bad := []struct{ name, text, names string }{
		{"measured with no date", "sensitivity_source = measured\n", "sensitivity_measured_on"},
		{"a date that is not a date", "sensitivity_source = measured\nsensitivity_measured_on = last Tuesday\n", "sensitivity_measured_on"},
		{"a date with no day", "sensitivity_source = measured\nsensitivity_measured_on = 2026-10\n", "sensitivity_measured_on"},
		{"a source that is neither", "sensitivity_source = guessed\n", "sensitivity_source"},
		{"datasheet with a date", "sensitivity_measured_on = 2026-10-03\n", "sensitivity_measured_on"},
		{"datasheet with a reference", "sensitivity_reference = a calibrator\n", "sensitivity_reference"},
		{"uncertainty below zero", "sensitivity_uncertainty_db = -1\n", "sensitivity_uncertainty_db"},
		{"uncertainty above ten", "sensitivity_uncertainty_db = 10.5\n", "sensitivity_uncertainty_db"},
	}
	for _, b := range bad {
		_, err := Parse(strings.NewReader(minimal + b.text))
		if err == nil {
			t.Errorf("%s was accepted", b.name)
			continue
		}
		if !strings.Contains(err.Error(), b.names) {
			t.Errorf("%s: error %q does not name %s", b.name, err, b.names)
		}
	}
}

// retention_days is how long a recording is kept before its files are
// deleted. 0 keeps every recording forever. The number is written out here
// rather than read from Default, so a changed default fails the test.
func TestRetentionDaysDefaultsTo90AndReadsTheFile(t *testing.T) {
	c, err := Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.RetentionDays != 90 {
		t.Errorf("retention_days = %d by default, want 90", c.RetentionDays)
	}
	for in, want := range map[string]int{"0": 0, "7": 7, "3650": 3650} {
		c, err := Parse(strings.NewReader(minimal + "retention_days = " + in + "\n"))
		if err != nil {
			t.Errorf("retention_days = %s: %v", in, err)
			continue
		}
		if c.RetentionDays != want {
			t.Errorf("retention_days = %s read as %d", in, c.RetentionDays)
		}
	}
	for _, in := range []string{"-1", "3651", "ninety", "90d"} {
		_, err := Parse(strings.NewReader(minimal + "retention_days = " + in + "\n"))
		if err == nil || !strings.Contains(err.Error(), "retention_days") {
			t.Errorf("retention_days = %s: error = %v, want one naming retention_days", in, err)
		}
	}
}

// at is an instant on a fixed day, used to test the pause windows against a
// clock time without caring about the calendar date.
func at(h, m int) time.Time { return time.Date(2026, 9, 13, h, m, 0, 0, time.UTC) }

// recording_pause is one or more spans, "HH:MM-HH:MM" separated by commas, so
// a pause is either fully set for each window or off. A half-filled span
// would pause the box at hours nobody chose.
func TestRecordingPauseReadsOneSpan(t *testing.T) {
	c := Default()
	if len(c.RecordingPause) != 0 {
		t.Fatalf("the default pause is %+v, want none", c.RecordingPause)
	}
	if err := c.Set("recording_pause", "21:30-06:45"); err != nil {
		t.Fatalf("Set recording_pause: %v", err)
	}
	if len(c.RecordingPause) != 1 {
		t.Fatalf("got %d windows, want 1: %+v", len(c.RecordingPause), c.RecordingPause)
	}
	if c.RecordingPause[0].Start != 21*time.Hour+30*time.Minute || c.RecordingPause[0].End != 6*time.Hour+45*time.Minute {
		t.Errorf("RecordingPause[0] = %+v, want 21h30m to 6h45m", c.RecordingPause[0])
	}
	// 23:00 is inside a pause that wraps past midnight, and 07:00 is not.
	if !c.RecordingPause.Contains(at(23, 0)) || c.RecordingPause.Contains(at(7, 0)) {
		t.Errorf("Contains is wrong for a pause that wraps past midnight: 23:00 %v, 07:00 %v",
			c.RecordingPause.Contains(at(23, 0)), c.RecordingPause.Contains(at(7, 0)))
	}
	if err := c.Set("recording_pause", ""); err != nil {
		t.Fatalf("Set recording_pause to empty: %v", err)
	}
	if len(c.RecordingPause) != 0 {
		t.Errorf("an empty recording_pause left %+v, want none", c.RecordingPause)
	}
}

// The owner may want more than one pause a day, such as an afternoon nap and
// the night. Each window is checked and reported on its own.
func TestRecordingPauseReadsSeveralWindows(t *testing.T) {
	c := Default()
	if err := c.Set("recording_pause", "21:00-23:00, 12:00-13:00"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(c.RecordingPause) != 2 {
		t.Fatalf("got %d windows, want 2: %+v", len(c.RecordingPause), c.RecordingPause)
	}
	for _, tc := range []struct {
		clock string
		h, m  int
		want  bool
	}{{"inside the first", 22, 0, true}, {"inside the second", 12, 30, true}, {"between", 15, 0, false}, {"at the end", 23, 0, false}} {
		if got := c.RecordingPause.Contains(at(tc.h, tc.m)); got != tc.want {
			t.Errorf("Contains %s (%02d:%02d) = %v, want %v", tc.clock, tc.h, tc.m, got, tc.want)
		}
	}
	if got := c.RecordingPause.String(); got != "21:00-23:00, 12:00-13:00" {
		t.Errorf("String() = %q, want the windows in the order given", got)
	}
}

func TestRecordingPauseRejectsMistakesAndChangesNothing(t *testing.T) {
	for _, val := range []string{
		"22:00", "22:00-", "-07:00", "22:00-25:00", "7:00-9:00", "22:00-22:00", "22:00 - 07:00",
		"21:00-23:00,", "21:00-23:00, 12:00", "21:00-23:00, 12:00-12:00",
		"22:00-01:00, 00:30-07:00", // 00:30-01:00 is in both
		"22:00-01:00, 01:00-07:00", // the first ends exactly where the second starts
		"12:00-13:00, 12:30-12:45", // the second is inside the first
	} {
		t.Run(val, func(t *testing.T) {
			c := Default()
			c.RecordingPause = PauseWindows{{Start: 20 * time.Hour, End: 21 * time.Hour}}
			before := c
			if err := c.Set("recording_pause", val); err == nil {
				t.Fatalf("Set recording_pause %q returned no error", val)
			}
			if !reflect.DeepEqual(c, before) {
				t.Errorf("a rejected Set changed the pause to %+v", c.RecordingPause)
			}
		})
	}
}

// Windows that just miss each other, even across midnight, are not
// overlapping and must be accepted.
func TestRecordingPauseAcceptsWindowsThatDoNotOverlapOrTouch(t *testing.T) {
	c := Default()
	if err := c.Set("recording_pause", "22:00-01:00, 02:00-03:00"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(c.RecordingPause) != 2 {
		t.Fatalf("got %d windows, want 2: %+v", len(c.RecordingPause), c.RecordingPause)
	}
}
