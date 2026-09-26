package web

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/video"
)

// The camera test runs ffmpeg. These tests run without ffmpeg and without a
// camera, so the test binary stands in for ffmpeg: when FAKE_FFMPEG is set,
// TestMain runs fakeFFmpeg instead of the tests. It is the same trick
// internal/video uses, and it exercises the real process plumbing rather
// than a stub of it.
//
//	FAKE_FFMPEG      run as ffmpeg rather than as the tests
//	FAKE_TEST_OK    a URL that contains this text succeeds; others fail
//	FAKE_TEST_INFO  stderr for a success, in ffmpeg's stream-info form
//	FAKE_STDERR      what a failure prints after the URL
//	FAKE_SLEEP_MS    how long to hang before answering
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_FFMPEG") != "" {
		os.Exit(fakeFFmpeg())
	}
	os.Exit(m.Run())
}

func fakeFFmpeg() int {
	args := os.Args[1:]
	url := ""
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			url = args[i+1]
		}
	}
	if ms := os.Getenv("FAKE_SLEEP_MS"); ms != "" {
		sleepMS(ms)
	}
	if ok := os.Getenv("FAKE_TEST_OK"); ok != "" && strings.Contains(url, ok) {
		fmt.Fprint(os.Stderr, os.Getenv("FAKE_TEST_INFO"))
		return 0
	}
	// Real ffmpeg repeats the URL it was given, password and all. That is
	// the whole reason every detail goes through Stream.Redact.
	fmt.Fprintf(os.Stderr, "%s: %s\n", url, os.Getenv("FAKE_STDERR"))
	return 1
}

// fakeFFmpegCommand returns the command and environment that run the test
// binary as ffmpeg.
func fakeFFmpegCommand(t *testing.T, env map[string]string) (video.Command, []string) {
	t.Helper()
	out := []string{"FAKE_FFMPEG=1"}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return video.Command{os.Args[0]}, out
}

// sleepMS hangs for the given number of milliseconds.
func sleepMS(ms string) {
	n, err := strconv.Atoi(ms)
	if err != nil {
		return
	}
	time.Sleep(time.Duration(n) * time.Millisecond)
}
