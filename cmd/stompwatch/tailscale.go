package main

import (
	"log/slog"
	"time"

	"github.com/minayousseif/stompwatch/internal/store"
	"github.com/minayousseif/stompwatch/internal/tailnet"
)

// funnelMessage is what Funnel being on means, in one sentence. The log
// line, the health row and the failure ping all say the same thing, so the
// owner reads one story wherever they meet it.
const funnelMessage = "the dashboard is published to the public internet by Tailscale Funnel"

// reportTailscale says, once at startup, what Tailscale is doing with the
// dashboard. It is called after the dashboard's own listening line, so the
// log reads in the order the owner set it up.
//
// Only Funnel is a fault. A box with no Tailscale is a normal box: the
// owner may be reaching the dashboard through the Nginx path in
// SPEC.md section 9.0 instead, and nothing in the collector depends on
// either (SPEC.md section 9.0.1).
func reportTailscale(st tailnet.Status, httpAddr string, log *slog.Logger,
	record func(at time.Time, kind, detail string, d time.Duration), alert func(string)) {
	switch {
	case !st.Installed:
		log.Info("Tailscale is not installed on this box, so nothing here reaches the dashboard over it",
			"http_addr", httpAddr,
			"detail", st.Err,
			"note", "that is not a fault; the reverse proxy is the other way in")
	case !st.Running:
		log.Info("Tailscale is installed but tailscaled is not serving this node",
			"backend", st.Backend, "detail", st.Err)
	case st.Serving:
		log.Info("the dashboard is reachable over Tailscale", "url", st.ServeURL, "node", st.Name)
	case st.Err != "":
		// Not the same as nothing serving the dashboard. Telling the owner
		// to run a command they may not need is worse than saying so.
		log.Warn("Tailscale is up, but what it serves could not be read",
			"node", st.Name, "detail", st.Err)
	default:
		log.Warn("Tailscale is up, but nothing serves the dashboard",
			"node", st.Name, "http_addr", httpAddr,
			"fix", tailnet.FixCommand(httpAddr))
	}

	// Funnel publishes the node to the public internet. auth_mode trusts
	// the Tailscale-User-Login header, which is only safe while
	// tailscale serve is the sole path to a loopback listener
	// (SPEC.md section 9.0). With Funnel on, that trust is misplaced and
	// anyone on the internet is whoever they say they are.
	if st.Funnel {
		log.Error(funnelMessage,
			"url", st.ServeURL, "node", st.Name,
			"why_it_matters", "auth_mode trusts the Tailscale-User-Login header, which is only safe while tailscale serve is the sole path to the loopback listener",
			"fix", tailnet.FunnelOffCommand(st.ServeURL))
		record(time.Now(), store.HealthTailscaleFunnel,
			funnelMessage+" at "+st.ServeURL+
				"; auth_mode trusts an identity header that is only safe behind tailscale serve. Turn it off with "+
				tailnet.FunnelOffCommand(st.ServeURL), 0)
		alert(funnelMessage + ". " + tailnet.FunnelOffCommand(st.ServeURL))
	}

	// Warned once, at start, and never on a timer: it is a setting, not a
	// reading. A node whose key silently expires drops off the tailnet and
	// the dashboard becomes unreachable with no obvious cause, which looks
	// exactly like the box being dead (SPEC.md section 9.0).
	if !st.KeyExpiry.IsZero() {
		log.Warn("this node's Tailscale key expires, and the dashboard goes unreachable when it does",
			"expires", st.KeyExpiry.UTC().Format(time.RFC3339),
			"effect", "the box drops off the tailnet and looks exactly as if it were dead",
			"fix", "disable key expiry on this node in the Tailscale admin console, or use an auth key that does not expire")
	}
}
