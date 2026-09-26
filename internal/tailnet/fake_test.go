package tailnet

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests run without a tailnet. The test binary stands in for the
// tailscale command: when FAKE_TAILSCALE is set, TestMain runs fakeTailscale
// instead of the tests, and the environment says what it answers.
//
//	FAKE_ARGV          file the argument list is appended to, one run per block
//	FAKE_STATUS        stdout for "status --json"
//	FAKE_STATUS_EXIT   exit status of that run
//	FAKE_SERVE         stdout for "serve status --json"
//	FAKE_SERVE_EXIT    exit status of that run
//	FAKE_STDERR        stderr printed by every run
//	FAKE_HANG_MS       how long every run sleeps before it answers
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_TAILSCALE") != "" {
		os.Exit(fakeTailscale())
	}
	os.Exit(m.Run())
}

func fakeTailscale() int {
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
	if ms, _ := strconv.Atoi(os.Getenv("FAKE_HANG_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	fmt.Fprint(os.Stderr, os.Getenv("FAKE_STDERR"))
	out, exit := "FAKE_STATUS", "FAKE_STATUS_EXIT"
	if len(args) > 0 && args[0] == "serve" {
		out, exit = "FAKE_SERVE", "FAKE_SERVE_EXIT"
	}
	fmt.Fprint(os.Stdout, os.Getenv(out))
	code, _ := strconv.Atoi(os.Getenv(exit))
	return code
}

// fake returns a Reader that runs the test binary as tailscale, with the
// given environment.
func fake(t *testing.T, env map[string]string) Reader {
	t.Helper()
	extra := []string{"FAKE_TAILSCALE=1"}
	for k, v := range env {
		extra = append(extra, k+"="+v)
	}
	return Reader{Command: []string{os.Args[0]}, Env: extra}
}
