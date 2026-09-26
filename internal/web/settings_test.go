package web

import (
	"net/http"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/settings"
)

// settingByKey finds one entry of the settings list.
func settingByKey(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	for _, s := range list(t, m, "settings") {
		if str(t, s, "key") == key {
			return s
		}
	}
	t.Fatalf("the settings list has no %q", key)
	return nil
}

// settingValue is the other half of config.Set. This holds the two together:
// the text it writes must parse back to the settings it was read from.
func TestSettingValuesSurviveARoundTrip(t *testing.T) {
	file := fileConfig(t, t.TempDir())
	// Values away from the defaults, so a formatter that returns a constant
	// cannot pass.
	file.ThresholdDB = 12.5
	file.MinDuration = 750 * time.Millisecond
	file.Hangover = 1500 * time.Millisecond
	file.Cooldown = 3000 * time.Millisecond
	file.MaxEvent = 120 * time.Second
	file.MinBaseline = 45 * time.Second
	file.PreRoll = 8 * time.Second
	file.PostRoll = 12 * time.Second
	file.BaselineWindow = 7 * time.Minute
	file.BaselinePercentile = 12.5
	file.Quiet = config.QuietHours{Start: 23*time.Hour + 30*time.Minute, End: 6*time.Hour + 15*time.Minute}
	file.RecordingPause = config.PauseWindows{{Start: 21*time.Hour + 30*time.Minute, End: 6 * time.Hour}}
	file.BatchInterval = 20 * time.Second
	file.DiskWarnFreeMB = 4096
	file.RetentionDays = 45
	file.CameraHost = "cam.local"
	file.CameraPort = 8554
	file.CameraRTSPPath = "Preview_01_sub"
	file.CameraRTSPPathMain = "Preview_01_main"
	file.VideoRingMinutes = 25
	file.VideoSegmentSeconds = 7
	// Away from the default here too, so a formatter that always writes
	// "false" cannot pass.
	file.CameraAudio = true

	for _, f := range settings.Editable {
		text := settingValue(f.Key, file)
		// A text setting may be empty on purpose; nothing else may be.
		if text == "" && f.Type != "text" {
			t.Errorf("%s has no value; every editable setting must have one", f.Key)
			continue
		}
		back := file
		if err := back.Set(f.Key, text); err != nil {
			t.Errorf("%s = %q, which the config parser refuses: %v", f.Key, text, err)
			continue
		}
		if !sameConfig(back, file) {
			t.Errorf("%s = %q, which parses back to different settings", f.Key, text)
		}
	}

	if got := settingValue("recording_pause", file); got != "21:30-06:00" {
		t.Errorf("recording_pause = %q, want %q", got, "21:30-06:00")
	}

	// Several windows are written as several ranges, comma-separated.
	twoWindows := file
	twoWindows.RecordingPause = config.PauseWindows{
		{Start: 21*time.Hour + 30*time.Minute, End: 6 * time.Hour},
		{Start: 12 * time.Hour, End: 13 * time.Hour},
	}
	if got := settingValue("recording_pause", twoWindows); got != "21:30-06:00, 12:00-13:00" {
		t.Errorf("recording_pause with two windows = %q, want %q", got, "21:30-06:00, 12:00-13:00")
	}
}

// An empty camera_rtsp_path means "try the default paths in order", not
// "no value", so it must read back as an empty string and parse again.
func TestAnEmptyTextSettingSurvivesTheRoundTrip(t *testing.T) {
	file := fileConfig(t, t.TempDir())
	file.CameraRTSPPath = ""
	if got := settingValue("camera_rtsp_path", file); got != "" {
		t.Fatalf("camera_rtsp_path = %q, want the empty string", got)
	}
	back := file
	if err := back.Set("camera_rtsp_path", ""); err != nil {
		t.Fatalf("the config parser refuses an empty camera_rtsp_path: %v", err)
	}
	if !sameConfig(back, file) {
		t.Error("an empty camera_rtsp_path parses back to different settings")
	}
}

// A text value and a yes-or-no are not quantities, so neither carries a
// number for a form to range-check.
func TestSettingNumberIsNullForTextAndBool(t *testing.T) {
	file := fileConfig(t, t.TempDir())
	file.CameraRTSPPath = "Preview_01_sub"
	file.VideoRingMinutes = 15
	for _, f := range []settings.Field{
		{Key: "camera_rtsp_path", Type: "text"},
		// A bool key reads back as text and has no number either.
		{Key: "video_ring_minutes", Type: "bool"},
	} {
		if got := settingNumber(f, file); got != nil {
			t.Errorf("%s of type %s has value_num %v, want null", f.Key, f.Type, *got)
		}
	}
}

// The camera settings the dashboard may change are in the list, with the
// right types. The two that name a place on disk are not, and neither are
// the host and port the login is sent to.
func TestSettingsListTheCameraKeys(t *testing.T) {
	e := newEnv(t, func(_ *Config, f *config.Config) {
		f.CameraHost = "192.168.1.40"
		f.CameraRTSPPath = "Preview_01_sub"
	})
	m := e.getJSON("/api/settings")

	path := settingByKey(t, m, "camera_rtsp_path")
	if got := str(t, path, "type"); got != "text" {
		t.Errorf("camera_rtsp_path type = %q, want \"text\"", got)
	}
	if got, ok := path["live"].(bool); !ok || got {
		t.Errorf("camera_rtsp_path live = %v, want false", path["live"])
	}
	if got := str(t, path, "value"); got != "Preview_01_sub" {
		t.Errorf("camera_rtsp_path value = %q", got)
	}
	if got := str(t, settingByKey(t, m, "video_ring_minutes"), "value"); got != "10" {
		t.Errorf("video_ring_minutes value = %q, want \"10\"", got)
	}
	if got := str(t, settingByKey(t, m, "video_segment_seconds"), "value"); got != "10" {
		t.Errorf("video_segment_seconds value = %q, want \"10\"", got)
	}

	// A web form must not be able to choose the file the password is read
	// from, or the directory the video is written to.
	for _, s := range list(t, m, "settings") {
		switch str(t, s, "key") {
		case "camera_credentials_file", "video_dir", "video_main_on_event", "camera_host", "camera_port":
			t.Errorf("%q must not be editable from the dashboard", str(t, s, "key"))
		}
	}
}

// The dashboard once let a user set camera_host to a host of their own, press
// Test, and have ffmpeg send the camera login there. The PUT is refused, and
// the camera test still goes to the host in the config file.
func TestTheDashboardCannotSendTheLoginToAnotherHost(t *testing.T) {
	e := withProbe(t, "", nil)
	for _, body := range []map[string]any{
		{"camera_host": "attacker.example"},
		{"camera_port": "8554"},
	} {
		w := e.do(http.MethodPut, "/api/settings", body)
		for key := range body {
			wantError(t, w, 400, key)
		}
	}
	if got := e.live.Current(); got.CameraHost != "127.0.0.1" || got.CameraPort != 554 {
		t.Errorf("the camera is %s:%d after the refused PUTs, want 127.0.0.1:554", got.CameraHost, got.CameraPort)
	}

	w := e.do(http.MethodPost, "/api/camera/probe", nil)
	if w.Code != 200 {
		t.Fatalf("POST probe = %d: %s", w.Code, w.Body.String())
	}
	detail := str(t, list(t, decode(t, w), "tried")[0], "detail")
	if !contains(detail, "@127.0.0.1:554/") || contains(detail, "attacker") || contains(detail, "8554") {
		t.Errorf("the probe tried %q, want the config file's 127.0.0.1:554", detail)
	}
}

// A text setting is checked by config.Validate like every other key. A host
// with a slash, a scheme, or a space is refused, and so is a path that
// could rewrite the stream URL. Nothing is saved.
func TestABadCameraTextSettingChangesNothing(t *testing.T) {
	e := newEnv(t)
	for _, body := range []map[string]any{
		{"camera_host": "rtsp://cam.local/x"},
		{"camera_host": "my camera"},
		{"camera_host": "cam/../etc"},
		{"camera_rtsp_path": "rtsp://cam/x"},
		{"camera_rtsp_path": "admin:pass@cam/x"},
		{"camera_rtsp_path_main": "a b"},
	} {
		w := e.do(http.MethodPut, "/api/settings", body)
		if w.Code != 400 {
			t.Errorf("PUT %v = %d, want 400: %s", body, w.Code, w.Body.String())
			continue
		}
		if got := e.live.Current().CameraHost; got != "" {
			t.Errorf("PUT %v left camera_host as %q", body, got)
		}
		rows, err := e.store.Settings(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Errorf("PUT %v wrote %v to the database", body, rows)
		}
	}
}

func TestSettingsListWhatMayBeChanged(t *testing.T) {
	e := newEnv(t)
	m := e.getJSON("/api/settings")

	th := settingByKey(t, m, "threshold_db")
	if got := str(t, th, "value"); got != "15" {
		t.Errorf("threshold_db value = %q, want \"15\"", got)
	}
	if got := str(t, th, "default"); got != "15" {
		t.Errorf("threshold_db default = %q, want \"15\"", got)
	}
	if got := str(t, th, "source"); got != "file" {
		t.Errorf("source = %q, want \"file\"", got)
	}
	if got := str(t, th, "type"); got != "float" {
		t.Errorf("type = %q, want \"float\"", got)
	}
	if got := str(t, th, "unit"); got != "dB" {
		t.Errorf("unit = %q, want \"dB\"", got)
	}
	if got := num(t, th, "min"); got != 1 {
		t.Errorf("min = %v, want 1", got)
	}
	if got := num(t, th, "max"); got != 60 {
		t.Errorf("max = %v, want 60", got)
	}
	if got, ok := th["live"].(bool); !ok || !got {
		t.Errorf("live = %v, want true", th["live"])
	}

	// batch_interval needs a restart, and the interface must say so.
	if got, ok := settingByKey(t, m, "batch_interval")["live"].(bool); !ok || got {
		t.Errorf("batch_interval live = %v, want false", settingByKey(t, m, "batch_interval")["live"])
	}
	if got := str(t, settingByKey(t, m, "quiet_start"), "value"); got != "22:00" {
		t.Errorf("quiet_start = %q, want \"22:00\"", got)
	}

	// Capture and path settings are never editable from a phone at 2am.
	for _, s := range list(t, m, "settings") {
		switch str(t, s, "key") {
		case "capture_device", "sensitivity_dbfs", "db_path", "clip_dir", "calibration_file":
			t.Errorf("%q must not be editable from the dashboard", str(t, s, "key"))
		}
	}
}

func TestChangingASettingTakesEffectAndIsSaved(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodPut, "/api/settings",
		map[string]any{"threshold_db": "12", "quiet_start": "22:30"},
		"Tailscale-User-Login", "me@example.com")
	if w.Code != 200 {
		t.Fatalf("PUT settings = %d, want 200: %s", w.Code, w.Body.String())
	}

	// The answer is the same shape as the GET, with the new values.
	m := decode(t, w)
	th := settingByKey(t, m, "threshold_db")
	if got := str(t, th, "value"); got != "12" {
		t.Errorf("value = %q, want \"12\"", got)
	}
	if got := str(t, th, "source"); got != "db" {
		t.Errorf("source = %q, want \"db\"", got)
	}
	if got := str(t, th, "default"); got != "15" {
		t.Errorf("default = %q, want \"15\", which is what the config file says", got)
	}

	// The running process uses it.
	if got := e.live.Current().ThresholdDB; got != 12 {
		t.Errorf("the collector is running with threshold_db %v, want 12", got)
	}
	// And it is on disk, so a restart keeps it.
	rows, err := e.store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rows["threshold_db"] != "12" || rows["quiet_start"] != "22:30" {
		t.Errorf("the config table holds %v, want threshold_db 12 and quiet_start 22:30", rows)
	}
}

// A value that fails validation rejects the whole request, changes nothing,
// and never reaches the disk.
func TestABadSettingChangesNothing(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"out of range", map[string]any{"threshold_db": "900"}},
		{"not a number", map[string]any{"threshold_db": "loud"}},
		{"not a time", map[string]any{"quiet_start": "half past ten"}},
		{"not editable here", map[string]any{"capture_device": "hw:0,0"}},
		{"one good and one bad", map[string]any{"threshold_db": "12", "cooldown_ms": "-1"}},
	}
	for _, c := range cases {
		w := e.do(http.MethodPut, "/api/settings", c.body)
		if w.Code != 400 {
			t.Errorf("%s: PUT = %d, want 400: %s", c.name, w.Code, w.Body.String())
			continue
		}
		wantError(t, w, 400, "")
		if got := e.live.Current().ThresholdDB; got != 15 {
			t.Errorf("%s: the collector is running with threshold_db %v, want the unchanged 15", c.name, got)
		}
		rows, err := e.store.Settings(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Errorf("%s: a rejected change wrote %v to the database", c.name, rows)
		}
	}
}

// null puts a setting back to what the config file says.
func TestNullRemovesTheOverride(t *testing.T) {
	e := newEnv(t)
	if w := e.do(http.MethodPut, "/api/settings", map[string]any{"threshold_db": "12"}); w.Code != 200 {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	w := e.raw(http.MethodPut, "/api/settings", `{"threshold_db":null}`)
	if w.Code != 200 {
		t.Fatalf("PUT null = %d, want 200: %s", w.Code, w.Body.String())
	}

	th := settingByKey(t, decode(t, w), "threshold_db")
	if got := str(t, th, "value"); got != "15" {
		t.Errorf("value = %q, want \"15\" back from the file", got)
	}
	if got := str(t, th, "source"); got != "file" {
		t.Errorf("source = %q, want \"file\"", got)
	}
	if got := e.live.Current().ThresholdDB; got != 15 {
		t.Errorf("the collector is running with threshold_db %v, want 15", got)
	}
	rows, err := e.store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, still := rows["threshold_db"]; still {
		t.Errorf("the config row is still there: %v", rows)
	}
}

func TestSettingsRejectsABodyThatIsNotSettings(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`[]`, `"threshold_db"`, `{"threshold_db":12}`, `{`} {
		w := e.raw(http.MethodPut, "/api/settings", body)
		if w.Code != 400 {
			t.Errorf("PUT %s = %d, want 400: %s", body, w.Code, w.Body.String())
		}
	}
}

func TestSettingsRejectsAQueryItDoesNotKnow(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.get("/api/settings?all=1"), 400, "all")
}

// The range a setting is checked against is counted in one unit, but the
// value is text in the unit the config file uses. A form cannot check a
// number it has to parse out of Go duration text, so the number comes too.
func TestSettingsCarryTheValueAsANumberInTheRangeUnit(t *testing.T) {
	e := newEnv(t)
	m := e.getJSON("/api/settings")
	got := map[string]any{}
	for _, s := range list(t, m, "settings") {
		got[str(t, s, "key")] = s["value_num"]
	}

	// baseline_window is "10m0s" as text, and its range is in seconds.
	if v, ok := got["baseline_window"].(float64); !ok || v != 600 {
		t.Errorf("baseline_window value_num = %v, want 600", got["baseline_window"])
	}
	if v, ok := got["threshold_db"].(float64); !ok || v != 15 {
		t.Errorf("threshold_db value_num = %v, want 15", got["threshold_db"])
	}
	if v, ok := got["min_duration_ms"].(float64); !ok || v != 400 {
		t.Errorf("min_duration_ms value_num = %v, want 400", got["min_duration_ms"])
	}
	// A clock time has no single number, so it has none.
	if v, present := got["quiet_start"]; !present || v != nil {
		t.Errorf("quiet_start value_num = %v, want null", v)
	}
}

// camera_audio is editable, and the response carries the sentence that says
// what switching it on means. An interface that drew only labels and ranges
// would hide what this setting does (SPEC.md section 15 decision 22).
func TestSettingsCarryCameraAudioAndItsWarning(t *testing.T) {
	e := newEnv(t)
	one := settingByKey(t, e.getJSON("/api/settings"), "camera_audio")
	if got := str(t, one, "value"); got != "false" {
		t.Errorf("camera_audio value = %q, want \"false\" on a default install", got)
	}
	if got := str(t, one, "type"); got != "bool" {
		t.Errorf("camera_audio type = %q, want \"bool\"", got)
	}
	if got, ok := one["live"].(bool); !ok || got {
		t.Errorf("camera_audio live = %v, want false: the ring is built at start", one["live"])
	}
	note := str(t, one, "note")
	for _, want := range []string{"speech", "microphone"} {
		if !contains(note, want) {
			t.Errorf("the note %q does not mention %q", note, want)
		}
	}
}

// Switching it on through the dashboard reaches the settings in force and is
// stored, so the next start records the camera's audio.
func TestCameraAudioCanBeSwitchedOnFromTheDashboard(t *testing.T) {
	e := newEnv(t)
	if e.live.Current().CameraAudio {
		t.Fatal("camera_audio is on before anything set it")
	}
	w := e.do(http.MethodPut, "/api/settings", map[string]any{"camera_audio": "true"})
	if w.Code != 200 {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	if !e.live.Current().CameraAudio {
		t.Error("camera_audio is still off in the settings in force")
	}
	if got := str(t, settingByKey(t, decode(t, w), "camera_audio"), "value"); got != "true" {
		t.Errorf("the response says camera_audio = %q", got)
	}
	stored, err := e.store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stored["camera_audio"] != "true" {
		t.Errorf("the stored rows say camera_audio = %q", stored["camera_audio"])
	}
}
