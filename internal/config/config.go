package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/minayousseif/stompwatch/internal/audio"
	"github.com/minayousseif/stompwatch/internal/clip"
	"github.com/minayousseif/stompwatch/internal/dsp"
)

// Config holds all settings. Keys and defaults follow SPEC.md section 6 and section 15.
type Config struct {
	DBPath  string
	ClipDir string
	LogDir  string

	CaptureDevice       string
	CaptureChannel      int
	ExpectedCaptureGain string
	MixerCard           string
	MixerControl        string
	StuckLimit          time.Duration

	// The microphone's sensitivity, and how well it is known. Every level
	// the instrument reports rests on SensitivityDBFS, so an instrument that
	// prints it as exact is claiming more than it knows
	// (SPEC.md section 15 decision 23).
	//
	// SensitivitySource is "datasheet" or "measured". "datasheet" is the
	// manufacturer's figure for the model, which the EM-01 datasheet gives
	// with a per-unit tolerance of plus or minus 2 dB. "measured" is what
	// stompwatch calibrate read against a reference, and then the day and the
	// reference are required: a measurement nobody can date is not one.
	SensitivityDBFS          float64
	SensitivitySource        string
	SensitivityUncertaintyDB float64
	SensitivityMeasuredOn    string // YYYY-MM-DD, empty for the datasheet figure
	SensitivityReference     string // free text, empty for the datasheet figure
	CalibrationFile          string

	BaselineWindow     time.Duration
	BaselinePercentile float64

	ThresholdDB float64
	MinDuration time.Duration
	Hangover    time.Duration
	Cooldown    time.Duration
	MaxEvent    time.Duration
	MinBaseline time.Duration

	PreRoll  time.Duration
	PostRoll time.Duration

	// ClipLowpassHz is the cutoff of the clip filter. It is here and not in
	// the dashboard's editable list on purpose: it decides what a recording
	// can hold, so changing it takes a config file edit and a restart
	// (SPEC.md section 3.2 and section 15 decision 18).
	ClipLowpassHz float64

	HeartbeatURL         string
	HeartbeatInterval    time.Duration
	HeartbeatStallFactor float64

	BatchInterval  time.Duration
	DiskMinFreeMB  int64
	DiskWarnFreeMB int64

	// RetentionDays is how long the audio and video files of an event are
	// kept. After that many days retention deletes the files, and only the
	// files: the event, its seconds, its levels and its review stay, and
	// every deletion is recorded in media_purge (SPEC.md section 3 rule 3).
	// 0 keeps every recording forever, and then retention does nothing.
	RetentionDays int

	HTTPAddr       string
	AuthMode       string
	AuthHeader     string
	AuthNameHeader string

	Quiet QuietHours

	// RecordingPause is one or more daily spans in which the collector opens
	// no event, cuts no audio clip, and takes no video from the camera. The
	// level of every second is still measured and stored, so the record
	// stays continuous and a pause never reads as a failure. A nil value
	// means no pause. Its key holds "HH:MM-HH:MM", or several separated by
	// commas, so each window is either fully set or absent.
	RecordingPause PauseWindows

	LogFileMB int64
	LogFiles  int

	// Video (SPEC.md section 7). An empty CameraHost switches video off, and then
	// no other video key does anything. The camera login is never here: it
	// comes from the environment or from CameraCredentialsFile (section 3).
	CameraHost            string
	CameraPort            int
	CameraRTSPPath        string // empty means try the default paths in order
	CameraRTSPPathMain    string // empty means the sub path with _main for _sub
	CameraCredentialsFile string
	VideoDir              string
	VideoRingMinutes      int
	VideoMainOnEvent      bool
	VideoSegmentSeconds   int
	NTPServer             string
	// CameraAudio records the camera's own audio track into the ring and
	// the event clips. It is false by default, and then -an is forced on
	// every ffmpeg command line, as SPEC.md section 7 says. True records the
	// track unfiltered: the camera's microphone is full bandwidth and
	// uncalibrated, so the track holds intelligible speech
	// (SPEC.md section 15 decision 22). It changes nothing about the
	// measuring microphone, whose clips stay filtered at ClipLowpassHz.
	CameraAudio bool
}

// VideoEnabled reports whether a camera is configured.
func (c Config) VideoEnabled() bool { return c.CameraHost != "" }

// Where the sensitivity figure came from.
const (
	// SensitivityDatasheet is the manufacturer's figure for the model. It is
	// not this unit's own: the EM-01 datasheet states plus or minus 2 dB of
	// per-unit tolerance.
	SensitivityDatasheet = "datasheet"
	// SensitivityMeasured is what this unit read against a reference, on a
	// named day.
	SensitivityMeasured = "measured"
)

// Authentication modes. The network authenticates the caller; the program
// only reads who that was. See SPEC.md section 9.0.
const (
	AuthTailscale     = "tailscale"
	AuthTrustedHeader = "trusted_header"
	AuthNone          = "none"
)

// Default returns the default settings. ExpectedCaptureGain has no default:
// it must be set in the file.
func Default() Config {
	return Config{
		DBPath:  "/data/noise.db",
		ClipDir: "/data/clips/audio",
		LogDir:  "/data/logs",

		CaptureDevice:  "hw:EM01,0",
		CaptureChannel: 0,
		MixerCard:      "EM01",
		MixerControl:   "Mic",
		StuckLimit:     5 * time.Second,

		SensitivityDBFS:          -13,
		SensitivitySource:        SensitivityDatasheet,
		SensitivityUncertaintyDB: 2,

		BaselineWindow:     10 * time.Minute,
		BaselinePercentile: 10,

		ThresholdDB: 15,
		MinDuration: 400 * time.Millisecond,
		Hangover:    2 * time.Second,
		Cooldown:    5 * time.Second,
		MaxEvent:    300 * time.Second,
		MinBaseline: 30 * time.Second,

		PreRoll:  30 * time.Second,
		PostRoll: 30 * time.Second,

		ClipLowpassHz: dsp.DefaultClipLowpassHz,

		HeartbeatInterval:    5 * time.Minute,
		HeartbeatStallFactor: 2,

		BatchInterval:  15 * time.Second,
		DiskMinFreeMB:  2048,
		DiskWarnFreeMB: 10240,

		RetentionDays: 90,

		HTTPAddr:       "127.0.0.1:8080",
		AuthMode:       AuthTailscale,
		AuthHeader:     "Tailscale-User-Login",
		AuthNameHeader: "Tailscale-User-Name",

		Quiet: QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour},

		LogFileMB: 20,
		LogFiles:  5,

		CameraPort:          554,
		VideoDir:            "/data/clips/video",
		VideoRingMinutes:    10,
		VideoSegmentSeconds: 10,
		NTPServer:           "pool.ntp.org",
	}
}

// Load reads and checks the config file at path.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	defer f.Close()
	c, err := Parse(f)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse reads "key = value" lines on top of the defaults and checks the
// result. Lines that start with # are comments. An unknown key, a repeated
// key, or a value that does not parse is an error, so a typo can never fall
// back to a default without notice.
func Parse(r io.Reader) (Config, error) {
	c := Default()
	setters := c.setters()
	seen := make(map[string]int)

	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		key, val, ok := strings.Cut(s, "=")
		if !ok {
			return Config{}, fmt.Errorf("config line %d: want key = value, got %q", line, s)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		set, known := setters[key]
		if !known {
			return Config{}, fmt.Errorf("config line %d: unknown key %q", line, key)
		}
		if prev, dup := seen[key]; dup {
			return Config{}, fmt.Errorf("config line %d: %s is already set on line %d", line, key, prev)
		}
		seen[key] = line
		if err := set(val); err != nil {
			return Config{}, fmt.Errorf("config line %d: %s: %w", line, key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Set applies one key and value, the same way Parse reads one line of the
// config file. It does not Validate: a caller that sets several keys checks
// the result once at the end. It is here so that settings stored in the
// database go through the file's own parser. A second parser would drift
// from this one, and that is how a measurement instrument starts lying.
func (c *Config) Set(key, value string) error {
	set, known := c.setters()[key]
	if !known {
		return fmt.Errorf("config: unknown key %q", key)
	}
	if err := set(value); err != nil {
		return fmt.Errorf("config: %s: %w", key, err)
	}
	return nil
}

func (c *Config) setters() map[string]func(string) error {
	return map[string]func(string) error{
		"db_path":                    str(&c.DBPath),
		"clip_dir":                   str(&c.ClipDir),
		"log_dir":                    str(&c.LogDir),
		"capture_device":             str(&c.CaptureDevice),
		"capture_channel":            integer(&c.CaptureChannel),
		"expected_capture_gain":      str(&c.ExpectedCaptureGain),
		"mixer_card":                 str(&c.MixerCard),
		"mixer_control":              str(&c.MixerControl),
		"stuck_limit":                duration(&c.StuckLimit),
		"sensitivity_dbfs":           float(&c.SensitivityDBFS),
		"sensitivity_source":         str(&c.SensitivitySource),
		"sensitivity_uncertainty_db": float(&c.SensitivityUncertaintyDB),
		"sensitivity_measured_on":    str(&c.SensitivityMeasuredOn),
		"sensitivity_reference":      str(&c.SensitivityReference),
		"calibration_file":           str(&c.CalibrationFile),
		"baseline_window":            duration(&c.BaselineWindow),
		"baseline_percentile":        float(&c.BaselinePercentile),
		"threshold_db":               float(&c.ThresholdDB),
		"min_duration_ms":            units(&c.MinDuration, time.Millisecond),
		"hangover_ms":                units(&c.Hangover, time.Millisecond),
		"cooldown_ms":                units(&c.Cooldown, time.Millisecond),
		"max_event_s":                units(&c.MaxEvent, time.Second),
		"min_baseline_s":             units(&c.MinBaseline, time.Second),
		"pre_roll_s":                 units(&c.PreRoll, time.Second),
		"post_roll_s":                units(&c.PostRoll, time.Second),
		"clip_lowpass_hz":            float(&c.ClipLowpassHz),
		"heartbeat_url":              str(&c.HeartbeatURL),
		"heartbeat_interval":         duration(&c.HeartbeatInterval),
		"heartbeat_stall_factor":     float(&c.HeartbeatStallFactor),
		"batch_interval":             duration(&c.BatchInterval),
		"disk_min_free_mb":           integer64(&c.DiskMinFreeMB),
		"disk_warn_free_mb":          integer64(&c.DiskWarnFreeMB),
		"retention_days":             integer(&c.RetentionDays),
		"http_addr":                  str(&c.HTTPAddr),
		"auth_mode":                  str(&c.AuthMode),
		"auth_header":                str(&c.AuthHeader),
		"auth_name_header":           str(&c.AuthNameHeader),
		"quiet_start":                clock(&c.Quiet.Start),
		"quiet_end":                  clock(&c.Quiet.End),
		"recording_pause":            dailySpans(&c.RecordingPause),
		"log_file_mb":                integer64(&c.LogFileMB),
		"log_files":                  integer(&c.LogFiles),
		"camera_host":                str(&c.CameraHost),
		"camera_port":                integer(&c.CameraPort),
		"camera_rtsp_path":           str(&c.CameraRTSPPath),
		"camera_rtsp_path_main":      str(&c.CameraRTSPPathMain),
		"camera_credentials_file":    str(&c.CameraCredentialsFile),
		"video_dir":                  str(&c.VideoDir),
		"video_ring_minutes":         integer(&c.VideoRingMinutes),
		"video_main_on_event":        boolean(&c.VideoMainOnEvent),
		"video_segment_seconds":      integer(&c.VideoSegmentSeconds),
		"camera_audio":               boolean(&c.CameraAudio),
		"ntp_server":                 str(&c.NTPServer),
	}
}

// boolean reads true or false and nothing else, so "yes" cannot be read as
// false.
func boolean(p *bool) func(string) error {
	return func(v string) error {
		switch v {
		case "true":
			*p = true
		case "false":
			*p = false
		default:
			return fmt.Errorf("%q is not true or false", v)
		}
		return nil
	}
}

func str(p *string) func(string) error {
	return func(v string) error { *p = v; return nil }
}

func integer(p *int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", v)
		}
		*p = n
		return nil
	}
}

func integer64(p *int64) func(string) error {
	return func(v string) error {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", v)
		}
		*p = n
		return nil
	}
}

func float(p *float64) func(string) error {
	return func(v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("%q is not a number", v)
		}
		*p = f
		return nil
	}
}

func duration(p *time.Duration) func(string) error {
	return func(v string) error {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%q is not a duration such as 30s or 5m", v)
		}
		*p = d
		return nil
	}
}

// clock reads a 24-hour time of day as HH:MM and stores it as the offset from
// midnight.
func clock(p *time.Duration) func(string) error {
	return func(v string) error {
		bad := fmt.Errorf("%q is not a time of day such as 22:00", v)
		hh, mm, ok := strings.Cut(v, ":")
		if !ok || len(hh) != 2 || len(mm) != 2 || !digits(hh) || !digits(mm) {
			return bad
		}
		h, err := strconv.Atoi(hh)
		if err != nil || h < 0 || h > 23 {
			return bad
		}
		m, err := strconv.Atoi(mm)
		if err != nil || m < 0 || m > 59 {
			return bad
		}
		*p = time.Duration(h)*time.Hour + time.Duration(m)*time.Minute
		return nil
	}
}

// dailySpan reads a daily span as "HH:MM-HH:MM", or an empty value for none.
// A span may wrap past midnight. A span that starts and ends at the same
// minute is refused rather than read as off: the only way to say off is to
// leave the value empty. Nothing is written unless the whole value parses.
func dailySpan(p *QuietHours) func(string) error {
	return func(v string) error {
		if v == "" {
			*p = QuietHours{}
			return nil
		}
		from, to, ok := strings.Cut(v, "-")
		if !ok {
			return fmt.Errorf("%q is not a span such as 22:00-07:00; leave it empty for no pause", v)
		}
		var start, end time.Duration
		if err := clock(&start)(from); err != nil {
			return err
		}
		if err := clock(&end)(to); err != nil {
			return err
		}
		if start == end {
			return fmt.Errorf("%q starts and ends at the same time; leave it empty for no pause", v)
		}
		*p = QuietHours{Start: start, End: end}
		return nil
	}
}

// dailySpans reads one or more spans separated by commas, such as
// "22:00-07:00, 12:00-13:00". Empty means no pause. No two windows may
// overlap or touch: Window relies on that to say which window is active, and
// two windows that meet would swallow the second "began" row the health log
// expects when the pause is really still running. Nothing is written unless
// every span parses and no pair overlaps or touches.
func dailySpans(p *PauseWindows) func(string) error {
	return func(v string) error {
		if strings.TrimSpace(v) == "" {
			*p = nil
			return nil
		}
		var out PauseWindows
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				return fmt.Errorf("%q has an empty span; write 22:00-07:00, 12:00-13:00, or leave it empty for no pause", v)
			}
			var q QuietHours
			if err := dailySpan(&q)(part); err != nil {
				return err
			}
			out = append(out, q)
		}
		for i := range out {
			for j := i + 1; j < len(out); j++ {
				if windowsOverlapOrTouch(out[i], out[j]) {
					return fmt.Errorf("%q: the windows %s and %s overlap or touch; write them as one window",
						v, windowString(out[i]), windowString(out[j]))
				}
			}
		}
		*p = out
		return nil
	}
}

// windowsOverlapOrTouch reports whether two daily windows share any part of
// the clock, or meet exactly at a boundary. A wrapping window is checked as
// its two pieces either side of midnight, but a shared boundary is checked
// on the windows' own Start and End: midnight only splits a window in two,
// it is not itself a boundary between different windows.
func windowsOverlapOrTouch(a, b QuietHours) bool {
	if a.End == b.Start || b.End == a.Start {
		return true
	}
	for _, pa := range dailyPieces(a) {
		for _, pb := range dailyPieces(b) {
			if pa[0] < pb[1] && pb[0] < pa[1] {
				return true
			}
		}
	}
	return false
}

// dailyPieces splits a window into one or two half-open intervals on the
// 0-24h clock, so a window that wraps past midnight can be compared to
// another window with ordinary interval arithmetic.
func dailyPieces(q QuietHours) [][2]time.Duration {
	if q.Start < q.End {
		return [][2]time.Duration{{q.Start, q.End}}
	}
	return [][2]time.Duration{{q.Start, 24 * time.Hour}, {0, q.End}}
}

// isDay reports whether s is a calendar date as YYYY-MM-DD. It is the date
// a sensitivity was measured on, so a date that does not exist, such as
// 2026-02-30, must not pass.
func isDay(s string) bool {
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Format("2006-01-02") == s
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// units reads a whole number of unit, for keys such as hangover_ms.
func units(p *time.Duration, unit time.Duration) func(string) error {
	return func(v string) error {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", v)
		}
		*p = time.Duration(n) * unit
		return nil
	}
}

// isLoopback reports whether a listen address host reaches only this machine.
// An empty host, as in ":8080", is not loopback: net.Listen then binds every
// interface, so anyone on the network reaches the port.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// LoopbackHTTPAddr reports whether http_addr reaches only this machine. An
// empty http_addr switches the server off, which also reaches nobody.
func (c Config) LoopbackHTTPAddr() bool {
	if c.HTTPAddr == "" {
		return true
	}
	host, _, err := net.SplitHostPort(c.HTTPAddr)
	if err != nil {
		return false
	}
	return isLoopback(host)
}

// plainHost reports whether s is a bare host name or IPv4 address: letters,
// digits, dots, and hyphens, and nothing else. An empty host switches video
// off, which is allowed.
//
// The dashboard may write camera_host, so this check is what stands between
// a web form and a stream URL of somebody else's choosing. An IPv6 literal
// is not accepted; a camera on one needs a name.
func plainHost(s string) bool {
	for _, r := range s {
		if !hostRune(r) {
			return false
		}
	}
	return true
}

func hostRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '.' || r == '-':
		return true
	}
	return false
}

// pathProblem says what is wrong with an RTSP path, or returns the empty
// string when the path is one ffmpeg can use: one or more segments
// separated by /, optionally followed by ? and a query. There is no leading
// slash, because the URL builder adds one. An empty path means "try the
// known cameras in turn", which is allowed.
//
// A query is part of a real path: an Amcrest or Dahua camera serves
// cam/realmonitor?channel=1&subtype=1. Refusing it, as the earlier
// one-plain-segment rule did, left those cameras with no way to be set up
// at all.
//
// The dashboard may write both RTSP path keys, so the same reasoning as
// plainHost applies. What is refused here is what could turn the path into
// a different URL, or into a URL the shell or ffmpeg reads as two things.
// The caller puts the config key in front of the message.
func pathProblem(s string) string {
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "://"):
		return "must be the path only, with no scheme: the rtsp:// part is built from camera_host and camera_port"
	case strings.Contains(s, "@"):
		return "must not hold @, which would replace the login in the stream URL"
	case strings.Contains(s, `\`):
		return "must not hold a backslash"
	case strings.Contains(s, "#"):
		return "must not hold #, which starts a URL fragment"
	}
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			return "must not hold a space, a tab, or a line break"
		case r < 0x20 || r == 0x7f:
			return "must not hold a control character"
		case r > 0x7e:
			return "must hold printable ASCII only"
		}
	}
	// Only the part before the query is a path. A query may hold anything
	// the checks above allow, / and = and & included.
	path, _, _ := strings.Cut(s, "?")
	for _, seg := range strings.Split(path, "/") {
		switch seg {
		case "":
			return "must not hold an empty path segment: no slash at the start, and no // in the middle"
		case ".", "..":
			return `must not hold "." or ".." as a path segment; they climb the path rather than name a stream`
		}
	}
	return ""
}

// pathExamples is the tail of both path messages: what a right one looks
// like, for a Reolink and for the owner's Amcrest.
const pathExamples = ". It is what comes after the port, such as Preview_01_sub, " +
	"or cam/realmonitor?channel=1&subtype=1 on an Amcrest or Dahua"

// Validate checks every setting and returns the first problem. Each message
// names the config key.
func (c Config) Validate() error {
	subPath, mainPath := pathProblem(c.CameraRTSPPath), pathProblem(c.CameraRTSPPathMain)
	checks := []struct {
		bad bool
		msg string
	}{
		{c.ExpectedCaptureGain == "", "expected_capture_gain is not set; set it to the raw capture value that amixer shows, or to none"},
		{c.DBPath == "", "db_path is empty"},
		{c.ClipDir == "", "clip_dir is empty"},
		{c.CaptureChannel != 0 && c.CaptureChannel != 1, "capture_channel must be 0 or 1"},
		{c.StuckLimit < time.Second, "stuck_limit must be at least 1s"},
		{c.SensitivityDBFS >= 0 || c.SensitivityDBFS < -60, "sensitivity_dbfs must be below 0 and at least -60"},
		{c.SensitivitySource != SensitivityDatasheet && c.SensitivitySource != SensitivityMeasured,
			"sensitivity_source must be datasheet or measured"},
		{c.SensitivityUncertaintyDB < 0 || c.SensitivityUncertaintyDB > 10,
			"sensitivity_uncertainty_db must be between 0 and 10; it is the plus or minus, not the range"},
		// The two states must not blur. A measured figure without a day is
		// not a measurement anybody can check, and a datasheet figure that
		// carries a day and a reference claims a measurement that never
		// happened.
		{c.SensitivitySource == SensitivityMeasured && c.SensitivityMeasuredOn == "",
			"sensitivity_measured_on is empty; sensitivity_source = measured needs the day it was measured, as YYYY-MM-DD"},
		{c.SensitivitySource == SensitivityMeasured && !isDay(c.SensitivityMeasuredOn),
			"sensitivity_measured_on must be a date such as 2026-10-03"},
		{c.SensitivitySource == SensitivityDatasheet && c.SensitivityMeasuredOn != "",
			"sensitivity_measured_on must be empty while sensitivity_source = datasheet; the datasheet figure was not measured on a day"},
		{c.SensitivitySource == SensitivityDatasheet && c.SensitivityReference != "",
			"sensitivity_reference must be empty while sensitivity_source = datasheet; the datasheet figure was not measured against anything"},
		{c.BaselineWindow < 10*time.Second, "baseline_window must be at least 10s"},
		{c.BaselinePercentile <= 0 || c.BaselinePercentile > 100, "baseline_percentile must be above 0 and at most 100"},
		{c.ThresholdDB <= 0, "threshold_db must be above 0"},
		{c.MinDuration < 0, "min_duration_ms must not be negative"},
		{c.Hangover < 0, "hangover_ms must not be negative"},
		{c.Cooldown < 0, "cooldown_ms must not be negative"},
		{c.MaxEvent <= 0 || c.MaxEvent < c.MinDuration, "max_event_s must be at least min_duration_ms"},
		{c.MinBaseline < time.Second, "min_baseline_s must be at least 1"},
		{c.PreRoll < 0 || c.PreRoll > clip.MaxPreRoll,
			fmt.Sprintf("pre_roll_s must be between 0 and %d (the audio buffer holds %d s)",
				int(clip.MaxPreRoll.Seconds()), int(clip.BufferLength.Seconds()))},
		{c.PostRoll < 0 || c.PostRoll > 60*time.Second, "post_roll_s must be between 0 and 60"},
		// Only these cutoffs give a whole-number decimation from 48 kHz.
		// Raising it is a privacy decision, so the message says so rather
		// than only listing numbers.
		{!slices.Contains(dsp.ClipLowpassChoices, c.ClipLowpassHz),
			"clip_lowpass_hz must be one of " + dsp.ClipLowpassChoicesText() +
				"; it is the clip filter cutoff, and raising it lets clips hold more speech"},
		{c.HeartbeatInterval <= 0, "heartbeat_interval must be above 0"},
		{c.HeartbeatStallFactor < 1, "heartbeat_stall_factor must be at least 1"},
		{c.BatchInterval < 10*time.Second || c.BatchInterval > 30*time.Second, "batch_interval must be between 10s and 30s"},
		{c.DiskMinFreeMB < 0, "disk_min_free_mb must not be negative"},
		{c.DiskWarnFreeMB < c.DiskMinFreeMB, "disk_warn_free_mb must be at least disk_min_free_mb"},
		{c.RetentionDays < 0 || c.RetentionDays > 3650,
			"retention_days must be between 0 and 3650; 0 keeps every recording forever"},
		{c.AuthMode != AuthTailscale && c.AuthMode != AuthTrustedHeader && c.AuthMode != AuthNone,
			"auth_mode must be tailscale, trusted_header, or none"},
		{c.AuthHeader == "" && c.AuthMode != AuthNone, "auth_header is empty; it names the header that carries the login"},
		{c.Quiet.Start == c.Quiet.End, "quiet_end must differ from quiet_start"},
		{c.LogFileMB < 1 || c.LogFileMB > 1024, "log_file_mb must be between 1 and 1024"},
		{c.LogFiles < 1 || c.LogFiles > 100, "log_files must be between 1 and 100"},
		// The camera keys are checked even with video off, so a typo is
		// found before the day the camera arrives.
		{!plainHost(c.CameraHost),
			"camera_host is a host name or address only, with no scheme, port, login, slash, or space; " +
				"the login comes from CAMERA_USER and CAMERA_PASS or camera_credentials_file"},
		{c.CameraPort < 1 || c.CameraPort > 65535, "camera_port must be between 1 and 65535"},
		{subPath != "", "camera_rtsp_path " + subPath + pathExamples},
		{mainPath != "", "camera_rtsp_path_main " + mainPath + pathExamples},
		{c.VideoEnabled() && c.VideoDir == "", "video_dir is empty"},
		{c.VideoRingMinutes < 1 || c.VideoRingMinutes > 24*60, "video_ring_minutes must be between 1 and 1440"},
		{c.VideoSegmentSeconds < 1 || c.VideoSegmentSeconds > 60, "video_segment_seconds must be between 1 and 60"},
		{c.VideoEnabled() && c.NTPServer == "", "ntp_server is empty; the camera clock check needs one"},
	}
	for _, ch := range checks {
		if ch.bad {
			return errors.New("config: " + ch.msg)
		}
	}
	if err := audio.ValidateDevice(c.CaptureDevice); err != nil {
		return fmt.Errorf("config: capture_device: %w", err)
	}
	if c.HTTPAddr != "" {
		if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
			return fmt.Errorf("config: http_addr %q is not host:port, for example 127.0.0.1:8080", c.HTTPAddr)
		}
		// Nothing authenticates the caller in this mode, so the listener must
		// stay on the local machine.
		if c.AuthMode == AuthNone && !c.LoopbackHTTPAddr() {
			return fmt.Errorf("config: auth_mode = none has no authentication, so http_addr %q must be loopback "+
				"(127.0.0.1, ::1, or localhost)", c.HTTPAddr)
		}
	}
	if c.HeartbeatURL != "" {
		u, err := url.Parse(c.HeartbeatURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("config: heartbeat_url %q is not an http or https URL", c.HeartbeatURL)
		}
	}
	return nil
}
