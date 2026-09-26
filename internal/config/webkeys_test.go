package config

import (
	"strings"
	"testing"
	"time"
)

// The Phase 3 keys need defaults that work with no config change at all.
func TestParseGivesPhase3Defaults(t *testing.T) {
	c, err := Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"http_addr", c.HTTPAddr, "127.0.0.1:8080"},
		{"auth_mode", c.AuthMode, "tailscale"},
		{"auth_header", c.AuthHeader, "Tailscale-User-Login"},
		{"auth_name_header", c.AuthNameHeader, "Tailscale-User-Name"},
		{"quiet_start", c.Quiet.Start, 22 * time.Hour},
		{"quiet_end", c.Quiet.End, 7 * time.Hour},
		{"log_file_mb", c.LogFileMB, int64(20)},
		{"log_files", c.LogFiles, 5},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.name, ch.got, ch.want)
		}
	}
}

func TestParseReadsPhase3Keys(t *testing.T) {
	in := minimal + `http_addr = 127.0.0.1:9000
auth_mode = trusted_header
auth_header = X-Forwarded-User
auth_name_header =
quiet_start = 23:15
quiet_end = 06:45
log_file_mb = 100
log_files = 12
`
	c, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.HTTPAddr != "127.0.0.1:9000" || c.AuthMode != "trusted_header" ||
		c.AuthHeader != "X-Forwarded-User" || c.AuthNameHeader != "" ||
		c.Quiet.Start != 23*time.Hour+15*time.Minute || c.Quiet.End != 6*time.Hour+45*time.Minute ||
		c.LogFileMB != 100 || c.LogFiles != 12 {
		t.Fatalf("parsed %+v", c)
	}
}

// An empty http_addr turns the server off, and then the loopback rule does
// not apply.
func TestParseAllowsEmptyHTTPAddr(t *testing.T) {
	c, err := Parse(strings.NewReader(minimal + "http_addr =\nauth_mode = none\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.HTTPAddr != "" {
		t.Fatalf("http_addr = %q, want empty", c.HTTPAddr)
	}
}

// auth_mode = none has no authentication at all, so the listener must stay on
// the local machine. ":8080" has no host, and net.Listen then binds every
// interface, so it is not loopback.
func TestAuthModeNoneNeedsALoopbackAddress(t *testing.T) {
	ok := []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"}
	for _, addr := range ok {
		in := minimal + "auth_mode = none\nhttp_addr = " + addr + "\n"
		if _, err := Parse(strings.NewReader(in)); err != nil {
			t.Errorf("http_addr %q with auth_mode none: %v", addr, err)
		}
	}
	bad := []string{":8080", "0.0.0.0:8080", "192.168.1.5:8080", "[::]:8080", "noise.example.com:8080"}
	for _, addr := range bad {
		in := minimal + "auth_mode = none\nhttp_addr = " + addr + "\n"
		_, err := Parse(strings.NewReader(in))
		if err == nil {
			t.Errorf("http_addr %q with auth_mode none was accepted", addr)
			continue
		}
		if !strings.Contains(err.Error(), "auth_mode") || !strings.Contains(err.Error(), "http_addr") {
			t.Errorf("http_addr %q: error %q should name both keys", addr, err)
		}
	}
	// The same addresses are fine when the network authenticates the caller.
	for _, addr := range bad {
		in := minimal + "http_addr = " + addr + "\n"
		if _, err := Parse(strings.NewReader(in)); err != nil {
			t.Errorf("http_addr %q with auth_mode tailscale: %v", addr, err)
		}
	}
}

// The startup warning reads LoopbackHTTPAddr. An address with no host
// listens on every interface, so it must get the warning.
func TestAnAddressWithNoHostIsNotLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		":8080":          false,
		"0.0.0.0:8080":   false,
		"127.0.0.1:8080": true,
		"localhost:8080": true,
		"":               true, // the server is off, so it reaches nobody
	} {
		c := Default()
		c.HTTPAddr = addr
		if got := c.LoopbackHTTPAddr(); got != want {
			t.Errorf("LoopbackHTTPAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestParseRejectsBadPhase3Values(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"unknown auth mode", minimal + "auth_mode = password\n", "auth_mode"},
		{"http_addr without a port", minimal + "http_addr = 127.0.0.1\n", "http_addr"},
		{"empty auth header", minimal + "auth_header =\n", "auth_header"},
		{"quiet_start without a colon", minimal + "quiet_start = 2200\n", "quiet_start"},
		{"quiet_start hour 24", minimal + "quiet_start = 24:00\n", "quiet_start"},
		{"quiet_start minute 60", minimal + "quiet_start = 22:60\n", "quiet_start"},
		{"quiet_start not a number", minimal + "quiet_start = late\n", "quiet_start"},
		{"quiet_end equals quiet_start", minimal + "quiet_start = 22:00\nquiet_end = 22:00\n", "quiet_end"},
		{"log_file_mb 0", minimal + "log_file_mb = 0\n", "log_file_mb"},
		{"log_file_mb 2000", minimal + "log_file_mb = 2000\n", "log_file_mb"},
		{"log_files 0", minimal + "log_files = 0\n", "log_files"},
		{"log_files 101", minimal + "log_files = 101\n", "log_files"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Parse error = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// An empty auth_header is fine when nothing reads it.
func TestEmptyAuthHeaderIsFineWithAuthModeNone(t *testing.T) {
	if _, err := Parse(strings.NewReader(minimal + "auth_mode = none\nauth_header =\n")); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

// The boundary values of the log keys are allowed.
func TestLogRotationBoundsAreInclusive(t *testing.T) {
	for _, in := range []string{"log_file_mb = 1\n", "log_file_mb = 1024\n", "log_files = 1\n", "log_files = 100\n"} {
		if _, err := Parse(strings.NewReader(minimal + in)); err != nil {
			t.Errorf("%q: %v", strings.TrimSpace(in), err)
		}
	}
}
