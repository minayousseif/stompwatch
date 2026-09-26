package video

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// The password in this file has every character that an RTSP URL escapes,
// so the tests cover both the plain and the escaped form.
const testPass = "p@ss:w/rd#1 ?"

func testStream(path string) Stream {
	return Stream{Host: "cam.local", Port: 554, Path: path, Creds: Credentials{User: "admin", Pass: testPass}}
}

// The only place the password may appear is the URL handed to ffmpeg.
func TestStreamURLCarriesThePasswordEscaped(t *testing.T) {
	u := testStream("Preview_01_sub").URL()
	want := "rtsp://admin:p%40ss%3Aw%2Frd%231%20%3F@cam.local:554/Preview_01_sub"
	if u != want {
		t.Fatalf("URL() = %q, want %q", u, want)
	}
}

// SPEC.md section 3 and section 7: a password must never reach a log line, an error, an
// HTTP response, or the health log. String is what all of those print.
func TestStreamStringMasksThePassword(t *testing.T) {
	s := testStream("Preview_01_sub")
	got := s.String()
	if got != "rtsp://admin:***@cam.local:554/Preview_01_sub" {
		t.Fatalf("String() = %q", got)
	}
	for _, form := range []string{fmt.Sprint(s), fmt.Sprintf("%v", s), fmt.Sprintf("%s", s), fmt.Sprintf("%+v", s)} {
		if strings.Contains(form, testPass) || strings.Contains(form, "p%40ss") {
			t.Errorf("printed form %q holds the password", form)
		}
	}
	if !strings.Contains(fmt.Sprintf("%+v", s), "***") {
		t.Errorf("%%+v does not mask the password")
	}
}

// The collector logs JSON, and slog's JSON handler serializes a struct
// through reflection, not String. The run test caught the password in a
// log line this way. A Stream and a Credentials must mask themselves for
// slog and for every JSON or text encoder, not only for fmt.
func TestStreamAndCredentialsMaskThemselvesForSlogAndJSON(t *testing.T) {
	s := testStream("Preview_01_sub")
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	log.Info("video is on", "camera", s, "creds", s.Creds)
	log.Info("as a group", "cfg", struct {
		S Stream
		C Credentials
	}{s, s.Creds})
	b, err := json.Marshal(map[string]any{"stream": s, "creds": s.Creds, "list": []Stream{s}})
	if err != nil {
		t.Fatal(err)
	}
	text := buf.String() + string(b)
	if strings.Contains(text, testPass) || strings.Contains(text, escapePassword(testPass)) {
		t.Fatalf("the password reached a log line or JSON:\n%s", text)
	}
	if !strings.Contains(buf.String(), `"camera":"rtsp://admin:***@cam.local:554/Preview_01_sub"`) {
		t.Errorf("the log line does not carry the masked URL:\n%s", buf.String())
	}
	if !strings.Contains(string(b), `"creds":"admin:***"`) {
		t.Errorf("JSON does not carry the masked login: %s", b)
	}
}

// ffmpeg echoes the URL it was given in its own error output, so an error
// built from that output carries the password unless it is redacted. The
// escaped form is what ffmpeg prints; the plain form is what a careless
// caller prints. Both must go.
func TestRedactRemovesBothFormsOfThePassword(t *testing.T) {
	s := testStream("Preview_01_sub")
	in := "rtsp://admin:" + testPass + "@cam.local:554/x: Connection refused; " +
		s.URL() + ": Connection refused; also " + testPass
	got := s.Redact(in)
	if strings.Contains(got, testPass) || strings.Contains(got, "p%40ss") {
		t.Fatalf("Redact(%q) = %q still holds the password", in, got)
	}
	if strings.Count(got, "***") != 3 {
		t.Errorf("Redact(%q) = %q; want each form replaced by ***", in, got)
	}
	if got := s.Redact("nothing here"); got != "nothing here" {
		t.Errorf("Redact changed a line with no password: %q", got)
	}
	// An empty password must not turn Redact into a replace-all of "".
	empty := Stream{Host: "cam", Port: 554, Path: "p"}
	if got := empty.Redact("abc"); got != "abc" {
		t.Errorf("Redact with an empty password gave %q", got)
	}
}

// An error made from ffmpeg's output must come out redacted whatever the
// caller does with it.
func TestStreamErrorfRedacts(t *testing.T) {
	s := testStream("Preview_01_sub")
	base := errors.New("boom")
	err := s.Errorf("connecting to %s: %s: %w", s.URL(), "stderr says "+testPass, base)
	if strings.Contains(err.Error(), testPass) || strings.Contains(err.Error(), "p%40ss") {
		t.Fatalf("Errorf leaked the password: %q", err)
	}
	if !errors.Is(err, base) {
		t.Error("Errorf lost the wrapped error")
	}
	if !strings.Contains(err.Error(), "rtsp://admin:***@cam.local:554/Preview_01_sub") {
		t.Errorf("Errorf did not keep the masked URL: %q", err)
	}
}

// An Amcrest or Dahua camera wants a query in the path. ffmpeg has to get
// the ?, the & and the = as they stand: a camera that is sent %3F answers
// 404. The URL is written out here in full, because that exact string is
// what the camera has to receive.
func TestStreamURLKeepsAQueryUnescaped(t *testing.T) {
	s := Stream{
		Host: "192.0.2.47", Port: 554,
		Path:  "cam/realmonitor?channel=1&subtype=1",
		Creds: Credentials{User: "admin", Pass: "PASS"},
	}
	want := "rtsp://admin:PASS@192.0.2.47:554/cam/realmonitor?channel=1&subtype=1"
	if got := s.URL(); got != want {
		t.Fatalf("URL() = %q, want %q", got, want)
	}
	if got := s.String(); got != "rtsp://admin:***@192.0.2.47:554/cam/realmonitor?channel=1&subtype=1" {
		t.Fatalf("String() = %q", got)
	}
}

// A query must not carry the password past the mask. The masked form keeps
// the query and loses the password, in every printed form.
func TestStreamStringMasksThePasswordWhenThePathHasAQuery(t *testing.T) {
	s := testStream("cam/realmonitor?channel=1&subtype=1")
	if got := s.String(); got != "rtsp://admin:***@cam.local:554/cam/realmonitor?channel=1&subtype=1" {
		t.Fatalf("String() = %q", got)
	}
	for _, form := range []string{fmt.Sprint(s), fmt.Sprintf("%v", s), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s)} {
		if strings.Contains(form, testPass) || strings.Contains(form, "p%40ss") {
			t.Errorf("printed form %q holds the password", form)
		}
	}
}

// ffmpeg repeats the URL it was given, query and all, so Redact has to
// reach the password in that form too.
func TestRedactWorksOnAURLWithAQuery(t *testing.T) {
	s := testStream("cam/realmonitor?channel=1&subtype=1")
	in := s.URL() + ": method OPTIONS failed: 404 Not Found"
	got := s.Redact(in)
	if strings.Contains(got, testPass) || strings.Contains(got, "p%40ss") {
		t.Fatalf("Redact(%q) = %q still holds the password", in, got)
	}
	if got != "rtsp://admin:***@cam.local:554/cam/realmonitor?channel=1&subtype=1: method OPTIONS failed: 404 Not Found" {
		t.Fatalf("Redact gave %q", got)
	}
}

// The default list covers the cameras the owner is likely to have, in this
// order: the two that already worked stay first. Each sub path is paired
// with its own main path in the table, rather than being guessed.
func TestCandidatePathsCoverTheKnownCameras(t *testing.T) {
	want := []string{
		"Preview_01_sub",
		"h264Preview_01_sub",
		"cam/realmonitor?channel=1&subtype=1",
		"Streaming/Channels/102",
		"stream2",
		"videoSub",
		"s1",
	}
	got := CandidatePaths("")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("CandidatePaths(\"\") =\n%v\nwant\n%v", got, want)
	}
	if got := CandidatePaths("cam/realmonitor?channel=1&subtype=1"); len(got) != 1 ||
		got[0] != "cam/realmonitor?channel=1&subtype=1" {
		t.Fatalf("a configured path did not replace the list: %v", got)
	}
}

// The main path is read from the table, not derived by string surgery. An
// Amcrest main path differs from its sub path in the subtype, not in a
// _sub suffix, so the old rule gave nothing for it.
func TestMainPathComesFromTheTable(t *testing.T) {
	for sub, main := range map[string]string{
		"Preview_01_sub":                      "Preview_01_main",
		"h264Preview_01_sub":                  "h264Preview_01_main",
		"cam/realmonitor?channel=1&subtype=1": "cam/realmonitor?channel=1&subtype=0",
		"Streaming/Channels/102":              "Streaming/Channels/101",
		"stream2":                             "stream1",
		"videoSub":                            "videoMain",
		"s1":                                  "s0",
		// Not in the table: camera_rtsp_path_main has to be set by hand.
		"live/ch0":       "",
		"Preview_02_sub": "",
	} {
		if got := MainPath(sub); got != main {
			t.Errorf("MainPath(%q) = %q, want %q", sub, got, main)
		}
	}
}
