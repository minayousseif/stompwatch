package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/tailnet"
)

// The dashboard address the startup lines talk about.
const testHTTPAddr = "127.0.0.1:8080"

// recorder collects what the report wrote: the log, the health rows, and
// the failure pings.
type recorder struct {
	log    bytes.Buffer
	kinds  []string
	detail []string
	alerts []string
}

func (r *recorder) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&r.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (r *recorder) record(_ time.Time, kind, detail string, _ time.Duration) {
	r.kinds = append(r.kinds, kind)
	r.detail = append(r.detail, detail)
}

func (r *recorder) alert(reason string) { r.alerts = append(r.alerts, reason) }

func (r *recorder) text() string { return r.log.String() }

// lineWith returns the logged line that holds sub, or "".
func (r *recorder) lineWith(sub string) string {
	for _, line := range strings.Split(r.text(), "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}

func report(st tailnet.Status) *recorder {
	r := &recorder{}
	reportTailscale(st, testHTTPAddr, r.logger(), r.record, r.alert)
	return r
}

func TestServedDashboardIsOneInfoLine(t *testing.T) {
	r := report(tailnet.Status{
		Installed: true, Running: true, Backend: "Running",
		Name: "stompwatch.example.ts.net", Serving: true,
		ServeURL: "https://stompwatch.example.ts.net/",
	})
	line := r.lineWith("the dashboard is reachable over Tailscale")
	if line == "" {
		t.Fatalf("the log does not say the dashboard is reachable:\n%s", r.text())
	}
	if !strings.Contains(line, "level=INFO") {
		t.Errorf("the line is not at INFO: %s", line)
	}
	if !strings.Contains(line, "https://stompwatch.example.ts.net/") {
		t.Errorf("the line does not carry the address to open: %s", line)
	}
	if len(r.kinds) != 0 || len(r.alerts) != 0 {
		t.Errorf("a working setup wrote %v and alerted %v", r.kinds, r.alerts)
	}
}

func TestNothingServingIsAWarningWithTheCommandToRun(t *testing.T) {
	r := report(tailnet.Status{
		Installed: true, Running: true, Backend: "Running",
		Name: "stompwatch.example.ts.net",
	})
	line := r.lineWith("nothing serves the dashboard")
	if line == "" {
		t.Fatalf("the log does not say that nothing serves the dashboard:\n%s", r.text())
	}
	if !strings.Contains(line, "level=WARN") {
		t.Errorf("the line is not at WARN: %s", line)
	}
	if !strings.Contains(line, "sudo tailscale serve --bg http://127.0.0.1:8080") {
		t.Errorf("the line does not carry the command that fixes it: %s", line)
	}
}

func TestNoTailscaleIsNotAFault(t *testing.T) {
	r := report(tailnet.Status{Err: "tailscale is not installed on this box"})
	if strings.Contains(r.text(), "level=WARN") || strings.Contains(r.text(), "level=ERROR") {
		t.Errorf("a box with no Tailscale was reported as a fault:\n%s", r.text())
	}
	if r.lineWith("Tailscale") == "" {
		t.Errorf("the log says nothing about Tailscale at all:\n%s", r.text())
	}
	if len(r.kinds) != 0 || len(r.alerts) != 0 {
		t.Errorf("a box with no Tailscale wrote %v and alerted %v", r.kinds, r.alerts)
	}
}

func TestTailscaledNotRunningIsNotAFault(t *testing.T) {
	r := report(tailnet.Status{
		Installed: true, Backend: "Stopped",
		Err: "tailscale status could not be read: failed to connect to local tailscaled",
	})
	if strings.Contains(r.text(), "level=WARN") || strings.Contains(r.text(), "level=ERROR") {
		t.Errorf("a stopped tailscaled was reported as a fault:\n%s", r.text())
	}
	if len(r.alerts) != 0 {
		t.Errorf("a stopped tailscaled alerted %v", r.alerts)
	}
}

// Funnel publishes the box to the public internet, and auth_mode trusts a
// header that is only safe behind serve. It has to be impossible to miss.
func TestFunnelIsAnErrorAnAlertAndAHealthRow(t *testing.T) {
	r := report(tailnet.Status{
		Installed: true, Running: true, Backend: "Running",
		Name: "stompwatch.example.ts.net", Serving: true, Funnel: true,
		ServeURL: "https://stompwatch.example.ts.net/",
	})
	line := r.lineWith("public internet")
	if line == "" {
		t.Fatalf("the log does not say the dashboard is on the public internet:\n%s", r.text())
	}
	if !strings.Contains(line, "level=ERROR") {
		t.Errorf("the line is not at ERROR: %s", line)
	}
	if !strings.Contains(line, "auth_mode") {
		t.Errorf("the line does not say why it matters: %s", line)
	}
	if !strings.Contains(line, "tailscale funnel") {
		t.Errorf("the line does not say how to turn it off: %s", line)
	}
	if len(r.kinds) != 1 || r.kinds[0] != "tailscale_funnel" {
		t.Fatalf("the health rows are %v, want one tailscale_funnel", r.kinds)
	}
	if !strings.Contains(r.detail[0], "public internet") {
		t.Errorf("the health row says %q", r.detail[0])
	}
	if len(r.alerts) != 1 {
		t.Fatalf("the failure pings are %v, want one", r.alerts)
	}
	if !strings.Contains(r.alerts[0], "public internet") {
		t.Errorf("the failure ping says %q", r.alerts[0])
	}
}

// A key that expires drops the box off the tailnet, and the dashboard then
// looks exactly like a dead box (SPEC.md section 9.0).
func TestKeyExpiryIsAWarningWithTheDate(t *testing.T) {
	r := report(tailnet.Status{
		Installed: true, Running: true, Backend: "Running",
		Name: "stompwatch.example.ts.net", Serving: true,
		ServeURL:  "https://stompwatch.example.ts.net/",
		KeyExpiry: time.Date(2026, 9, 27, 10, 28, 11, 0, time.UTC),
	})
	line := r.lineWith("key expires")
	if line == "" {
		t.Fatalf("the log does not warn that the node key expires:\n%s", r.text())
	}
	if !strings.Contains(line, "level=WARN") {
		t.Errorf("the line is not at WARN: %s", line)
	}
	if !strings.Contains(line, "2026-09-27") {
		t.Errorf("the line does not carry the date: %s", line)
	}
}

func TestKeyExpiryDisabledSaysNothing(t *testing.T) {
	r := report(tailnet.Status{
		Installed: true, Running: true, Backend: "Running", Serving: true,
		Name: "stompwatch.example.ts.net", ServeURL: "https://stompwatch.example.ts.net/",
	})
	if line := r.lineWith("key expires"); line != "" {
		t.Fatalf("a node whose key does not expire was warned about: %s", line)
	}
}

// The serve config being unreadable is not the same as nothing serving the
// dashboard, and must not send the owner to run a command they do not need.
func TestAnUnreadableServeConfigDoesNotClaimNothingServes(t *testing.T) {
	r := report(tailnet.Status{
		Installed: true, Running: true, Backend: "Running",
		Name: "stompwatch.example.ts.net",
		Err:  "the tailscale serve config could not be read: access denied",
	})
	if line := r.lineWith("nothing serves the dashboard"); line != "" {
		t.Errorf("an unreadable serve config was reported as nothing serving: %s", line)
	}
	if r.lineWith("could not be read") == "" {
		t.Errorf("the log does not say what could not be read:\n%s", r.text())
	}
}

// --- the whole program ---

// fakeTailscaleOnPath puts a tailscale on PATH that answers with the given
// JSON. It is a script, because this is the one test that has to prove the
// collector really looks the command up and runs it.
func fakeTailscaleOnPath(t *testing.T, status, serve string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"serve\" ]; then\n  cat <<'JSON'\n" + serve +
		"\nJSON\nelse\n  cat <<'JSON'\n" + status + "\nJSON\nfi\n"
	path := filepath.Join(dir, "tailscale")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunReportsTailscaleAfterTheDashboardListens(t *testing.T) {
	fakeTailscaleOnPath(t, `{
  "BackendState": "Running",
  "Self": {"DNSName": "stompwatch.example.ts.net.", "HostName": "stompwatch"},
  "MagicDNSSuffix": "example.ts.net"
}`, `{}`)

	cfg, dir := writeConfig(t)
	wav := thumps(t, dir)
	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "nothing serves the dashboard") {
		t.Fatalf("the log does not report what Tailscale is doing:\n%s", stderr)
	}
	if !strings.Contains(stderr, "stompwatch.example.ts.net") {
		t.Errorf("the log does not name the node:\n%s", stderr)
	}
	if strings.Contains(stderr, "nodekey") {
		t.Errorf("the log carries key material from the tailscale answer:\n%s", stderr)
	}
}

// Tailscale must never be a reason for the collector to stop measuring.
func TestRunKeepsMeasuringWhenTailscaleIsBroken(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "tailscale")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg, data := writeConfig(t)
	wav := thumps(t, data)
	code, _, stderr := runCmd("run", "-config", cfg, "-input", wav)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if n := rows(t, readDB(t, data), `SELECT count(*) FROM samples_1s`); n != 60 {
		t.Fatalf("samples_1s has %d rows, want 60: the measurement stopped with Tailscale", n)
	}
}
