package tailnet

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The dashboard address every test asks about.
const addr = "127.0.0.1:8080"

func read(t *testing.T, r Reader, httpAddr string) Status {
	t.Helper()
	return r.Read(t.Context(), httpAddr)
}

// --- the commands this package may run ---

// The whole safety story of this package is that it only reads. The set is
// stated here in full so that adding a writing subcommand breaks a test
// rather than shipping quietly.
func TestOnlyTwoReadOnlySubcommandsAreAllowed(t *testing.T) {
	want := [][]string{
		{"status", "--json"},
		{"serve", "status", "--json"},
	}
	if !reflect.DeepEqual(allowed, want) {
		t.Fatalf("the allowed commands are %v, want %v", allowed, want)
	}
	// Say it a second way: no allowed command may be one that writes.
	writes := []string{"serve", "funnel", "up", "down", "set", "login", "logout", "cert"}
	for _, cmd := range allowed {
		for _, w := range writes {
			if cmd[0] != w {
				continue
			}
			// "serve status" reads; "serve" with anything else writes.
			if w == "serve" && len(cmd) > 1 && cmd[1] == "status" {
				continue
			}
			t.Errorf("%v is a command that changes Tailscale", cmd)
		}
	}
}

func TestRunRefusesACommandThatIsNotAllowed(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning})
	for _, args := range [][]string{
		{"serve", "--bg", "https", "/", "http://127.0.0.1:8080"},
		{"funnel", "8080", "on"},
		{"up"},
		{"logout"},
		{"status"},
	} {
		out, _, err := r.run(t.Context(), args...)
		if err == nil {
			t.Errorf("run %v was allowed to start", args)
		}
		if out != "" {
			t.Errorf("run %v produced output %q", args, out)
		}
	}
}

// A refused command must never reach the tailscale binary at all.
func TestRefusedCommandsNeverStartTheProgram(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_ARGV": argv})
	if _, _, err := r.run(t.Context(), "serve", "--bg", "https", "/", "http://"+addr); err == nil {
		t.Fatal("the writing command was allowed")
	}
	if runs := argvRuns(t, argv); len(runs) != 0 {
		t.Fatalf("the program ran %d times: %v", len(runs), runs)
	}
}

// --- states ---

func TestNotInstalled(t *testing.T) {
	r := Reader{Command: []string{filepath.Join(t.TempDir(), "no-such-tailscale")}}
	got := read(t, r, addr)
	if got.Installed {
		t.Error("Installed is true with no tailscale on the box")
	}
	if got.Running || got.Serving || got.Funnel {
		t.Errorf("a box with no tailscale reported %+v", got)
	}
	if !strings.Contains(got.Err, "not installed") {
		t.Errorf("Err = %q, want it to say tailscale is not installed", got.Err)
	}
}

func TestInstalledButTailscaledIsNotRunning(t *testing.T) {
	r := fake(t, map[string]string{
		"FAKE_STATUS":      "",
		"FAKE_STATUS_EXIT": "1",
		"FAKE_STDERR":      "failed to connect to local tailscaled; it doesn't appear to be running\n",
	})
	got := read(t, r, addr)
	if !got.Installed {
		t.Error("Installed is false although the command is there")
	}
	if got.Running {
		t.Error("Running is true although tailscaled did not answer")
	}
	if got.Err == "" {
		t.Error("Err is empty although nothing answered")
	}
	if got.Serving || got.Funnel {
		t.Errorf("nothing answered, yet the status claims %+v", got)
	}
}

func TestLoggedOutIsNotRunning(t *testing.T) {
	r := fake(t, map[string]string{
		"FAKE_STATUS":      statusNeedsLogin,
		"FAKE_STATUS_EXIT": "1",
		"FAKE_SERVE":       serveNone,
	})
	got := read(t, r, addr)
	if !got.Installed {
		t.Error("Installed is false although the command is there")
	}
	if got.Running {
		t.Error("Running is true although the backend state is NeedsLogin")
	}
	if got.Backend != "NeedsLogin" {
		t.Errorf("Backend = %q, want \"NeedsLogin\"", got.Backend)
	}
}

func TestRunningWithNothingServed(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveNone})
	got := read(t, r, addr)
	if !got.Running {
		t.Error("Running is false although the backend state is Running")
	}
	if got.Backend != "Running" {
		t.Errorf("Backend = %q, want \"Running\"", got.Backend)
	}
	if got.Name != "stompwatch.example.ts.net" {
		t.Errorf("Name = %q, want \"stompwatch.example.ts.net\"", got.Name)
	}
	if got.Serving {
		t.Error("Serving is true although no serve config exists")
	}
	if got.ServeURL != "" {
		t.Errorf("ServeURL = %q, want empty", got.ServeURL)
	}
	if got.Funnel {
		t.Error("Funnel is true although no serve config exists")
	}
	if got.Err != "" {
		t.Errorf("Err = %q, want empty: everything was readable", got.Err)
	}
}

func TestServingTheDashboard(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveDashboard})
	got := read(t, r, addr)
	if !got.Serving {
		t.Error("Serving is false although a handler proxies the dashboard")
	}
	if got.ServeURL != "https://stompwatch.example.ts.net/" {
		t.Errorf("ServeURL = %q, want \"https://stompwatch.example.ts.net/\"", got.ServeURL)
	}
	if got.Funnel {
		t.Error("Funnel is true although AllowFunnel is absent")
	}
}

func TestServingADifferentPortIsNotTheDashboard(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveOtherPort})
	got := read(t, r, addr)
	if got.Serving {
		t.Error("Serving is true although the only handler proxies port 9090")
	}
	if got.ServeURL != "" {
		t.Errorf("ServeURL = %q, want empty", got.ServeURL)
	}
}

// The host may be written three ways for the same loopback listener, and a
// near miss would send the owner to run a command they do not need.
func TestTheHostMayBeWrittenAnyLoopbackWay(t *testing.T) {
	cases := []struct{ httpAddr, proxy string }{
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"127.0.0.1:8080", "http://localhost:8080"},
		{"localhost:8080", "http://127.0.0.1:8080"},
		{":8080", "http://127.0.0.1:8080"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"[::1]:8080", "http://127.0.0.1:8080"},
	}
	for _, c := range cases {
		serve := strings.Replace(serveDashboard, "http://127.0.0.1:8080", c.proxy, 1)
		r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serve})
		if got := read(t, r, c.httpAddr); !got.Serving {
			t.Errorf("http_addr %q with proxy %q reported not serving", c.httpAddr, c.proxy)
		}
	}
}

// A routable http_addr is a different machine's address, and a serve that
// points somewhere else is not this dashboard.
func TestARoutableAddressMustMatchExactly(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveDashboard})
	if got := read(t, r, "192.168.1.40:8080"); got.Serving {
		t.Error("a serve at 127.0.0.1:8080 was read as serving 192.168.1.40:8080")
	}
}

func TestServingOverPlainHTTPOnPort80(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": servePlainHTTP})
	got := read(t, r, addr)
	if !got.Serving {
		t.Fatal("Serving is false although a handler proxies the dashboard")
	}
	if got.ServeURL != "http://stompwatch.example.ts.net/" {
		t.Errorf("ServeURL = %q, want \"http://stompwatch.example.ts.net/\"", got.ServeURL)
	}
}

// --- funnel ---

func TestFunnelOnTheDashboard(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveFunnel})
	got := read(t, r, addr)
	if !got.Serving {
		t.Error("Serving is false although a handler proxies the dashboard")
	}
	if !got.Funnel {
		t.Fatal("Funnel is false although AllowFunnel is on for the dashboard's host and port")
	}
}

func TestFunnelOnAnotherPortIsNotTheDashboard(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveFunnelElsewhere})
	got := read(t, r, addr)
	if !got.Serving {
		t.Error("Serving is false although port 443 proxies the dashboard")
	}
	if got.Funnel {
		t.Error("Funnel is true although the funnelled port carries something else")
	}
}

// A serve held in the foreground by a running session serves just the same,
// and so does its funnel.
func TestAForegroundServeCounts(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveForeground})
	got := read(t, r, addr)
	if !got.Serving {
		t.Error("Serving is false although a foreground session proxies the dashboard")
	}
	if got.ServeURL != "https://stompwatch.example.ts.net/dashboard" {
		t.Errorf("ServeURL = %q, want \"https://stompwatch.example.ts.net/dashboard\"", got.ServeURL)
	}
	if !got.Funnel {
		t.Error("Funnel is false although the foreground serve allows it")
	}
}

// --- key expiry ---

func TestKeyExpiryIsRead(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveNone})
	got := read(t, r, addr)
	want := time.Date(2026, 9, 27, 10, 28, 11, 0, time.UTC)
	if !got.KeyExpiry.Equal(want) {
		t.Fatalf("KeyExpiry = %v, want %v", got.KeyExpiry, want)
	}
}

func TestKeyExpiryDisabledIsZero(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusNoExpiry, "FAKE_SERVE": serveNone})
	got := read(t, r, addr)
	if !got.KeyExpiry.IsZero() {
		t.Fatalf("KeyExpiry = %v, want the zero time: the node's key does not expire", got.KeyExpiry)
	}
}

// --- failure is never a failure ---

func TestAHangingCommandTimesOut(t *testing.T) {
	r := fake(t, map[string]string{
		"FAKE_STATUS":  statusRunning,
		"FAKE_HANG_MS": "60000",
	})
	r.Timeout = 150 * time.Millisecond
	start := time.Now()
	got := read(t, r, addr)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Read took %v; a hung tailscale blocked the caller", d)
	}
	if got.Running || got.Serving || got.Funnel {
		t.Errorf("a hung command reported %+v", got)
	}
	if got.Err == "" {
		t.Error("Err is empty although the command never answered")
	}
}

func TestUnreadableJSONIsAnErrNotAPanic(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": "not json at all\n"})
	got := read(t, r, addr)
	if got.Running {
		t.Error("Running is true although the answer was not readable")
	}
	if got.Err == "" {
		t.Error("Err is empty although the answer was not readable")
	}
}

// The serve config being unreadable must not lose what the status said.
func TestAnUnreadableServeConfigKeepsTheNodeFacts(t *testing.T) {
	r := fake(t, map[string]string{
		"FAKE_STATUS":     statusRunning,
		"FAKE_SERVE":      "",
		"FAKE_SERVE_EXIT": "1",
		"FAKE_STDERR":     "access denied: serve config denied\n",
	})
	got := read(t, r, addr)
	if !got.Running {
		t.Error("Running is false although the status was readable")
	}
	if got.Name != "stompwatch.example.ts.net" {
		t.Errorf("Name = %q, want the node name from the status", got.Name)
	}
	if got.Serving || got.Funnel {
		t.Error("the serve config was unreadable, so nothing may be claimed about it")
	}
	if got.Err == "" {
		t.Error("Err is empty although the serve config could not be read")
	}
}

// --- nothing secret leaves this package ---

// tailscale status --json carries the node key, every peer's key, and the
// tailnet's user list. None of it may reach a Status field, and there must
// be no field that carries the answer through whole.
func TestNoKeyMaterialReachesTheStatus(t *testing.T) {
	r := fake(t, map[string]string{"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveDashboard})
	got := read(t, r, addr)
	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.String {
			continue
		}
		for _, secret := range []string{"nodekey:", "PublicKey", "abcdef0123456789", "fedcba9876543210", "owner@example.com", "nAAAAAAAAA1CNTRL"} {
			if strings.Contains(f.String(), secret) {
				t.Errorf("%s = %q carries %q from the raw answer", v.Type().Field(i).Name, f.String(), secret)
			}
		}
	}
	// Every field is one this package built. A raw-JSON field would let the
	// whole answer through whatever the checks above test.
	for i := 0; i < v.NumField(); i++ {
		switch name := v.Type().Field(i).Name; name {
		case "Installed", "Running", "Backend", "Name", "Serving", "ServeURL", "Funnel", "KeyExpiry", "Err":
		default:
			t.Errorf("Status has an unexpected field %q", name)
		}
	}
}

// --- what it actually ran ---

func TestReadRunsOnlyTheTwoReadingCommands(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	r := fake(t, map[string]string{
		"FAKE_STATUS": statusRunning, "FAKE_SERVE": serveDashboard, "FAKE_ARGV": argv,
	})
	read(t, r, addr)
	want := [][]string{
		{"status", "--json"},
		{"serve", "status", "--json"},
	}
	if got := argvRuns(t, argv); !reflect.DeepEqual(got, want) {
		t.Fatalf("Read ran %v, want %v", got, want)
	}
}

// FixCommand is printed, never run. It has to be the command that works.
func TestFixCommandNamesTheDashboardAddress(t *testing.T) {
	if got := FixCommand("127.0.0.1:8080"); got != "sudo tailscale serve --bg http://127.0.0.1:8080" {
		t.Fatalf("FixCommand = %q", got)
	}
}

// An address with no host listens on every interface, and tailscale serve
// reaches it on loopback. "http://:8080" is not a target tailscale accepts.
func TestFixCommandFillsInAnEmptyHost(t *testing.T) {
	if got := FixCommand(":8080"); got != "sudo tailscale serve --bg http://127.0.0.1:8080" {
		t.Fatalf("FixCommand(\":8080\") = %q", got)
	}
}

func argvRuns(t *testing.T, path string) [][]string {
	t.Helper()
	body, err := readFileOrEmpty(path)
	if err != nil {
		t.Fatal(err)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	var runs [][]string
	for _, block := range strings.Split(body, "\n\n") {
		runs = append(runs, strings.Split(block, "\n"))
	}
	return runs
}

var _ = context.Background

// FunnelOffCommand is printed, never run, and has to name the port the
// funnel is actually on.
func TestFunnelOffCommandNamesThePort(t *testing.T) {
	cases := []struct{ serveURL, want string }{
		{"https://stompwatch.example.ts.net/", "sudo tailscale funnel --https=443 off"},
		{"https://stompwatch.example.ts.net:8443/dashboard", "sudo tailscale funnel --https=8443 off"},
		{"", "sudo tailscale funnel --https=443 off"},
	}
	for _, c := range cases {
		if got := FunnelOffCommand(c.serveURL); got != c.want {
			t.Errorf("FunnelOffCommand(%q) = %q, want %q", c.serveURL, got, c.want)
		}
	}
}
