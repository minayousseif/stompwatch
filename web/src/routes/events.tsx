import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useNavigate, useSearchParams } from "react-router-dom"
import { SlidersHorizontalIcon } from "lucide-react"

import { Screen } from "@/components/app-shell"
import { SiteHeader } from "@/components/site-header"
import { EmptyNote, ErrorNote, LoadingNote, Panel } from "@/components/state"
import { Button } from "@/components/ui/button"
import { EventFilters } from "@/features/events/filters"
import { EventsTable } from "@/features/events/events-table"
import { SelectionBar } from "@/features/purge/selection-bar"
import {
  ShortcutButton,
  ShortcutHelp,
  ShortcutHint,
  useReviewKeys,
} from "@/features/review/shortcuts"
import { useReviewDesk } from "@/features/review/use-review"
import { useResource } from "@/hooks/use-resource"
import { api, type EventQuery } from "@/lib/api"
import {
  eventHref,
  isFiltered,
  mutedChoice,
  queryKey,
  readQuery,
  unreviewedQuery,
  withMuted,
  writeQuery,
  type SortKey,
} from "@/lib/query"
import { formatDayAndClock } from "@/lib/time"

export function EventsScreen() {
  const [search, setSearch] = useSearchParams()
  const navigate = useNavigate()
  const desk = useReviewDesk()
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const [showKeys, setShowKeys] = useState(false)

  const query = useMemo(() => readQuery(search), [search])
  const key = queryKey(query)

  // The filter bar opens by itself when the URL already carries a filter, so
  // a reloaded view says what it is showing rather than looking like all of it.
  const [showFilters, setShowFilters] = useState(() => isFiltered(query))

  const page = useResource((signal) => api.events(query, signal), [key])
  const events = page.data?.events ?? []
  const total = page.data?.total ?? 0

  // How many events this range holds that a mute window covers. The list
  // hides them, so it has to say they exist; a filter nobody can see is a
  // way to believe an evening was quiet when it was not.
  const hidingMuted = mutedChoice(query) === "hide"
  const muted = useResource(
    (signal) =>
      hidingMuted
        ? api.events({ ...query, muted: 1, limit: 1, offset: 0 }, signal).then((p) => p.total)
        : Promise.resolve(0),
    [key, hidingMuted],
  )

  const [selectedId, setSelectedId] = useState<number | null>(null)

  // The events ticked for a bulk action. A new page or a new filter clears
  // it: a selection the owner cannot see is a selection they did not make.
  const [ticked, setTicked] = useState<Set<number>>(() => new Set())
  useEffect(() => setTicked(new Set()), [key])

  const tick = useCallback((id: number, on: boolean) => {
    setTicked((was) => {
      const next = new Set(was)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }, [])

  const tickPage = useCallback(
    (on: boolean) => setTicked(on ? new Set(events.map((one) => one.id)) : new Set()),
    [events],
  )

  // A new page of rows needs a selection, or the keys have nothing to act on.
  useEffect(() => {
    if (events.length === 0) {
      setSelectedId(null)
      return
    }
    setSelectedId((current) =>
      current !== null && events.some((e) => e.id === current) ? current : events[0].id,
    )
  }, [events])

  const setQuery = useCallback(
    (next: EventQuery) => setSearch(writeQuery(next), { replace: true }),
    [setSearch],
  )

  const move = useCallback(
    (step: number) => {
      if (events.length === 0) return
      const index = events.findIndex((e) => e.id === selectedId)
      const next = Math.min(events.length - 1, Math.max(0, (index < 0 ? 0 : index) + step))
      setSelectedId(events[next].id)
    },
    [events, selectedId],
  )

  const selected = events.find((e) => e.id === selectedId) ?? null

  useReviewKeys(
    useMemo(
      () => ({
        next: () => move(1),
        previous: () => move(-1),
        confirm: () => selected && desk.mark(selected, "verified"),
        reject: () => selected && desk.mark(selected, "rejected"),
        unsure: () => selected && desk.mark(selected, "unsure"),
        clear: () => selected && desk.clear(selected),
        note: () => noteRef.current?.focus(),
        open: () => selected && navigate(eventHref(selected.id, query)),
        help: () => setShowKeys(true),
      }),
      [desk, move, navigate, query, selected],
    ),
    !showKeys,
  )

  const first = total === 0 ? 0 : (query.offset ?? 0) + 1
  const last = (query.offset ?? 0) + events.length
  const onlyUnreviewed = query.status?.length === 1 && query.status[0] === "none"

  return (
    <>
      <SiteHeader title="Events" aside={page.data ? countLine(total, query) : undefined} />
      <Screen>
        <Panel
          title="Filters"
          aside={
            <div className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="sm"
                className="-my-1 h-7 gap-1.5 text-xs"
                onClick={() => setShowFilters((open) => !open)}
                aria-expanded={showFilters}
              >
                <SlidersHorizontalIcon className="size-3.5" aria-hidden />
                {showFilters ? "Hide" : "Show"}
              </Button>
            </div>
          }
          bodyClassName={showFilters ? "p-4" : "px-4 py-2.5"}
        >
          {showFilters ? (
            <EventFilters query={query} onChange={setQuery} />
          ) : (
            <p className="text-xs text-muted-foreground">
              {isFiltered(query)
                ? "Some events are filtered out. Open the filters to see which."
                : "Every event, newest first."}
            </p>
          )}
        </Panel>

        {/* Above the list, not under it. A list of fifty rows puts its own
            foot off the screen, and a key nobody can find is not a feature. */}
        {events.length > 0 && <ShortcutHint onOpen={() => setShowKeys(true)} />}

        {/* Only while something is ticked. An empty bar is a control with
            nothing to act on. */}
        {ticked.size > 0 && (
          <SelectionBar
            selected={[...ticked]}
            onClear={() => setTicked(new Set())}
            onPurged={() => {
              setTicked(new Set())
              page.reload()
            }}
          />
        )}

        <Panel
          title={onlyUnreviewed ? "Waiting for a decision" : "Events"}
          bodyClassName="p-0"
          aside={
            <div className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="sm"
                className="-my-1 h-7 text-xs"
                aria-pressed={onlyUnreviewed}
                onClick={() =>
                  setQuery(
                    onlyUnreviewed ? { ...query, status: undefined } : unreviewedQuery(query),
                  )
                }
              >
                {onlyUnreviewed ? "Show every event" : "Needs a decision"}
              </Button>
              <ShortcutButton onOpen={() => setShowKeys(true)} />
            </div>
          }
        >
          {page.error ? (
            <ErrorNote error={page.error} onRetry={page.reload} className="px-4" />
          ) : page.data ? (
            events.length === 0 ? (
              <EmptyNote className="px-4" {...emptyWords(query, onlyUnreviewed)} />
            ) : (
              <EventsTable
                events={events}
                query={query}
                desk={desk}
                selectedId={selectedId}
                onSelect={setSelectedId}
                onSort={(sort: SortKey, order) => setQuery({ ...query, sort, order, offset: 0 })}
                noteRef={noteRef}
                ticked={ticked}
                onTick={tick}
                onTickPage={tickPage}
              />
            )
          ) : page.loading ? (
            <LoadingNote what="the events" className="px-4" />
          ) : null}
        </Panel>

        {muted.data ? (
          <p className="text-xs text-muted-foreground">
            {muted.data === 1 ? "One event here is" : `${muted.data} events here are`} muted and
            hidden. A mute window marks the events you can explain; the measurement and the clip are
            still here.{" "}
            <button
              type="button"
              onClick={() => setQuery(withMuted(query, "both"))}
              className="text-foreground underline-offset-4 hover:underline"
            >
              Show them
            </button>
          </p>
        ) : null}

        {mutedChoice(query) === "only" && (
          <p className="text-xs text-muted-foreground">
            Only muted events are listed. They are left out of the summary counts and out of the CSV
            export, whatever you decide about them.
          </p>
        )}

        {events.length > 0 && (
          <div className="flex flex-wrap items-center justify-end gap-3">
            <div className="flex items-center gap-3">
              <p className="text-xs text-muted-foreground">
                {first} to {last} of {total}
              </p>
              <Button
                variant="outline"
                size="sm"
                className="h-7 text-xs"
                disabled={(query.offset ?? 0) === 0}
                onClick={() =>
                  setQuery({
                    ...query,
                    offset: Math.max(0, (query.offset ?? 0) - (query.limit ?? 50)),
                  })
                }
              >
                Newer page
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="h-7 text-xs"
                disabled={last >= total}
                onClick={() =>
                  setQuery({ ...query, offset: (query.offset ?? 0) + (query.limit ?? 50) })
                }
              >
                Older page
              </Button>
            </div>
          </div>
        )}
      </Screen>

      <ShortcutHelp open={showKeys} onOpenChange={setShowKeys} />
    </>
  )
}

/** The line beside the title: what the range and the filters add up to. */
function countLine(total: number, query: EventQuery): string {
  const events = total === 1 ? "1 event" : `${total} events`
  if (query.from !== undefined && query.to !== undefined) {
    return `${events}, ${formatDayAndClock(query.from)} to ${formatDayAndClock(query.to)}`
  }
  if (query.from !== undefined) return `${events} since ${formatDayAndClock(query.from)}`
  if (query.to !== undefined) return `${events} up to ${formatDayAndClock(query.to)}`
  return events
}

/**
 * An empty list is an answer. Say which question it answers, so the screen
 * never looks like it failed to load.
 */
function emptyWords(query: EventQuery, onlyUnreviewed: boolean) {
  if (onlyUnreviewed) {
    return {
      headline: "Every event here has a decision.",
      detail: "Nothing is waiting for you. Show every event to read them again.",
    }
  }
  if (isFiltered(query)) {
    return {
      headline: "No event matches these filters.",
      detail: "That is an answer, not an error. Widen the range or clear a filter.",
    }
  }
  return {
    headline: "No events have been detected yet.",
    detail: "The collector writes one as soon as a noise crosses the trigger level.",
  }
}
