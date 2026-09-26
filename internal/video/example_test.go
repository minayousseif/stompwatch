package video

import (
	"os"
	"strings"
	"testing"
)

// The example the owner copies must use the names the program reads, and it
// must not ship a password. A renamed variable, or a placeholder somebody
// leaves in place, would both be silent.
func TestCameraEnvExampleMatchesWhatTheProgramReads(t *testing.T) {
	const path = "../../deploy/camera.env.example"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the example: %v", err)
	}

	got := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Errorf("line %q is not KEY=value", line)
			continue
		}
		got[key] = value
	}

	for _, key := range []string{"CAMERA_USER", "CAMERA_PASS"} {
		if _, ok := got[key]; !ok {
			t.Errorf("the example does not set %s", key)
		}
	}
	if len(got) != 2 {
		t.Errorf("the example sets %d variables, want only CAMERA_USER and CAMERA_PASS: %v", len(got), got)
	}
	// A password in a file that is committed is a password that is public.
	if got["CAMERA_PASS"] != "" {
		t.Errorf("the example ships a password: CAMERA_PASS=%q", got["CAMERA_PASS"])
	}

	// The example must parse through the same reader the program uses, so a
	// format the example uses but the program cannot read is caught here.
	// The password is filled in only for the copy under test: an empty one
	// is rightly refused, and the shipped file must stay empty.
	filled := strings.Replace(string(b), "CAMERA_PASS=", "CAMERA_PASS=hunter2 with a space", 1)
	f := t.TempDir() + "/camera.env"
	if err := os.WriteFile(f, []byte(filled), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCredentials(func(string) string { return "" }, f)
	if err != nil {
		t.Fatalf("the program cannot read its own example: %v", err)
	}
	if c.User != "admin" {
		t.Errorf("user read from the example = %q, want admin", c.User)
	}
	// The comment promises a value may hold spaces. Hold it to that.
	if c.Pass != "hunter2 with a space" {
		t.Errorf("password read from the example = %q, want the whole value after the =", c.Pass)
	}
}
