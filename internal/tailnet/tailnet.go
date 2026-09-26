// Package tailnet reads what Tailscale says about this node.
//
// It only reads. Nothing here configures Tailscale, and nothing here may.
// Setting up `tailscale serve` is the owner's job (SPEC.md section 9.0,
// "README, not code's job"): doing it from the service would need the
// service account made a Tailscale operator, which is a large privilege for
// a process whose whole security story is that it listens on loopback.
//
// Nothing Tailscale says is ever a reason to fail. Read returns no error: a
// box with no Tailscale, a tailscaled that will not answer, and a command
// that hangs are all valid answers, and capture, detection, clips and
// commits carry on regardless (SPEC.md section 9.0.1).
//
// `tailscale status --json` carries the node key, every peer's key, and the
// tailnet's user list. Nothing in this package passes that answer on: it is
// decoded into the few fields below and the rest is dropped.
package tailnet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Status is what Tailscale says about this node. Every field is best
// effort: a box with no Tailscale is a valid answer, not a failure.
type Status struct {
	Installed bool      // the tailscale command is on PATH
	Running   bool      // tailscaled answered
	Backend   string    // Running, Stopped, NeedsLogin, and so on
	Name      string    // the node's MagicDNS name, without the trailing dot
	Serving   bool      // a serve config points at the dashboard's address
	ServeURL  string    // what to open, when Serving
	Funnel    bool      // the dashboard is published to the public internet
	KeyExpiry time.Time // zero when expiry is disabled
	Err       string    // why the answer is incomplete, in plain words
}

// allowed is every tailscale subcommand this package may ever run. Both
// only read. run refuses anything else, and a test states this set out
// literally, so a later change cannot quietly add a writing one.
var allowed = [][]string{
	{"status", "--json"},
	{"serve", "status", "--json"},
}

// callTimeout bounds one call. tailscale answers a local socket, so it is
// either quick or never; waiting longer only delays the caller. Read makes
// two calls, so it costs at most twice this.
const callTimeout = 3 * time.Second

// Budget is the longest Read can take: two calls, each bounded by its own
// timeout. The collector reads Tailscale in a goroutine of its own, and
// this is how long that goroutine can live.
const Budget = 2 * callTimeout

// Reader runs the tailscale command. The zero value reads the tailscale on
// PATH, which is what the collector uses; the fields are here so that a
// test can stand a program of its own in its place.
type Reader struct {
	// Command is the program and any leading arguments. nil means the
	// tailscale on PATH.
	Command []string
	// Env is extra environment for the child. Tests use it.
	Env []string
	// Timeout bounds one call. Zero means callTimeout.
	Timeout time.Duration
}

// Read asks Tailscale about this node and about what serves httpAddr, the
// dashboard's listen address. It never returns an error: an answer it could
// not get is a Status with Err set.
func Read(ctx context.Context, httpAddr string) Status { return Reader{}.Read(ctx, httpAddr) }

// FixCommand is the command that puts the dashboard behind tailscale serve.
// It is printed for the owner to run and is never run from here. An address
// with no host listens on every interface, loopback included, and "http://:"
// is not a target tailscale accepts, so the host becomes 127.0.0.1.
func FixCommand(httpAddr string) string {
	if strings.HasPrefix(httpAddr, ":") {
		httpAddr = "127.0.0.1" + httpAddr
	}
	return "sudo tailscale serve --bg http://" + httpAddr
}

func (r Reader) Read(ctx context.Context, httpAddr string) Status {
	var st Status
	if _, err := exec.LookPath(r.program()); err != nil {
		st.Err = "tailscale is not installed on this box"
		return st
	}
	st.Installed = true

	out, errText, err := r.run(ctx, "status", "--json")
	var node nodeStatus
	if jsonErr := json.Unmarshal([]byte(out), &node); jsonErr != nil {
		// tailscaled that is not running prints a sentence, not JSON, and
		// the sentence is the useful part. The JSON itself never is: it
		// carries key material, so it is not repeated back.
		st.Err = reason("tailscale status could not be read", errText, err)
		return st
	}
	st.Backend = node.BackendState
	st.Running = node.BackendState == "Running"
	st.Name = strings.TrimSuffix(node.Self.DNSName, ".")
	if node.Self.KeyExpiry != nil {
		st.KeyExpiry = *node.Self.KeyExpiry
	}
	if !st.Running {
		return st
	}

	out, errText, err = r.run(ctx, "serve", "status", "--json")
	var serve serveConfig
	if jsonErr := json.Unmarshal([]byte(out), &serve); jsonErr != nil {
		st.Err = reason("the tailscale serve config could not be read", errText, err)
		return st
	}
	st.Serving, st.ServeURL, st.Funnel = serve.find(httpAddr)
	return st
}

// --- running the command ---

func (r Reader) program() string {
	if len(r.Command) == 0 {
		return "tailscale"
	}
	return r.Command[0]
}

// run starts one of the allowed commands and returns its output. An
// argument list that is not in allowed is refused before anything starts.
func (r Reader) run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	if !slices.ContainsFunc(allowed, func(a []string) bool { return slices.Equal(a, args) }) {
		return "", "", fmt.Errorf("tailnet: %q is not one of the read-only tailscale commands", strings.Join(args, " "))
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = callTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	full := append(slices.Clone(r.Command[min(1, len(r.Command)):]), args...)
	c := exec.CommandContext(cctx, r.program(), full...)
	if len(r.Env) > 0 {
		c.Env = append(c.Environ(), r.Env...)
	}
	var out, errOut limited
	c.Stdout, c.Stderr = &out, &errOut
	err = c.Run()
	return out.String(), errOut.String(), err
}

// reason turns a failed call into one sentence for the owner. It carries
// the command's own words, which say whether tailscaled is down or the
// caller lacks permission, and never the command's JSON.
func reason(what, stderr string, err error) string {
	if line := lastLine(stderr); line != "" {
		return what + ": " + line
	}
	if err != nil {
		return what + ": " + err.Error()
	}
	return what
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// limited keeps the first 64 KB written to it. A status answer on a large
// tailnet is bigger than anything worth holding in memory here.
type limited struct{ b []byte }

func (l *limited) Write(p []byte) (int, error) {
	if room := 64 * 1024; len(l.b) < room {
		l.b = append(l.b, p[:min(room-len(l.b), len(p))]...)
	}
	return len(p), nil
}

func (l *limited) String() string { return string(l.b) }

// --- what is read out of the answers ---

// nodeStatus is the little of `tailscale status --json` this package reads.
// Every other field, the keys and the peer list included, is dropped by
// encoding/json before anything here can pass it on.
type nodeStatus struct {
	BackendState string
	Self         struct {
		DNSName string
		// KeyExpiry is absent when key expiry is disabled on the node,
		// which is what SPEC.md section 9.0 asks the owner to do. A
		// pointer is how "absent" is told from "expires at the zero time".
		KeyExpiry *time.Time
	}
}

// serveConfig is the little of `tailscale serve status --json` this package
// reads. Web maps "host:port" to the handlers on it, AllowFunnel says which
// of those host and port pairs are published to the public internet, and
// TCP says whether the port terminates HTTPS or plain HTTP.
//
// Foreground holds the config of a `tailscale serve` run without --bg, one
// entry per session holding it. Such a serve is serving while its session
// runs, so it is searched too.
type serveConfig struct {
	TCP map[string]struct {
		HTTPS bool
		HTTP  bool
	}
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string
		}
	}
	AllowFunnel map[string]bool
	Foreground  map[string]*serveConfig
}

// find reports whether anything in this serve config proxies the dashboard,
// what to open when it does, and whether that is published to the public
// internet.
//
// How the match is made: the ports must be equal, and the hosts must name
// the same machine. tailscale writes the target back in its own form, and
// the owner may have typed it in another, so `http://127.0.0.1:8080`,
// `http://localhost:8080` and `127.0.0.1:8080` all describe the dashboard
// when http_addr is `127.0.0.1:8080` or `localhost:8080` or `:8080`. Every
// loopback name is therefore treated as one name. When http_addr names a
// routable address instead, the host must match exactly: a serve pointing
// at another machine is not this dashboard, and saying it is would hide the
// fact that nothing serves this one.
//
// A near miss costs the owner a command they do not need, so the loose
// reading is the right way round here.
func (s *serveConfig) find(httpAddr string) (serving bool, url string, funnel bool) {
	if s == nil {
		return false, "", false
	}
	for hostPort, web := range s.Web {
		for path, h := range web.Handlers {
			if !sameAddr(h.Proxy, httpAddr) {
				continue
			}
			serving = true
			if s.AllowFunnel[hostPort] {
				funnel = true
			}
			if u := s.url(hostPort, path); url == "" || funnel {
				url = u
			}
		}
	}
	for _, fg := range s.Foreground {
		fgServing, fgURL, fgFunnel := fg.find(httpAddr)
		if !fgServing {
			continue
		}
		serving = true
		funnel = funnel || fgFunnel
		if url == "" {
			url = fgURL
		}
	}
	return serving, url, funnel
}

// url is what the owner opens for one handler. The scheme is https unless
// the port is set up for plain HTTP, and the port is left off when it is
// the default one for the scheme.
func (s *serveConfig) url(hostPort, path string) string {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		host, port = hostPort, ""
	}
	scheme := "https"
	if tcp, ok := s.TCP[port]; ok && tcp.HTTP && !tcp.HTTPS {
		scheme = "http"
	}
	out := scheme + "://" + host
	if (scheme == "https" && port != "443") || (scheme == "http" && port != "80") {
		if port != "" {
			out += ":" + port
		}
	}
	if !strings.HasPrefix(path, "/") {
		out += "/"
	}
	return out + path
}

// sameAddr reports whether a serve target points at the dashboard's listen
// address. See find for how the match is made and why.
func sameAddr(target, httpAddr string) bool {
	tHost, tPort, ok := splitTarget(target)
	if !ok {
		return false
	}
	aHost, aPort, ok := splitTarget(httpAddr)
	if !ok || tPort == "" || tPort != aPort {
		return false
	}
	if loopback(aHost) {
		return loopback(tHost)
	}
	return tHost == aHost
}

// splitTarget reads a host and port out of a serve target or a listen
// address. The target may carry a scheme and a path, as
// `http://127.0.0.1:8080/` does, or neither, as `127.0.0.1:8080` does.
func splitTarget(s string) (host, port string, ok bool) {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", "", false
	}
	return host, port, true
}

// loopback reports whether a host name in a listen address or a serve
// target is a loopback name. An empty host is the wildcard form of
// http_addr, `:8080`. That listener binds every interface, so it is not
// loopback-only and the collector warns about it at start; but it does
// answer on loopback too, so a loopback serve target reaches it, and it is
// matched here as one.
func loopback(host string) bool {
	switch strings.ToLower(host) {
	case "", "localhost":
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// FunnelOffCommand is the command that takes the dashboard back off the
// public internet, for the port the serve URL names. It is printed for the
// owner to run and is never run from here.
func FunnelOffCommand(serveURL string) string {
	port := "443"
	if _, p, ok := splitTarget(serveURL); ok && p != "" {
		port = p
	}
	return "sudo tailscale funnel --https=" + port + " off"
}
