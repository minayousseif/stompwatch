package web

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/tailnet"
)

// tailscaleEnv is a server whose Tailscale reading is the given one. It
// returns the number of times the server asked, and a clock the test moves.
func tailscaleEnv(t *testing.T, st tailnet.Status) (*env, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	var offset atomic.Int64
	e := newEnv(t, func(c *Config, _ *config.Config) {
		c.Now = func() time.Time { return t0.Add(time.Duration(offset.Load())) }
		c.Tailscale = func(context.Context) tailnet.Status {
			calls.Add(1)
			return st
		}
	})
	return e, &calls, &offset
}

func TestSystemReportsTheServedDashboard(t *testing.T) {
	e, _, _ := tailscaleEnv(t, tailnet.Status{
		Installed: true, Running: true, Backend: "Running",
		Name:     "stompwatch.example.ts.net",
		Serving:  true,
		ServeURL: "https://stompwatch.example.ts.net/",
		// 2026-09-27T10:28:11Z
		KeyExpiry: time.Date(2026, 9, 27, 10, 28, 11, 0, time.UTC),
	})

	ts := object(t, e.getJSON("/api/system"), "tailscale")
	if got := ts["installed"]; got != true {
		t.Errorf("installed = %v, want true", got)
	}
	if got := ts["running"]; got != true {
		t.Errorf("running = %v, want true", got)
	}
	if got := str(t, ts, "backend"); got != "Running" {
		t.Errorf("backend = %q, want \"Running\"", got)
	}
	if got := str(t, ts, "name"); got != "stompwatch.example.ts.net" {
		t.Errorf("name = %q", got)
	}
	if got := ts["serving"]; got != true {
		t.Errorf("serving = %v, want true", got)
	}
	if got := str(t, ts, "serve_url"); got != "https://stompwatch.example.ts.net/" {
		t.Errorf("serve_url = %q", got)
	}
	if got := ts["funnel"]; got != false {
		t.Errorf("funnel = %v, want false", got)
	}
	if got := int64(num(t, ts, "key_expiry_ms")); got != 1790504891000 {
		t.Errorf("key_expiry_ms = %d, want 1790504891000", got)
	}
	if got := str(t, ts, "err"); got != "" {
		t.Errorf("err = %q, want empty", got)
	}
}

func TestSystemSaysZeroWhenKeyExpiryIsDisabled(t *testing.T) {
	e, _, _ := tailscaleEnv(t, tailnet.Status{Installed: true, Running: true, Backend: "Running"})
	ts := object(t, e.getJSON("/api/system"), "tailscale")
	if got := num(t, ts, "key_expiry_ms"); got != 0 {
		t.Fatalf("key_expiry_ms = %v, want 0: the node's key does not expire", got)
	}
}

func TestSystemReportsFunnel(t *testing.T) {
	e, _, _ := tailscaleEnv(t, tailnet.Status{
		Installed: true, Running: true, Backend: "Running", Serving: true, Funnel: true,
		Name: "stompwatch.example.ts.net", ServeURL: "https://stompwatch.example.ts.net/",
	})
	ts := object(t, e.getJSON("/api/system"), "tailscale")
	if got := ts["funnel"]; got != true {
		t.Fatalf("funnel = %v, want true", got)
	}
}

func TestSystemReportsABoxWithNoTailscale(t *testing.T) {
	e, _, _ := tailscaleEnv(t, tailnet.Status{Err: "tailscale is not installed on this box"})
	ts := object(t, e.getJSON("/api/system"), "tailscale")
	if got := ts["installed"]; got != false {
		t.Errorf("installed = %v, want false", got)
	}
	if got := str(t, ts, "err"); got != "tailscale is not installed on this box" {
		t.Errorf("err = %q", got)
	}
}

// With nothing wired up, the block is still there and still says false. A
// screen that has to cope with a missing block is a screen with a second
// way to be wrong.
func TestSystemCarriesTheBlockWithNoReader(t *testing.T) {
	e := newEnv(t)
	ts := object(t, e.getJSON("/api/system"), "tailscale")
	if got := ts["installed"]; got != false {
		t.Errorf("installed = %v, want false", got)
	}
	if got := ts["serving"]; got != false {
		t.Errorf("serving = %v, want false", got)
	}
}

// Shelling out on every request would make the screen slow and hammer the
// tailscaled socket.
func TestTailscaleIsReadAtMostOnceAMinute(t *testing.T) {
	e, calls, offset := tailscaleEnv(t, tailnet.Status{Installed: true, Running: true})

	e.getJSON("/api/system")
	e.getJSON("/api/system")
	offset.Store(int64(59 * time.Second))
	e.getJSON("/api/system")
	if got := calls.Load(); got != 1 {
		t.Fatalf("tailscale was read %d times in under a minute, want 1", got)
	}

	offset.Store(int64(61 * time.Second))
	e.getJSON("/api/system")
	if got := calls.Load(); got != 2 {
		t.Fatalf("tailscale was read %d times after the minute was up, want 2", got)
	}
}

// tailscale status --json carries the node key, every peer's key and the
// tailnet's user list. None of it may reach the browser.
func TestSystemNeverCarriesTheRawTailscaleAnswer(t *testing.T) {
	e, _, _ := tailscaleEnv(t, tailnet.Status{
		Installed: true, Running: true, Backend: "Running",
		Name: "stompwatch.example.ts.net",
	})
	body := e.get("/api/system").Body.String()
	for _, secret := range []string{"nodekey:", "PublicKey", "BackendState", "MagicDNSSuffix", "TailscaleIPs", "Peer"} {
		if strings.Contains(body, secret) {
			t.Errorf("the system response carries %q from the raw tailscale answer", secret)
		}
	}
}

// The screen has to print the serve command with the address in it, and
// http_addr is not one of the settings the dashboard can read.
func TestSystemCarriesTheDashboardListenAddress(t *testing.T) {
	e := newEnv(t, func(_ *Config, file *config.Config) {
		file.HTTPAddr = "127.0.0.1:8080"
	})
	ts := object(t, e.getJSON("/api/system"), "tailscale")
	if got := str(t, ts, "http_addr"); got != "127.0.0.1:8080" {
		t.Fatalf("http_addr = %q, want \"127.0.0.1:8080\"", got)
	}
}

// The block carries these fields and no others. A field added later to
// carry the whole tailscale answer through would land here, whatever it
// happened to hold at the time.
func TestTheTailscaleBlockHasExactlyTheseFields(t *testing.T) {
	e, _, _ := tailscaleEnv(t, tailnet.Status{Installed: true, Running: true})
	ts := object(t, e.getJSON("/api/system"), "tailscale")
	want := []string{
		"installed", "running", "backend", "name", "serving", "serve_url",
		"funnel", "key_expiry_ms", "err", "http_addr",
	}
	got := slices.Sorted(maps.Keys(ts))
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the tailscale block has %v, want %v", got, want)
	}
}
