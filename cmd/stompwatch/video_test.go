package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/video"
)

// The password every video test uses. It has the characters an RTSP URL
// escapes, so both the plain and the escaped form are looked for.
const camPass = "p@ss:w/rd#1"

// fakeFFmpegScript writes a shell script that stands in for ffmpeg. works
// is the text a URL must contain for the camera test to succeed; the rest fail
// the way ffmpeg fails, naming the URL it was given.
func fakeFFmpegScript(t *testing.T, works string) string {
	return fakeFFmpegScriptLines(t, works, "")
}

// fakeFFmpegScriptWithAudio is the same, for a camera that sends an audio
// track as well as video.
func fakeFFmpegScriptWithAudio(t *testing.T, works string) string {
	return fakeFFmpegScriptLines(t, works,
		"  Stream #0:1: Audio: aac (LC), 16000 Hz, mono, fltp")
}

// fakeFFmpegScriptLines writes the script. audioLine is printed after the
// video line when it is not empty, the way ffmpeg lists a second stream.
func fakeFFmpegScriptLines(t *testing.T, works, audioLine string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ffmpeg")
	extra := ""
	if audioLine != "" {
		extra = `echo "` + audioLine + `" >&2; `
	}
	script := `#!/bin/sh
for a in "$@"; do
  case "$a" in
    *` + works + `*) echo "  Stream #0:0: Video: h264 (Main), yuv420p(progressive), 640x360, 15 fps, 15 tbr, 90k tbn" >&2; ` + extra + `exit 0;;
  esac
done
for a in "$@"; do
  case "$a" in
    rtsp://*) echo "$a: 401 Unauthorized" >&2;;
  esac
done
exit 1
`
	if works == "" {
		script = strings.Replace(script, "*"+works+"*", "never-matches-anything", 1)
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func setCameraLogin(t *testing.T) {
	t.Helper()
	t.Setenv("CAMERA_USER", "admin")
	t.Setenv("CAMERA_PASS", camPass)
}

// noPassword fails the test if either form of the password is in text.
func noPassword(t *testing.T, what, text string) {
	t.Helper()
	if strings.Contains(text, camPass) || strings.Contains(text, "p%40ss") {
		t.Fatalf("%s holds the camera password:\n%s", what, text)
	}
}

// SPEC.md section 7: test-camera tries each path form, reports which works, and
// prints the resolution and frame rate. The owner reads this, so the
// password is masked everywhere.
func TestCameraTestReportsTheWorkingPath(t *testing.T) {
	setCameraLogin(t)
	cfg, _ := writeConfig(t, "camera_host = cam.invalid")
	ffmpeg := fakeFFmpegScript(t, "h264Preview_01_")

	code, stdout, stderr := runCmd("test-camera", "-config", cfg, "-ffmpeg", ffmpeg)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	for _, want := range []string{
		"rtsp://admin:***@cam.invalid:554/Preview_01_sub: failed: rtsp://admin:***@cam.invalid:554/Preview_01_sub: 401 Unauthorized",
		"rtsp://admin:***@cam.invalid:554/h264Preview_01_sub: works: 640x360 at 15 fps (h264)",
		"rtsp://admin:***@cam.invalid:554/h264Preview_01_main: works: 640x360 at 15 fps (h264)",
		"camera_host = cam.invalid",
		"camera_rtsp_path = h264Preview_01_sub",
		"camera_rtsp_path_main = h264Preview_01_main",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	noPassword(t, "stdout", stdout)
	noPassword(t, "stderr", stderr)
}

func TestCameraTestFailsWhenNoPathWorks(t *testing.T) {
	setCameraLogin(t)
	cfg, _ := writeConfig(t, "camera_host = cam.invalid")
	ffmpeg := fakeFFmpegScript(t, "")

	code, stdout, stderr := runCmd("test-camera", "-config", cfg, "-ffmpeg", ffmpeg)
	if code == 0 {
		t.Fatalf("exit 0 with no working path\n%s", stdout)
	}
	for _, want := range []string{"Preview_01_sub: failed", "h264Preview_01_sub: failed", "401 Unauthorized", "No sub-stream path worked"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	noPassword(t, "stdout", stdout)
	noPassword(t, "stderr", stderr)
}

func TestCameraTestNeedsALoginAndAHost(t *testing.T) {
	cfg, _ := writeConfig(t, "camera_host = cam.invalid")
	code, _, stderr := runCmd("test-camera", "-config", cfg, "-ffmpeg", fakeFFmpegScript(t, ""))
	if code == 0 || !strings.Contains(stderr, "CAMERA_USER") {
		t.Errorf("without a login: exit %d, stderr %q", code, stderr)
	}

	setCameraLogin(t)
	cfg, _ = writeConfig(t)
	code, _, stderr = runCmd("test-camera", "-config", cfg, "-ffmpeg", fakeFFmpegScript(t, ""))
	if code == 0 || !strings.Contains(stderr, "camera_host") {
		t.Errorf("without a host: exit %d, stderr %q", code, stderr)
	}
}

// SPEC.md section 7 and the rule above every slice of this phase: nothing in the
// collector depends on the camera. With a camera host that does not
// resolve, the run still measures, detects, and writes the audio clip, and
// says in the health log that there is no video.
func TestRunCarriesOnWhenTheCameraDoesNotResolve(t *testing.T) {
	setCameraLogin(t)
	cfg, dir := writeConfig(t, "camera_host = camera.invalid", "video_dir = "+filepath.Join(tempDir(t), "video"))
	wav := thumps(t, dir)
	// A fake ffmpeg that fails at once, the way a name that does not
	// resolve fails, and names the URL it was given as ffmpeg does.
	ffmpeg := fakeFFmpegScript(t, "")

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav, "-ffmpeg", ffmpeg)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	noPassword(t, "stderr", stderr)
	logFile, err := os.ReadFile(filepath.Join(dir, "logs", "stompwatch.log"))
	if err != nil {
		t.Fatal(err)
	}
	noPassword(t, "the log file", string(logFile))

	db := readDB(t, dir)
	if n := rows(t, db, `SELECT count(*) FROM events`); n != 1 {
		t.Errorf("%d events, want 1", n)
	}
	if n := rows(t, db, `SELECT count(*) FROM event_media WHERE kind = 'audio'`); n != 1 {
		t.Errorf("%d audio clips, want 1", n)
	}
	if n := rows(t, db, `SELECT count(*) FROM event_media WHERE kind = 'video'`); n != 0 {
		t.Errorf("%d video clips, want 0", n)
	}
	if n := rows(t, db, `SELECT count(*) FROM system_health WHERE kind = 'camera_disconnect'`); n < 1 {
		t.Errorf("%d camera_disconnect rows, want at least 1", n)
	}
	if n := rows(t, db, `SELECT count(*) FROM system_health WHERE kind = 'clip_truncated' AND detail LIKE '%no video%'`); n != 1 {
		t.Errorf("%d rows say the event has no video, want 1", n)
	}
	if n := rows(t, db, `SELECT count(*) FROM system_health WHERE detail LIKE '%`+camPass+`%' OR detail LIKE '%p%40ss%'`); n != 0 {
		t.Errorf("%d health rows hold the password", n)
	}
}

// Without ffmpeg the collector says so once, at start, and carries on.
func TestRunSaysWhenFFmpegIsMissing(t *testing.T) {
	setCameraLogin(t)
	cfg, dir := writeConfig(t, "camera_host = camera.invalid", "video_dir = "+filepath.Join(tempDir(t), "video"))
	wav := thumps(t, dir)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav, "-ffmpeg", filepath.Join(dir, "no-such-ffmpeg"))
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "ffmpeg is missing") || !strings.Contains(stderr, "apt install ffmpeg") {
		t.Errorf("the log does not say ffmpeg is missing or how to install it:\n%s", stderr)
	}
	db := readDB(t, dir)
	if n := rows(t, db, `SELECT count(*) FROM system_health WHERE kind = 'camera_disconnect' AND detail LIKE '%ffmpeg%'`); n != 1 {
		t.Errorf("%d health rows about ffmpeg, want 1", n)
	}
	if n := rows(t, db, `SELECT count(*) FROM event_media WHERE kind = 'audio'`); n != 1 {
		t.Errorf("%d audio clips, want 1", n)
	}
}

// Video off is the default, and then no ffmpeg is looked for and nothing
// about a camera is logged as a problem.
func TestRunWithoutACameraNeverMentionsFFmpeg(t *testing.T) {
	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)
	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav, "-ffmpeg", filepath.Join(dir, "no-such-ffmpeg"))
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if strings.Contains(stderr, "ffmpeg") {
		t.Errorf("ffmpeg was mentioned with video off:\n%s", stderr)
	}
	db := readDB(t, dir)
	if n := rows(t, db, `SELECT count(*) FROM system_health WHERE kind = 'camera_disconnect'`); n != 0 {
		t.Errorf("%d camera rows with video off", n)
	}
}

// tempDir is a fresh temporary directory, for a path that goes in a config line
// before writeConfig has made its own directory.
func tempDir(t *testing.T) string { return t.TempDir() }

// A clock more than 2 s out is a warning and a clock_drift row; a clean
// check is neither (SPEC.md section 7).
func TestReportClocksWritesADriftRowPerProblem(t *testing.T) {
	var rows []string
	var durations []time.Duration
	record := func(_ time.Time, kind, detail string, d time.Duration) {
		rows = append(rows, kind+": "+detail)
		durations = append(durations, d)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	reportClocks(video.ClockReport{HostOffset: 500 * time.Millisecond, CameraOffset: -800 * time.Millisecond}, log, record)
	if len(rows) != 0 {
		t.Fatalf("a clean check wrote %v", rows)
	}

	reportClocks(video.ClockReport{HostOffset: 200 * time.Millisecond, CameraOffset: 3 * time.Second}, log, record)
	if len(rows) != 1 || !strings.HasPrefix(rows[0], "clock_drift: the camera clock is 3.2s from NTP") {
		t.Fatalf("rows = %v", rows)
	}
	if durations[0] != 3200*time.Millisecond {
		t.Errorf("duration = %v, want the drift", durations[0])
	}

	rows = nil
	reportClocks(video.ClockReport{HostErr: errors.New("no route"), CameraErr: video.ErrNoCameraClock}, log, record)
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want one for NTP and one for the camera", rows)
	}
}

// A drift logged as -12000000 with no unit in its key reads as 12 ms to one
// reader and 12 seconds to another. Both drift fields name their unit, the way
// rate_hz and ring_minutes do elsewhere.
func TestTheClockLinesNameTheirUnit(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	record := func(time.Time, string, string, time.Duration) {}

	// A clean check, then one with a problem: both lines carry the drift.
	reportClocks(video.ClockReport{HostOffset: -12 * time.Millisecond,
		CameraOffset: 300 * time.Millisecond}, log, record)
	reportClocks(video.ClockReport{HostOffset: -12 * time.Millisecond,
		CameraOffset: 3 * time.Second}, log, record)

	for _, want := range []string{
		`"host_from_ntp_ms":-12`,
		`"camera_from_host_ms":300`,
		`"camera_from_host_ms":3000`,
		`"limit_ms":2000`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("no %s in the clock lines:\n%s", want, buf.String())
		}
	}
	for _, unitless := range []string{`"host_from_ntp":`, `"camera_from_host":`, `"limit":`} {
		if strings.Contains(buf.String(), unitless) {
			t.Errorf("%s is still logged with no unit in its key:\n%s", unitless, buf.String())
		}
	}
}

// SPEC.md section 15 decision 22: camera_audio does nothing on a camera that
// sends no audio track, so test-camera says which kind the camera is.
func TestCameraTestSaysWhetherTheCameraSendsAudio(t *testing.T) {
	setCameraLogin(t)
	cfg, _ := writeConfig(t, "camera_host = cam.invalid")

	code, stdout, stderr := runCmd("test-camera", "-config", cfg,
		"-ffmpeg", fakeFFmpegScript(t, "h264Preview_01_"))
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "no audio track") || !strings.Contains(stdout, "camera_audio") {
		t.Errorf("the report does not say the camera sends no audio, or does not name the setting:\n%s", stdout)
	}

	// The first run took the login out of the environment, as a real run
	// does, so the second one needs it put back.
	setCameraLogin(t)
	code, stdout, stderr = runCmd("test-camera", "-config", cfg,
		"-ffmpeg", fakeFFmpegScriptWithAudio(t, "h264Preview_01_"))
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	for _, want := range []string{"aac", "16000", "camera_audio"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not mention %q:\n%s", want, stdout)
		}
	}
}

// The line that says the camera's audio is recorded into the ring and every
// clip must be true when it is printed. It claims what the ring's own command
// line does, so a ring that is not set up to keep the audio cannot be
// described as keeping it. For a day the line came from the setting, and it
// said audio was being recorded while every clip came out silent.
func TestTheStartLineOnlyClaimsTheAudioTheRingKeeps(t *testing.T) {
	ffmpeg := fakeFFmpegScriptWithAudio(t, "h264Preview_01_")
	streams := []video.Stream{{Host: "cam.invalid", Port: 554, Path: "h264Preview_01_sub",
		Creds: video.Credentials{User: "admin", Pass: camPass}}}

	// A real ring, built both ways. The ring's own command line is what the
	// line has to agree with, so the ring is what the check is given.
	logOf := func(recording bool) string {
		t.Helper()
		ring, err := video.NewRing(video.RingConfig{
			Dir: t.TempDir(), Streams: streams, SegmentSeconds: 10,
			Keep: time.Minute, Audio: recording,
		})
		if err != nil {
			t.Fatalf("recording = %v: NewRing: %v", recording, err)
		}
		var buf strings.Builder
		log := slog.New(slog.NewJSONHandler(&buf, nil))
		a, known := checkCameraAudio(video.Command{ffmpeg}, streams, ring, log)
		if !known || !a.Present {
			t.Fatalf("recording = %v: the camera test did not find the audio track: %s", recording, buf.String())
		}
		return buf.String()
	}

	kept := logOf(true)
	if !strings.Contains(kept, "records it into the ring") {
		t.Errorf("the ring keeps the audio and nothing says so:\n%s", kept)
	}

	lost := logOf(false)
	if strings.Contains(lost, "records it into the ring") {
		t.Errorf("the ring does not keep the audio and the log claims it does:\n%s", lost)
	}
	if !strings.Contains(lost, `"level":"ERROR"`) {
		t.Errorf("the ring does not keep the audio and nothing is logged as an error:\n%s", lost)
	}
}

// warnings returns the WARN lines of a log that mention want.
func warnings(stderr, want string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, want) {
			out = append(out, line)
		}
	}
	return out
}

// The owner must never be able to forget which mode the box is in. While
// camera_audio is on, every start warns at WARN, names the setting, and says
// in plain words that the camera records speech in clear
// (SPEC.md section 15 decision 22).
func TestRunWarnsAtEveryStartWhileTheCameraRecordsAudio(t *testing.T) {
	setCameraLogin(t)
	cfg, dir := writeConfig(t, "camera_host = cam.invalid", "camera_audio = true",
		"video_dir = "+filepath.Join(tempDir(t), "video"))
	ffmpeg := fakeFFmpegScriptWithAudio(t, "h264Preview_01_")

	code, _, stderr := runCmd("run", "-config", cfg, "-input", thumps(t, dir), "-ffmpeg", ffmpeg)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	found := warnings(stderr, "camera_audio")
	if len(found) == 0 {
		t.Fatalf("no WARN line names camera_audio:\n%s", stderr)
	}
	for _, want := range []string{"speech", "camera_audio"} {
		if !strings.Contains(found[0], want) {
			t.Errorf("the warning %q does not mention %q", found[0], want)
		}
	}

	// Off is the default, and a warning that appeared on an ordinary install
	// would teach the owner to ignore it.
	cfg, dir = writeConfig(t, "camera_host = cam.invalid",
		"video_dir = "+filepath.Join(tempDir(t), "video"))
	code, _, stderr = runCmd("run", "-config", cfg, "-input", thumps(t, dir), "-ffmpeg", ffmpeg)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if found := warnings(stderr, "camera_audio"); len(found) != 0 {
		t.Errorf("the default warned anyway: %v", found)
	}
}

// A setting that silently does nothing is a trap: with camera_audio on and a
// camera that sends no audio track, the collector says so once at start
// (SPEC.md section 15 decision 22).
func TestRunSaysWhenCameraAudioIsOnAndTheCameraSendsNone(t *testing.T) {
	setCameraLogin(t)
	run := func(ffmpeg string) string {
		t.Helper()
		cfg, dir := writeConfig(t, "camera_host = cam.invalid", "camera_audio = true",
			"camera_rtsp_path = h264Preview_01_sub",
			"video_dir = "+filepath.Join(tempDir(t), "video"))
		code, _, stderr := runCmd("run", "-config", cfg, "-input", thumps(t, dir), "-ffmpeg", ffmpeg)
		if code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", code, stderr)
		}
		return stderr
	}

	silent := run(fakeFFmpegScript(t, "h264Preview_01_")) // video only
	found := warnings(silent, "no audio track")
	if len(found) == 0 {
		t.Fatalf("nothing warns that the camera sends no audio:\n%s", silent)
	}
	if !strings.Contains(found[0], "camera_audio") {
		t.Errorf("the warning %q does not name the setting", found[0])
	}

	// A camera that does send audio must not be warned about: the setting
	// is doing exactly what it says.
	talking := run(fakeFFmpegScriptWithAudio(t, "h264Preview_01_"))
	if found := warnings(talking, "no audio track"); len(found) != 0 {
		t.Errorf("a camera that sends audio was warned about anyway: %v", found)
	}
}

// envFFmpeg is a fake ffmpeg that writes its environment and its arguments
// to two files, then fails the way a camera that refuses the login does.
func envFFmpeg(t *testing.T) (path, envFile, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "ffmpeg")
	envFile, argsFile = filepath.Join(dir, "env"), filepath.Join(dir, "args")
	script := "#!/bin/sh\nenv >> " + envFile + "\necho \"$@\" >> " + argsFile + "\necho 401 Unauthorized >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, envFile, argsFile
}

// checkNoLoginInChildren fails the test if a child saw a CAMERA_ variable,
// if the process still holds one, or if ffmpeg never got the login it was
// meant to use. The URL escapes the password, so the start of the escaped
// form is looked for.
func checkNoLoginInChildren(t *testing.T, envFile, argsFile string) {
	t.Helper()
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("ffmpeg never ran: %v", err)
	}
	if strings.Contains(string(env), "CAMERA_") {
		t.Errorf("ffmpeg's environment holds a CAMERA_ variable")
	}
	// Not noPassword: it prints the text, and this text is the whole
	// environment of the machine running the test.
	if strings.Contains(string(env), camPass) {
		t.Errorf("ffmpeg's environment holds the camera password")
	}
	for _, k := range []string{"CAMERA_USER", "CAMERA_PASS"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Errorf("%s is still in the process environment after the login was read", k)
		}
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "rtsp://admin:p%40ss") {
		t.Errorf("ffmpeg did not get the loaded login in its URL")
	}
}

// systemd loads CAMERA_USER and CAMERA_PASS with EnvironmentFile=. The
// collector reads them once and takes them out of its environment, so
// ffmpeg, arecord, amixer and tailscale do not inherit the password. The
// login it read still reaches ffmpeg, on the URL.
func TestRunKeepsTheLoginOutOfEveryChildsEnvironment(t *testing.T) {
	setCameraLogin(t)
	cfg, dir := writeConfig(t, "camera_host = camera.invalid", "video_dir = "+filepath.Join(tempDir(t), "video"))
	wav := thumps(t, dir)
	ffmpeg, envFile, argsFile := envFFmpeg(t)

	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav, "-ffmpeg", ffmpeg)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	checkNoLoginInChildren(t, envFile, argsFile)
}

func TestCameraTestKeepsTheLoginOutOfFFmpegsEnvironment(t *testing.T) {
	setCameraLogin(t)
	cfg, _ := writeConfig(t, "camera_host = cam.invalid")
	ffmpeg, envFile, argsFile := envFFmpeg(t)

	runCmd("test-camera", "-config", cfg, "-ffmpeg", ffmpeg)
	checkNoLoginInChildren(t, envFile, argsFile)
}
