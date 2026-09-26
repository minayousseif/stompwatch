package video

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests run without a camera and, on this machine, without ffmpeg. The
// test binary stands in for ffmpeg: when FAKE_FFMPEG is set, TestMain runs
// fakeFFmpeg instead of the tests, and the environment says what it does.
//
//	FAKE_ARGV        file the argument list is appended to, one run per block
//	FAKE_MODE        segments | test | concat | version
//	FAKE_STDERR      text printed to stderr before exit
//	FAKE_EXIT        exit status
//
// For segments:
//
//	FAKE_START       Unix seconds of the first segment's name
//	FAKE_STEP        seconds between segment names
//	FAKE_COUNT       segments to write; 0 means keep writing until killed
//	FAKE_INTERVAL_MS real time between segments
//	FAKE_HANG        write one segment, then write nothing more
//
// For test:
//
//	FAKE_TEST_OK    a URL that contains this text succeeds; others fail
//	FAKE_TEST_INFO  stderr for a success, in ffmpeg's stream-info form
//
// For concat:
//
//	FAKE_OUT_TIME_US what the progress output reports as the duration
//	FAKE_INSPECT_INFO stderr for the run that reads the written clip back,
//	                  in ffmpeg's stream-info form
//	FAKE_INSPECT_EXIT exit status of that run
//
// For version:
//
//	FAKE_STDOUT      the version banner, printed to stdout as ffmpeg does
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_FFMPEG") != "" {
		os.Exit(fakeFFmpeg())
	}
	os.Exit(m.Run())
}

func fakeFFmpeg() int {
	args := os.Args[1:]
	if path := os.Getenv("FAKE_ARGV"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 99
		}
		fmt.Fprintln(f, strings.Join(args, "\n")+"\n")
		f.Close()
	}
	exit, _ := strconv.Atoi(os.Getenv("FAKE_EXIT"))
	switch os.Getenv("FAKE_MODE") {
	case "segments":
		return fakeSegments(args, exit)
	case "test":
		url := args[len(args)-1]
		for i, a := range args {
			if a == "-i" && i+1 < len(args) {
				url = args[i+1]
			}
		}
		if ok := os.Getenv("FAKE_TEST_OK"); ok != "" && strings.Contains(url, ok) {
			fmt.Fprint(os.Stderr, os.Getenv("FAKE_TEST_INFO"))
			return 0
		}
		// Real ffmpeg names the URL it was given, password and all.
		fmt.Fprintf(os.Stderr, "%s: %s\n", url, os.Getenv("FAKE_STDERR"))
		return 1
	case "concat":
		// The clip writer reads the file it wrote back, to find out whether
		// the audio track is really there. That run asks for one frame; the
		// join does not.
		if slices.Contains(args, "-frames:v") {
			fmt.Fprint(os.Stderr, os.Getenv("FAKE_INSPECT_INFO"))
			code, _ := strconv.Atoi(os.Getenv("FAKE_INSPECT_EXIT"))
			return code
		}
		return fakeConcat(args, exit)
	case "version":
		// Real ffmpeg prints its version banner on stdout.
		fmt.Fprint(os.Stdout, os.Getenv("FAKE_STDOUT"))
		return exit
	}
	fmt.Fprint(os.Stderr, os.Getenv("FAKE_STDERR"))
	return exit
}

func fakeSegments(args []string, exit int) int {
	pattern := args[len(args)-1]
	dir := filepath.Dir(pattern)
	start, _ := strconv.ParseInt(os.Getenv("FAKE_START"), 10, 64)
	step, _ := strconv.Atoi(os.Getenv("FAKE_STEP"))
	count, _ := strconv.Atoi(os.Getenv("FAKE_COUNT"))
	interval, _ := strconv.Atoi(os.Getenv("FAKE_INTERVAL_MS"))
	if step == 0 {
		step = 10
	}
	var current *os.File
	for i := 0; count == 0 || i < count; i++ {
		if current != nil {
			current.Close()
		}
		name := SegmentName(time.Unix(start+int64(i*step), 0).UTC())
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 98
		}
		current = f
		fmt.Fprintf(f, "segment %d\n", i)
		if os.Getenv("FAKE_HANG") != "" {
			time.Sleep(time.Hour)
		}
		time.Sleep(time.Duration(interval) * time.Millisecond)
	}
	fmt.Fprint(os.Stderr, os.Getenv("FAKE_STDERR"))
	return exit
}

func fakeConcat(args []string, exit int) int {
	var list string
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			list = args[i+1]
		}
	}
	body, err := os.ReadFile(list)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 97
	}
	out, err := os.Create(args[len(args)-1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 96
	}
	defer out.Close()
	for _, line := range strings.Split(string(body), "\n") {
		path, ok := strings.CutPrefix(line, "file '")
		if !ok {
			continue
		}
		path = strings.TrimSuffix(path, "'")
		in, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 95
		}
		io.Copy(out, in)
		in.Close()
	}
	fmt.Fprintf(os.Stdout, "frame=1\nout_time_us=%s\nprogress=end\n", os.Getenv("FAKE_OUT_TIME_US"))
	fmt.Fprint(os.Stderr, os.Getenv("FAKE_STDERR"))
	return exit
}

// fake returns a Command that runs the test binary as ffmpeg, with the
// given environment on top of the mode.
func fake(t *testing.T, mode string, env map[string]string) (cmd Command, argv string, extraEnv []string) {
	t.Helper()
	argv = filepath.Join(t.TempDir(), "argv")
	extraEnv = []string{"FAKE_FFMPEG=1", "FAKE_MODE=" + mode, "FAKE_ARGV=" + argv}
	for k, v := range env {
		extraEnv = append(extraEnv, k+"="+v)
	}
	return Command{os.Args[0]}, argv, extraEnv
}

// readArgv returns the argument lists recorded by the fake, one per run.
func readArgv(t *testing.T, path string) [][]string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no argv was recorded: %v", err)
	}
	var runs [][]string
	for _, block := range strings.Split(strings.TrimSpace(string(body)), "\n\n") {
		runs = append(runs, strings.Split(block, "\n"))
	}
	return runs
}
