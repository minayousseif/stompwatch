package video

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// Login says whether a camera login was read and where it came from. It is
// what the dashboard shows: a source and a user name, never a password.
//
// The password is inside Creds, which masks itself in every printed,
// logged, and encoded form. Nothing outside this package can read it except
// by naming the field, which only the code that builds an ffmpeg command
// line does.
type Login struct {
	// Source is "environment", "file", or "none".
	Source string
	Creds  Credentials
	// Err says why no login was read. It never repeats a password: the
	// loader keeps values out of its errors.
	Err error
}

// Loaded reports whether a whole login was read.
func (l Login) Loaded() bool { return l.Creds.User != "" && l.Creds.Pass != "" }

// Credential sources, as the dashboard names them.
const (
	FromEnvironment = "environment"
	FromFile        = "file"
	FromNowhere     = "none"
)

// DescribeLogin reads the camera login the way LoadCredentials does and says
// where it came from. It is for the dashboard, which must be able to tell
// the owner that a login is loaded without ever carrying the password.
func DescribeLogin(getenv func(string) string, file string) Login {
	source := FromFile
	if getenv(EnvUser) != "" {
		source = FromEnvironment
	}
	creds, err := LoadCredentials(getenv, file)
	if err != nil {
		return Login{Source: FromNowhere, Err: err}
	}
	return Login{Source: source, Creds: creds}
}

// versionTimeout bounds the version check. ffmpeg -version answers at once
// or not at all.
const versionTimeout = 5 * time.Second

// Version returns the first line ffmpeg prints for -version, or the empty
// string when ffmpeg cannot be run. The dashboard shows it so the owner can
// see whether video can be recorded at all.
func Version(c Command, env []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	args := append(append([]string(nil), c.leading()...), "-version")
	cmd := exec.CommandContext(ctx, c.program(), args...)
	cmd.Env = childEnv(env)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}

// clockCheckTimeout bounds the camera clock reading inside a full camera test.
const clockCheckTimeout = 10 * time.Second

// CameraReport is everything a full camera test found: the sub-stream paths
// tried, the main stream, and the camera clock.
type CameraReport struct {
	Sub  Report
	Main Report // empty when there was no main path to try
	// MainPath is the path the main stream was tried on: the configured
	// one, or the sub path with _main for _sub. Empty when neither gave one.
	MainPath    string
	ClockOffset time.Duration // the camera clock minus the host clock
	// ClockErr says why there is no reading: the camera does not report its
	// clock, or it was never asked. A zero offset with no error would be a
	// measurement nobody made.
	ClockErr error
}

// ErrClockNotRead means the camera clock was never asked for, because no
// stream answered and there was nothing to test against.
var ErrClockNotRead = errors.New("video: the camera clock was not read, because no stream answered")

// RunCameraTest tries the sub-stream paths in order, then the main stream, then
// reads the camera clock. One implementation serves stompwatch test-camera
// and POST /api/camera/test, so the two can never disagree about what
// works. Nothing it returns holds a password (SPEC.md section 7).
func RunCameraTest(ctx context.Context, cmd Command, env []string, base Stream,
	subPaths []string, mainPath string) CameraReport {
	rep := CameraReport{Sub: TryPaths(ctx, cmd, env, base, subPaths)}
	if rep.Sub.Working == nil {
		// There is no stream to derive a main path from, and no reason to
		// believe the main path would answer when the sub path did not.
		rep.ClockErr = ErrClockNotRead
		return rep
	}
	rep.MainPath = mainPath
	if rep.MainPath == "" {
		rep.MainPath = MainPath(rep.Sub.Working.Stream.Path)
	}
	if rep.MainPath != "" {
		rep.Main = TryPaths(ctx, cmd, env, base, []string{rep.MainPath})
	}
	cctx, cancel := context.WithTimeout(ctx, clockCheckTimeout)
	defer cancel()
	rep.ClockOffset, rep.ClockErr = CameraOffset(cctx, base)
	return rep
}
