// Package web serves the dashboard: the embedded interface and the JSON API
// it reads.
//
// Trust model. This program has no login system and holds no password. The
// listener is bound to loopback, so the only way in is through something that
// has already authenticated the caller: tailscale serve over WireGuard, or a
// reverse proxy that authenticates users. The identity in the request headers
// is therefore read, never checked. It is recorded on each review row so the
// audit trail says who marked what, and it is shown on the system page. In
// the tailscale and trusted_header modes an API request with no login header
// is refused, and a Tailscale Funnel request is refused in every mode.
//
// Reads go through the store's read-only pool, so a slow dashboard query can
// never take a lock the collector's writer is waiting for. Nothing in this
// package is on the measurement path: the live meter is fed by a LiveFeed
// that drops updates rather than making the DSP goroutine wait.
//
// Errors say what happened and what to do, in plain English. They never name
// a file on disk: the owner reads them on a phone, and a path is both useless
// there and more than a response needs to say.
package web
