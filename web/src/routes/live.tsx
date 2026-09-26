import { useCallback, useMemo, useState } from "react"
import { Link } from "react-router-dom"

import { Screen } from "@/components/app-shell"
import { SiteHeader } from "@/components/site-header"
import { EmptyNote, ErrorNote, LoadingNote, Panel } from "@/components/state"
import { Button } from "@/components/ui/button"
import { EventList } from "@/features/live/event-list"
import { LiveMeter } from "@/features/live/live-meter"
import { NightTimeline, TimelineKey } from "@/features/live/timeline"
import { StatStrip } from "@/features/live/stat-strip"
import { useInterval } from "@/hooks/use-interval"
import { useResource } from "@/hooks/use-resource"
import { api } from "@/lib/api"
import { formatRange, nightWindow, NIGHT_RULE } from "@/lib/time"

/** How often the night's numbers catch up with the live meter. */
const REFRESH_MS = 60_000

export function LiveScreen() {
  // The window is worked out once per render pass and re-worked on reload, so
  // a page left open overnight does not keep showing yesterday.
  const [tick, setTick] = useState(0)
  const night = useMemo(() => nightWindow(), [tick])
  const range = { from: night.from, to: night.to }

  const summary = useResource((signal) => api.summary(range, signal), [range.from, range.to, tick])

  // `auto` serves any range up to 24 hours from the seconds, thinned into
  // buckets, and a night is always shorter than that. Seconds carry the
  // baseline through the thinning; the minute rollup does not store one.
  const timeline = useResource(
    (signal) => api.timeline(range, "auto", signal),
    [range.from, range.to, tick],
  )

  // Muted events are out of this list, the same way the summary leaves them
  // out of its counts. The strip says how many, so the night never reads as
  // quieter than it was.
  const events = useResource(
    (signal) =>
      api.events({ ...range, sort: "started_ms", order: "desc", muted: 0, limit: 50 }, signal),
    [range.from, range.to, tick],
  )

  const reload = useCallback(() => setTick((n) => n + 1), [])

  useInterval(reload, REFRESH_MS)

  const measured = summary.data ? summary.data.laeq !== null : true
  // The Events screen reads the same range from the query string, so a jump
  // from here lands on the night that was on screen.
  const allEvents = `/events?from=${night.from}&to=${night.to}`
  const mutedEvents = `${allEvents}&muted=1`

  return (
    <>
      {/* The screen is called Live. The night it is showing is said beside
          the name, because "tonight" and "last night" are the same window a
          few hours apart and the owner needs to know which one is on screen. */}
      <SiteHeader title="Live" aside={`${night.title}, ${formatRange(night)}`} />
      <Screen>
        <p className="text-xs text-muted-foreground">{NIGHT_RULE}</p>

        <LiveMeter />

        <Panel
          title="Level through the night"
          aside={
            <Button variant="ghost" size="sm" className="-my-1 h-7 text-xs" onClick={reload}>
              Refresh
            </Button>
          }
        >
          {timeline.error ? (
            <ErrorNote error={timeline.error} onRetry={reload} />
          ) : timeline.data ? (
            timeline.data.points.length === 0 ? (
              <EmptyNote
                headline="Nothing was measured in this range."
                detail="The trace starts as soon as the collector writes a second."
              />
            ) : (
              <>
                <NightTimeline timeline={timeline.data} />
                <TimelineKey
                  quietHours={timeline.data.quiet.length > 0}
                  baseline={timeline.data.points.some(
                    (point) => point.baseline !== null && point.baseline !== undefined,
                  )}
                />
              </>
            )
          ) : timeline.loading ? (
            <LoadingNote what="the night" />
          ) : null}
        </Panel>

        <Panel bodyClassName="p-0">
          {summary.error ? (
            <ErrorNote error={summary.error} onRetry={reload} className="px-4" />
          ) : summary.data ? (
            <StatStrip summary={summary.data} mutedHref={mutedEvents} />
          ) : summary.loading ? (
            <LoadingNote what="the summary" className="px-4" />
          ) : null}
        </Panel>

        <Panel
          title="Events"
          bodyClassName="p-0"
          aside={
            <Link to={allEvents} className="underline-offset-4 hover:underline">
              All events
            </Link>
          }
        >
          {events.error ? (
            <ErrorNote error={events.error} onRetry={reload} className="px-4" />
          ) : events.data ? (
            <EventList
              events={events.data.events}
              total={events.data.total}
              measured={measured}
              from={night.from}
              nightTitle={night.title}
              allEventsHref={allEvents}
            />
          ) : events.loading ? (
            <LoadingNote what={`${night.title.toLowerCase()}'s events`} className="px-4" />
          ) : null}
        </Panel>
      </Screen>
    </>
  )
}
