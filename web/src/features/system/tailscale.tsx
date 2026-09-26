import { useState } from "react"

import { Fact, Group } from "@/components/state"
import { Button } from "@/components/ui/button"
import type { Tailscale } from "@/lib/api"
import { formatDay } from "@/lib/time"
import { cn } from "@/lib/utils"

/**
 * The command that puts the dashboard behind `tailscale serve`. The server
 * says the same sentence at startup. It is shown, never run: setting
 * Tailscale up is the owner's job, and a service that could do it would
 * need to be a Tailscale operator (SPEC.md section 9.0).
 */
export function serveCommand(httpAddr: string): string {
  return `sudo tailscale serve --bg http://${httpAddr.startsWith(":") ? "127.0.0.1" + httpAddr : httpAddr}`
}

/** The command that takes the dashboard back off the public internet. */
export function funnelOffCommand(serveURL: string): string {
  let port = "443"
  try {
    const parsed = new URL(serveURL)
    if (parsed.port) port = parsed.port
  } catch {
    // An address that will not parse leaves the default port, which is the
    // one Funnel uses unless it was set up otherwise.
  }
  return `sudo tailscale funnel --https=${port} off`
}

/**
 * The second line under State, when it says more than the value already
 * does. Tailscale reports NeedsLogin, NeedsMachineAuth, Starting and so on,
 * and each of those is something to act on; "running" under "Running" is
 * noise, and noise is what teaches somebody to stop reading the screen.
 */
function backendNote(ts: Tailscale): string | undefined {
  if (!ts.installed) return "the box is reached some other way"
  // NeedsMachineAuth reads as "needs machine auth".
  const words = ts.backend.replace(/([a-z])([A-Z])/g, "$1 $2").toLowerCase()
  if (words === "" || words === "running" || words === "stopped") return undefined
  return words
}

/** How the box is reached from outside, in the same shape as Capture. */
export function TailscaleFacts({ ts }: { ts: Tailscale }) {
  return (
    <Group title="Tailscale">
      <Fact
        label="State"
        value={!ts.installed ? "Not installed" : ts.running ? "Running" : "Not running"}
        note={backendNote(ts)}
        quiet={!ts.installed}
      />
      {/* A MagicDNS name is one long word with no space to break at, so it
          is allowed to break anywhere rather than push its own label off
          the row at 375 px. */}
      {ts.name !== "" && <Fact label="Node" value={<span className="break-all">{ts.name}</span>} />}
      <Fact
        label="Address"
        value={
          ts.serving && ts.serve_url !== "" ? (
            <a className="break-all underline underline-offset-2" href={ts.serve_url}>
              {ts.serve_url}
            </a>
          ) : (
            "Not served over Tailscale"
          )
        }
        note={ts.serving ? "open this from any device on the tailnet" : undefined}
        quiet={!ts.serving}
      />
      {/* Key expiry and Funnel are read off a running tailscaled. With
          nothing answering they are not known, and "Disabled" would be a
          claim nobody measured. */}
      {ts.running && (
        <Fact
          label="Key expiry"
          value={ts.key_expiry_ms === 0 ? "Disabled" : formatDay(ts.key_expiry_ms)}
          note={
            ts.key_expiry_ms === 0
              ? "the node stays on the tailnet"
              : "the box drops off the tailnet on this date"
          }
          quiet={ts.key_expiry_ms === 0}
        />
      )}
      {/* The one reading on this screen that is set in color. Funnel being
          on is a fault, and section 9.2 allows color for a fault. */}
      {ts.running && (
        <Fact
          label="Funnel"
          value={ts.funnel ? <span className="font-medium text-destructive">On</span> : "Off"}
          note={ts.funnel ? "the public internet can reach this box" : undefined}
          quiet={!ts.funnel}
        />
      )}
    </Group>
  )
}

/**
 * What to do about Tailscale, when there is something to do. Nothing is
 * shown when the dashboard is served and Funnel is off, because then there
 * is nothing to say.
 *
 * Funnel is the one place color is used outside the level ramp. It is a
 * fault, not a reading: Funnel publishes the box to the public internet,
 * and `auth_mode` trusts an identity header that is only safe while
 * `tailscale serve` is the sole path to the loopback listener
 * (SPEC.md section 9.0).
 */
export function TailscaleNotice({ ts }: { ts: Tailscale }) {
  if (ts.funnel) {
    return (
      <Notice tone="fault" heading="The dashboard is published to the public internet.">
        <p>
          Tailscale Funnel is on for {ts.serve_url || "this node"}. This dashboard has no login of
          its own: it trusts the identity header <code>Tailscale-User-Login</code>, which is only
          safe while <code>tailscale serve</code> is the only way in. With Funnel on, anyone on the
          internet reaches the same pages.
        </p>
        <p>Turn it off on the box:</p>
        <CopyCommand command={funnelOffCommand(ts.serve_url)} />
      </Notice>
    )
  }

  if (ts.installed && ts.running && !ts.serving && ts.err === "") {
    return (
      <Notice tone="quiet" heading="Nothing serves the dashboard over Tailscale.">
        <p>
          Tailscale is up on this box, but no serve target points at {ts.http_addr}. The dashboard
          is still reachable on the box itself, and through the reverse proxy if that is set up. Run
          this on the box to reach it from your other devices:
        </p>
        <CopyCommand command={serveCommand(ts.http_addr)} />
      </Notice>
    )
  }

  if (!ts.installed) {
    return (
      <Notice tone="quiet" heading="Tailscale is not installed on this box.">
        <p>
          That is not a fault. The dashboard is reached some other way: on the box itself, or
          through the reverse proxy on the LAN. Nothing in the collector depends on it, so
          measurement, detection, clips and the database carry on either way.
        </p>
      </Notice>
    )
  }

  if (ts.err !== "") {
    return (
      <Notice tone="quiet" heading="Tailscale could not be read in full.">
        <p>{ts.err}</p>
        <p>
          Nothing in the collector depends on this. Measurement, detection, clips and the database
          carry on whatever Tailscale is doing.
        </p>
      </Notice>
    )
  }

  return null
}

function Notice({
  tone,
  heading,
  children,
}: {
  tone: "fault" | "quiet"
  heading: string
  children: React.ReactNode
}) {
  const fault = tone === "fault"
  return (
    <div
      className={cn(
        "flex max-w-prose flex-col gap-2 border p-3 text-sm",
        fault ? "border-destructive text-destructive" : "border-border text-muted-foreground",
      )}
      role={fault ? "alert" : undefined}
    >
      <p className={cn("font-medium", !fault && "text-foreground")}>{heading}</p>
      {children}
    </div>
  )
}

/**
 * A command to run on the box, with a button that copies it. The command is
 * never sent anywhere: this dashboard does not configure Tailscale.
 */
export function CopyCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // A browser that refuses the clipboard leaves the text on screen to
      // select by hand, which is the whole point of showing it.
      setCopied(false)
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <code className="min-w-0 flex-1 overflow-x-auto border border-border bg-muted px-2 py-1 font-mono text-xs whitespace-pre text-foreground">
        {command}
      </code>
      <Button variant="outline" size="sm" className="h-7 text-xs" onClick={copy}>
        {copied ? "Copied" : "Copy"}
      </Button>
    </div>
  )
}
