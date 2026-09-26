import { Link } from "react-router-dom"

import type { Summary } from "@/lib/api"
import { formatDb } from "@/lib/level"
import { formatClock } from "@/lib/time"
import { cn } from "@/lib/utils"

/**
 * The five facts the trace cannot tell you: how many events there were, how
 * many fell in quiet hours, how many still need a decision, the loudest one,
 * and the average level for the night.
 *
 * One strip on one surface, not a row of cards. Nothing here is tinted.
 */
export function StatStrip({ summary, mutedHref }: { summary: Summary; mutedHref: string }) {
  const waiting = Math.max(0, summary.events - summary.reviewed)

  return (
    <>
      {/* The gap is the border color showing through, so every cell is
        divided from its neighbors at any width. */}
      <dl className="grid grid-cols-2 gap-px bg-border sm:grid-cols-3 lg:grid-cols-5">
        <Stat label="Events" value={String(summary.events)} />
        <Stat label="In quiet hours" value={String(summary.quiet_hour_events)} />
        <Stat
          label="Waiting for a decision"
          value={String(waiting)}
          note={waiting === 0 && summary.events > 0 ? "all reviewed" : undefined}
        />
        <Stat
          label="Loudest"
          value={summary.loudest ? `${formatDb(summary.loudest.lamax)} dB` : "-"}
          note={summary.loudest ? `at ${formatClock(summary.loudest.started_ms)}` : undefined}
        />
        <Stat
          label="Average level"
          value={summary.laeq === null ? "-" : `${formatDb(summary.laeq)} dB`}
          note={summary.laeq === null ? "nothing measured" : "LAeq for the range"}
          // The odd one out fills the last row rather than leaving a blank cell.
          className="col-span-2 sm:col-span-1"
        />
      </dl>

      {/* A mute window must never make a night read as quiet. The count says
        what was left out, and the link shows exactly which events. */}
      {summary.muted > 0 && (
        <p className="border-t border-border px-4 py-3 text-sm text-muted-foreground lg:px-5">
          {summary.muted === 1 ? "One event is" : `${summary.muted} events are`} muted and left out
          of these counts.{" "}
          <Link to={mutedHref} className="text-foreground underline-offset-4 hover:underline">
            See which
          </Link>
        </p>
      )}
    </>
  )
}

function Stat({
  label,
  value,
  note,
  className,
}: {
  label: string
  value: string
  note?: string
  className?: string
}) {
  return (
    <div className={cn("bg-card px-4 py-3 lg:px-5", className)}>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-xl leading-tight">{value}</dd>
      {note && <p className="mt-0.5 text-xs text-muted-foreground">{note}</p>}
    </div>
  )
}
