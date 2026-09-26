package video

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestCredentialsComeFromTheEnvironmentFirst(t *testing.T) {
	c, err := LoadCredentials(envOf(map[string]string{"CAMERA_USER": "admin", "CAMERA_PASS": "s3cret"}), "")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if c.User != "admin" || c.Pass != "s3cret" {
		t.Fatalf("got %+v", c)
	}
}

func TestCredentialsComeFromA0600File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "camera.env")
	body := "# camera login\nCAMERA_USER=admin\n\nCAMERA_PASS=p@ss word=1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCredentials(envOf(nil), path)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if c.User != "admin" || c.Pass != "p@ss word=1" {
		t.Fatalf("got %+v", c)
	}
}

// SPEC.md section 3: the file must be 0600. A group- or world-readable password
// file is refused, and the refusal says what mode it must have.
func TestCredentialsFileMustBe0600(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o644, 0o604, 0o660} {
		path := filepath.Join(t.TempDir(), "camera.env")
		if err := os.WriteFile(path, []byte("CAMERA_USER=admin\nCAMERA_PASS=hunter2\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := LoadCredentials(envOf(nil), path)
		if err == nil {
			t.Errorf("mode %04o was accepted", mode)
			continue
		}
		if !errors.Is(err, ErrCredentialsMode) {
			t.Errorf("mode %04o: error %v is not ErrCredentialsMode", mode, err)
		}
		if !strings.Contains(err.Error(), "0600") {
			t.Errorf("mode %04o: error %q does not say the mode must be 0600", mode, err)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("mode %04o: error %q repeats the password", mode, err)
		}
	}
}

func TestCredentialsFileMistakesAreNamedWithoutTheValue(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"no user", "CAMERA_PASS=hunter2\n", "CAMERA_USER"},
		{"no pass", "CAMERA_USER=admin\n", "CAMERA_PASS"},
		{"unknown key", "CAMERA_USER=admin\nCAMERA_PASS=hunter2\nPASSWORD=hunter2\n", "line 3"},
		{"no equals", "CAMERA_USER admin\n", "line 1"},
	}
	for _, tc := range tests {
		path := filepath.Join(t.TempDir(), "camera.env")
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadCredentials(envOf(nil), path)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %s", tc.name, err, tc.want)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: error %q repeats the password", tc.name, err)
		}
	}
}

func TestNoCredentialsIsAnError(t *testing.T) {
	_, err := LoadCredentials(envOf(nil), "")
	if err == nil {
		t.Fatal("no credentials were accepted")
	}
	for _, want := range []string{"CAMERA_USER", "camera_credentials_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say where to put them (%s)", err, want)
		}
	}
	// A user without a password in the environment is half a login, and the
	// file must not be read as a fallback for the missing half.
	_, err = LoadCredentials(envOf(map[string]string{"CAMERA_USER": "admin"}), "")
	if err == nil {
		t.Fatal("a user with no password was accepted")
	}
}

// systemd puts the login in the environment with EnvironmentFile=, and
// every child the collector starts would inherit it. Once the login is read
// it is taken out, and what was read still holds it.
func TestForgetEnvLoginTakesTheLoginOutOfTheEnvironment(t *testing.T) {
	t.Setenv("CAMERA_USER", "admin")
	t.Setenv("CAMERA_PASS", "test-only-pass")
	c, err := LoadCredentials(os.Getenv, "")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	ForgetEnvLogin()
	for _, k := range []string{"CAMERA_USER", "CAMERA_PASS"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s is still in the environment (%d bytes)", k, len(v))
		}
	}
	if c.User != "admin" || c.Pass != "test-only-pass" {
		t.Errorf("the loaded login changed when the environment was cleared: user %q", c.User)
	}
}

// The environment a child gets carries no CAMERA_ entry, even when the
// process still has one or a caller passes one in.
func TestChildEnvCarriesNoCameraEntry(t *testing.T) {
	t.Setenv("CAMERA_USER", "admin")
	t.Setenv("CAMERA_PASS", "test-only-pass")
	t.Setenv("CAMERA_OTHER", "x")
	env := childEnv([]string{"CAMERA_PASS=passed-in", "A=1"})
	for _, kv := range env {
		if strings.HasPrefix(kv, "CAMERA_") {
			t.Errorf("childEnv holds %s", kv[:strings.IndexByte(kv, '=')])
		}
	}
	if !contains(env, "A=1") || !contains(env, "TZ=UTC") {
		t.Errorf("childEnv lost what it should keep: %d entries", len(env))
	}
}
