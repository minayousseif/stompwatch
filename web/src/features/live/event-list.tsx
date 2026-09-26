import { ChevronRightIcon } from "lucide-react"
import { Link } from "react-router-dom"

import { EmptyNote } from "@/components/state"
import type { NoiseEvent, ReviewStatus } from "@/lib/api"
import { formatDb, levelColor } from "@/lib/level"
import { formatClock, formatDuration } from "@/lib/time"
import { cn } from "@/lib/utils"

/** How the owner says each decision out loud. */
const STATUS_WORD: Record<ReviewStatus, string> = {
  verified: "Confirmed",
  rejected: "Rejected",
  unsure: "Unsure",
}

const STATUS_COLOR: Record<ReviewStatus, string> = {
  verified: "bg-verified",
  rejected: "bg-rejected",
  unsure: "bg-unsure",
}

/** A short list. The Events screen is where the whole history lives. */
const MOST_ROWS = 10

/**
 * The night's events, newest first. Each row opens the event, because the
 * clip is what settles the question.
 */
export function EventList({
  events,
  total,
  measured,
  from,
  nightTitle,
  allEventsHref,
}: {
  events: NoiseEvent[]
  /** How many events the range holds, before this list was shortened. */
  total: number
  /** False when nothing was measured in the range at all. */
  measured: boolean
  from: number
  /** "Tonight" or "Last night", so the empty state reads like speech. */
  nightTitle: string
  allEventsHref: string
}) {
  if (events.length === 0) {
    return measured ? (
      <EmptyNote
        className="px-4"
        headline={`No events ${nightTitle.toLowerCase()}.`}
        detail={`The meter has been measuring since ${formatClock(
          from,
        )} and nothing crossed the threshold.`}
      />
    ) : (
      <EmptyNote
        className="px-4"
        headline="Nothing was measured in this range."
        detail="The collector was not recording. System says what it found."
      />
    )
  }

  const shown = events.slice(0, MOST_ROWS)
  const rest = total - shown.length

  return (
    <>
      <ul className="divide-y divide-border">
        {shown.map((event) => (
          <li key={event.id}>
            {/* Two lines at every width. Nothing has to be cut off on a
                phone, which is where a good share of review happens. */}
            <Link
              to={`/events/${event.id}`}
              className="flex items-center gap-3 px-4 py-3 hover:bg-accent focus-visible:bg-accent"
            >
              <span
                className="size-2 shrink-0 rounded-full"
                style={{ backgroundColor: levelColor(event.lamax) }}
                aria-hidden
              />
              <span className="min-w-0 flex-1">
                <span className="flex items-baseline gap-2">
                  <span className="text-sm">{formatClock(event.started_ms)}</span>
                  <span className="truncate text-sm capitalize">{event.class}</span>
                </span>
                <span className="mt-0.5 flex items-center gap-3 text-xs text-muted-foreground">
                  <span>{formatDuration(event.duration_ms)}</span>
                  <ReviewMark status={event.review?.status ?? null} />
                </span>
              </span>
              <span className="shrink-0 text-sm">
                {formatDb(event.lamax)} <span className="text-xs text-muted-foreground">dB</span>
              </span>
              <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
            </Link>
          </li>
        ))}
      </ul>
      {rest > 0 && (
        <p className="border-t border-border px-4 py-3 text-sm text-muted-foreground">
          {rest} more in this range.{" "}
          <Link to={allEventsHref} className="text-foreground underline-offset-4 hover:underline">
            See them all
          </Link>
        </p>
      )}
    </>
  )
}

/**
 * The mark on an event a mute window covers. It sits beside the review mark
 * because the two answer different questions: the review says whether the
 * noise was real, this says the owner already knows what it was.
 */
export function MutedMark({ className }: { className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-xs border border-border px-1.5 py-px text-[11px] leading-4 text-muted-foreground",
        className,
      )}
      title="A mute window covers this event. It is left out of the counts and the export."
    >
      Muted
    </span>
  )
}

/** The review decision. This and the level ramp are the only color here. */
export function ReviewMark({ status }: { status: ReviewStatus | null }) {
  if (!status) {
    return <span className="truncate text-xs text-muted-foreground">Not reviewed</span>
  }
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-xs">
      <span className={cn("size-1.5 shrink-0 rounded-full", STATUS_COLOR[status])} aria-hidden />
      <span className="truncate text-muted-foreground">{STATUS_WORD[status]}</span>
    </span>
  )
}
