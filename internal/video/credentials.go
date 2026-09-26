package video

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Credentials is the camera login. It is never written to the config file,
// the database, a log line, or an error (SPEC.md section 3).
type Credentials struct {
	User string
	Pass string
}

// String, Format, LogValue, MarshalJSON, and MarshalText all print the user
// and never the password, whatever asks: fmt, slog, or an encoder that
// would otherwise walk the fields by reflection.
func (c Credentials) String() string               { return c.User + ":***" }
func (c Credentials) Format(f fmt.State, _ rune)   { fmt.Fprint(f, c.String()) }
func (c Credentials) LogValue() slog.Value         { return slog.StringValue(c.String()) }
func (c Credentials) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }
func (c Credentials) MarshalText() ([]byte, error) { return []byte(c.String()), nil }

// ErrCredentialsMode means the credentials file can be read by more than
// its owner.
var ErrCredentialsMode = errors.New("video: the camera credentials file must be mode 0600")

// The environment variables and file keys that carry the login. The file
// uses the same names as the environment, so one file serves both a
// systemd EnvironmentFile= line and camera_credentials_file.
const (
	EnvUser = "CAMERA_USER"
	EnvPass = "CAMERA_PASS"
)

// LoadCredentials reads the camera login from the environment, or from
// file when the environment has none. getenv is os.Getenv in the program
// and a map in tests.
//
// The environment wins when EnvUser is set, and then both halves must be
// there: a user with no password is not half a login that the file can
// complete, it is a mistake to report.
func LoadCredentials(getenv func(string) string, file string) (Credentials, error) {
	if user := getenv(EnvUser); user != "" {
		pass := getenv(EnvPass)
		if pass == "" {
			return Credentials{}, fmt.Errorf("video: %s is set but %s is empty", EnvUser, EnvPass)
		}
		return Credentials{User: user, Pass: pass}, nil
	}
	if file == "" {
		return Credentials{}, fmt.Errorf("video: no camera login; set %s and %s in the environment, "+
			"or name a 0600 file with both in camera_credentials_file", EnvUser, EnvPass)
	}
	return readCredentialsFile(file)
}

// ForgetEnvLogin takes EnvUser and EnvPass out of the process environment.
// systemd puts them there with EnvironmentFile=, and every child the
// collector starts would otherwise inherit the password: ffmpeg, arecord,
// amixer, and tailscale. The collector calls it once the login is read,
// whether or not the read worked, and before it starts any child.
func ForgetEnvLogin() {
	os.Unsetenv(EnvUser)
	os.Unsetenv(EnvPass)
}

// readCredentialsFile reads KEY=value lines. Nothing from the file goes into
// an error: an error names a line number, never a value.
func readCredentialsFile(path string) (Credentials, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("video: camera credentials file: %w", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return Credentials{}, fmt.Errorf("%w: %s is mode %04o; run chmod 0600 on it so only the service user can read the password",
			ErrCredentialsMode, path, mode)
	}
	f, err := os.Open(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("video: camera credentials file: %w", err)
	}
	defer f.Close()

	var c Credentials
	var haveUser, havePass bool
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		key, val, ok := strings.Cut(s, "=")
		if !ok {
			return Credentials{}, fmt.Errorf("video: %s line %d: want KEY=value", path, line)
		}
		switch strings.TrimSpace(key) {
		case EnvUser:
			c.User, haveUser = val, true
		case EnvPass:
			c.Pass, havePass = val, true
		default:
			return Credentials{}, fmt.Errorf("video: %s line %d: unknown key; only %s and %s are read",
				path, line, EnvUser, EnvPass)
		}
	}
	if err := sc.Err(); err != nil {
		return Credentials{}, fmt.Errorf("video: camera credentials file: %w", err)
	}
	switch {
	case !haveUser || c.User == "":
		return Credentials{}, fmt.Errorf("video: %s does not set %s", path, EnvUser)
	case !havePass || c.Pass == "":
		return Credentials{}, fmt.Errorf("video: %s does not set %s", path, EnvPass)
	}
	return c, nil
}
