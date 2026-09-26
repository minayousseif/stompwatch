package video

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// KnownPath is one camera family's pair of RTSP paths: the sub-stream the
// ring records, and the main stream that goes with it. The pair is stated
// here once, so nothing has to guess one path from the other.
type KnownPath struct {
	Camera string // the camera family, as the owner would name it
	Sub    string // the sub-stream path
	Main   string // the main-stream path of the same camera
}

// KnownPaths are the sub-stream paths tried in order when camera_rtsp_path
// is empty (SPEC.md section 7 and decision 19). The two Reolink paths come
// first, because those are the ones that were already in use.
//
// The list is a convenience and nothing more. A camera that is not on it is
// set up by hand: camera_rtsp_path takes any path the camera serves, a query
// included, and camera_rtsp_path_main takes its main stream.
var KnownPaths = []KnownPath{
	{"Reolink, current firmware", "Preview_01_sub", "Preview_01_main"},
	{"Reolink, older firmware", "h264Preview_01_sub", "h264Preview_01_main"},
	{"Amcrest and Dahua", "cam/realmonitor?channel=1&subtype=1", "cam/realmonitor?channel=1&subtype=0"},
	{"Hikvision", "Streaming/Channels/102", "Streaming/Channels/101"},
	{"TP-Link Tapo", "stream2", "stream1"},
	{"Foscam", "videoSub", "videoMain"},
	{"Ubiquiti", "s1", "s0"},
}

// masked is what stands in for the password wherever it is printed.
const masked = "***"

// Stream names one RTSP stream of the camera.
type Stream struct {
	Host  string
	Port  int
	Path  string
	Creds Credentials
}

// URL returns the full stream URL with the password in it. It is for the
// ffmpeg command line and nothing else. Every printed form goes through
// String or Redact.
func (s Stream) URL() string { return s.build(s.Creds.Pass) }

// String returns the URL with the password masked. fmt prints this, so a
// Stream in a log line or an error is safe by construction.
func (s Stream) String() string {
	if s.Creds.Pass == "" {
		return s.build("")
	}
	return s.build(masked)
}

// GoString keeps %#v from printing the struct fields.
func (s Stream) GoString() string { return s.String() }

// Format keeps every fmt verb, %+v included, on the masked form.
func (s Stream) Format(f fmt.State, _ rune) { fmt.Fprint(f, s.String()) }

// LogValue keeps a Stream in a log line on the masked form. slog's JSON
// handler would otherwise serialize the struct by reflection, fields and
// all, and String would never be asked.
func (s Stream) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON and MarshalText do the same for JSON and text encoders.
func (s Stream) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }
func (s Stream) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// build returns the URL with pass as the password: the real one escaped
// for a URL, the mask as it is, or none.
func (s Stream) build(pass string) string {
	// A path may carry a query: an Amcrest or Dahua camera wants
	// cam/realmonitor?channel=1&subtype=1. The query goes in RawQuery, which
	// url.URL writes out as it stands. Left in Path it would be escaped, the
	// ? would reach the camera as %3F, and the camera would answer 404.
	path, query, hasQuery := strings.Cut(s.Path, "?")
	u := url.URL{
		Scheme:     "rtsp",
		Host:       net.JoinHostPort(s.Host, strconv.Itoa(s.Port)),
		Path:       "/" + strings.TrimPrefix(path, "/"),
		RawQuery:   query,
		ForceQuery: hasQuery,
	}
	if s.Creds.User == "" && pass == "" {
		return u.String()
	}
	u.User = url.User(s.Creds.User)
	if pass == "" {
		return u.String()
	}
	if pass != masked {
		pass = escapePassword(pass)
	}
	head := "rtsp://" + u.User.String()
	return head + ":" + pass + strings.TrimPrefix(u.String(), head)
}

// escapePassword escapes a password the way a URL carries it.
func escapePassword(pass string) string {
	return strings.TrimPrefix(url.UserPassword("u", pass).String(), "u:")
}

// Redact replaces the password in text with ***, in both the plain form
// and the escaped form a URL carries. ffmpeg repeats the URL it was given
// in its own error output, so everything read from ffmpeg goes through
// here before it is logged or stored.
func (s Stream) Redact(text string) string {
	pass := s.Creds.Pass
	if pass == "" {
		return text
	}
	if escaped := escapePassword(pass); escaped != pass {
		text = strings.ReplaceAll(text, escaped, masked)
	}
	return strings.ReplaceAll(text, pass, masked)
}

// Errorf is fmt.Errorf with the password redacted from the result. An
// error given as an argument is kept for errors.Is, unless its own text
// holds the password: then it is dropped rather than left reachable
// through Unwrap.
func (s Stream) Errorf(format string, args ...any) error {
	msg := s.Redact(fmt.Errorf(format, args...).Error())
	var inner []error
	for _, a := range args {
		if e, ok := a.(error); ok && s.Redact(e.Error()) == e.Error() {
			inner = append(inner, e)
		}
	}
	return &redactedError{msg: msg, errs: inner}
}

type redactedError struct {
	msg  string
	errs []error
}

func (e *redactedError) Error() string   { return e.msg }
func (e *redactedError) Unwrap() []error { return e.errs }

// CandidatePaths returns the sub-stream paths to try: the configured one, or
// every known camera's sub path, in order, when none is configured.
func CandidatePaths(configured string) []string {
	if configured != "" {
		return []string{configured}
	}
	paths := make([]string, 0, len(KnownPaths))
	for _, k := range KnownPaths {
		paths = append(paths, k.Sub)
	}
	return paths
}

// MainPath returns the main-stream path that goes with a sub-stream path,
// read from KnownPaths. It is empty when the sub path is not a known one,
// and then camera_rtsp_path_main has to be set.
func MainPath(sub string) string {
	for _, k := range KnownPaths {
		if k.Sub == sub {
			return k.Main
		}
	}
	return ""
}
