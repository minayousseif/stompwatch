import { useCallback, useMemo, useRef, useState } from "react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom"
import { ChevronLeftIcon, ChevronRightIcon, ListIcon, PrinterIcon } from "lucide-react"

import { Screen } from "@/components/app-shell"
import { SiteHeader } from "@/components/site-header"
import { ErrorNote, LoadingNote, Panel } from "@/components/state"
import { Button } from "@/components/ui/button"
import {
  ClipFacts,
  ClipMissing,
  ClipPurged,
  ClipTransport,
  useClipPlayer,
} from "@/features/detail/clip"
import { VideoClip } from "@/features/detail/video"
import { ChartKey, clipWindow, EventChart } from "@/features/detail/event-chart"
import { EventFacts } from "@/features/detail/facts"
import { PrintFooter, PrintHeader, usePrintTime } from "@/features/detail/print-sheet"
import { ReviewControls, ReviewNote } from "@/features/review/review-controls"
import { ReviewMark } from "@/features/live/event-list"
import { ShortcutButton, ShortcutHelp, useReviewKeys } from "@/features/review/shortcuts"
import { useReviewDesk } from "@/features/review/use-review"
import { useIsMobile } from "@/hooks/use-mobile"
import { useResource } from "@/hooks/use-resource"
import { api, url, type EventDetail, type EventMedia } from "@/lib/api"
import { eventHref, queryKey, readQuery, writeQuery } from "@/lib/query"
import { formatDayAndClock, formatStamp } from "@/lib/time"

/** How many peaks to ask for. More than the pixels available is wasted. */
const WAVEFORM_BUCKETS = 1200

function audioOf(event: EventDetail): EventMedia | null {
  return event.media.find((one) => one.kind === "audio") ?? null
}

function videoOf(event: EventDetail): EventMedia | null {
  return event.media.find((one) => one.kind === "video") ?? null
}

/** Read one setting as a number of seconds. */
function seconds(settings: { key: string; value: string }[] | undefined, key: string) {
  const found = settings?.find((one) => one.key === key)
  if (!found) return null
  const value = Number(found.value)
  return Number.isFinite(value) ? value : null
}

export function EventDetailScreen() {
  const { id: text } = useParams()
  const id = Number(text)
  const [search] = useSearchParams()
  const navigate = useNavigate()
  const isMobile = useIsMobile()
  const desk = useReviewDesk()
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const [showKeys, setShowKeys] = useState(false)

  // The list passed its query along, so moving on from here follows the same
  // set of events the list was showing.
  const query = useMemo(() => readQuery(search), [search])
  const key = queryKey(query)

  const detail = useResource((signal) => api.event(id, signal), [id])
  const event = detail.data

  const media = event ? audioOf(event) : null
  const playable = !!media && !media.missing && media.purged_ms === null
  const video = event ? videoOf(event) : null

  const waveform = useResource(
    (signal) => (playable ? api.waveform(id, WAVEFORM_BUCKETS, signal) : Promise.resolve(null)),
    [id, playable],
  )

  // A clip stored before the collector recorded its start has to be placed
  // from its end, which needs post_roll_s. Only the settings know it, and a
  // failure here costs the alignment, not the screen.
  const settings = useResource((signal) => api.settings(signal).catch(() => null), [])

  const printedMs = usePrintTime()

  const postRollMs = useMemo(() => {
    const value = seconds(settings.data?.settings, "post_roll_s")
    return value === null ? null : value * 1000
  }, [settings.data])

  const clip = useMemo(
    () => (event && waveform.data ? clipWindow(event, waveform.data, media, postRollMs) : null),
    [event, waveform.data, media, postRollMs],
  )

  // How much of the run-up the buffer could not supply. The server measures
  // it from the recorded start; when that was never recorded there is nothing
  // to measure, and a number worked out from a guessed start would be one
  // nobody took.
  const shortfallMs = media?.missing_head_ms ?? null

  const player = useClipPlayer(clip?.from ?? null)
  // "tilt" lifts the upper part of the clip so a phone speaker reproduces
  // it. "flat" is the balance the microphone recorded. Neither changes the
  // stored file, and the download is always the bytes on disk.
  const [tone, setTone] = useState<"tilt" | "flat">("tilt")

  // The events either side, for j and k. They come from the same query the
  // list used, so a queue of unreviewed events walks in order.
  const siblings = useResource((signal) => api.events(query, signal), [key])
  const list = siblings.data?.events ?? []
  const index = list.findIndex((one) => one.id === id)
  const previous = index > 0 ? list[index - 1] : null
  const next = index >= 0 && index < list.length - 1 ? list[index + 1] : null

  const step = useCallback(
    (forward: boolean) => {
      const target = forward ? next : previous
      if (target) {
        navigate(eventHref(target.id, query))
        return
      }
      // At the edge of the page, follow the list onto the next one.
      const limit = query.limit ?? 50
      const offset = (query.offset ?? 0) + (forward ? limit : -limit)
      if (offset < 0 || index < 0) return
      const page = { ...query, offset }
      api
        .events({ ...page, limit: 1, offset: forward ? offset : offset + limit - 1 })
        .then((answer) => {
          const one = answer.events[0]
          if (one) navigate(eventHref(one.id, page))
        })
        .catch(() => undefined)
    },
    [index, navigate, next, previous, query],
  )

  const backToList = `/events${writeQuery(query).toString() ? `?${writeQuery(query)}` : ""}`

  useReviewKeys(
    useMemo(
      () => ({
        next: () => step(true),
        previous: () => step(false),
        confirm: () => event && desk.mark(event, "verified"),
        reject: () => event && desk.mark(event, "rejected"),
        unsure: () => event && desk.mark(event, "unsure"),
        clear: () => event && desk.clear(event),
        note: () => noteRef.current?.focus(),
        open: () => navigate(backToList),
        play: playable ? player.toggle : undefined,
        help: () => setShowKeys(true),
      }),
      [backToList, desk, event, navigate, playable, player.toggle, step],
    ),
    !showKeys,
  )

  if (!Number.isInteger(id) || id <= 0) {
    return (
      <>
        <SiteHeader title="Event" />
        <Screen>
          <Panel>
            <p className="text-sm">That is not an event number.</p>
            <Button asChild variant="outline" size="sm" className="mt-4">
              <Link to="/events">Go to the events</Link>
            </Button>
          </Panel>
        </Screen>
      </>
    )
  }

  // A purged clip is its own state. It is not missing, which is a fault,
  // and it is not absent, which means no clip was ever recorded.
  const clipState: "purged" | "missing" | "none" | "failed" | "ok" = !media
    ? "none"
    : media.purged_ms !== null
      ? "purged"
      : media.missing
        ? "missing"
        : player.failed
          ? "failed"
          : "ok"

  return (
    <>
      <SiteHeader
        title={`Event ${id}`}
        aside={event ? formatDayAndClock(event.started_ms) : undefined}
      />
      <Screen>
        <div className="flex flex-wrap items-center justify-between gap-3 print:hidden">
          <Button asChild variant="ghost" size="sm" className="h-8 gap-1.5 text-xs">
            <Link to={backToList}>
              <ListIcon className="size-3.5" aria-hidden />
              Back to the events
            </Link>
          </Button>
          <div className="flex flex-wrap items-center justify-end gap-2">
            {index >= 0 && siblings.data && (
              <p className="text-xs text-muted-foreground">
                {(query.offset ?? 0) + index + 1} of {siblings.data.total}
              </p>
            )}
            <Button
              variant="outline"
              size="sm"
              className="h-8 gap-1 text-xs"
              onClick={() => step(false)}
              aria-keyshortcuts="k"
            >
              <ChevronLeftIcon className="size-3.5" aria-hidden />
              Newer
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="h-8 gap-1 text-xs"
              onClick={() => step(true)}
              aria-keyshortcuts="j"
            >
              Older
              <ChevronRightIcon className="size-3.5" aria-hidden />
            </Button>
            <ShortcutButton onOpen={() => setShowKeys(true)} />
            {/* One button for both, because on a phone they are the same
                action and the owner should not have to work that out. */}
            <Button
              variant="outline"
              size="sm"
              className="h-8 gap-1.5 text-xs"
              onClick={() => window.print()}
            >
              <PrinterIcon className="size-3.5" aria-hidden />
              Print or save as PDF
            </Button>
          </div>
        </div>

        {detail.error ? (
          <Panel>
            <ErrorNote error={detail.error} onRetry={detail.reload} />
          </Panel>
        ) : event ? (
          <>
            {/* The sheet reads the instrument off the event's own capture
                block, not off the collector's current state: a reprint
                months later must not claim today's calibration. */}
            <PrintHeader event={event} printedMs={printedMs} />

            <Panel title="The clip and the level around it">
              {clipState === "ok" ? (
                <div className="print:hidden">
                  {/* The element is hidden: the waveform is the control. */}
                  <audio {...player.audioProps} src={url.audio(id, tone)} />
                  <ClipTransport
                    player={player}
                    durationMs={media?.duration_ms ?? null}
                    gainDb={media?.playback_gain_db ?? null}
                    tone={tone}
                    onTone={setTone}
                  />
                </div>
              ) : clipState === "purged" && media ? (
                <ClipPurged media={media} />
              ) : (
                <ClipMissing reason={clipState} />
              )}

              <div className="mt-4 print:mt-0">
                {waveform.error && playable ? (
                  <ErrorNote error={waveform.error} onRetry={waveform.reload} />
                ) : null}
                <EventChart
                  event={event}
                  waveform={waveform.data}
                  clip={clip}
                  playheadMs={
                    clipState === "ok" && (player.playing || player.position > 0)
                      ? player.playheadMs
                      : null
                  }
                  onSeek={clipState === "ok" ? player.seekTo : undefined}
                  compact={isMobile}
                />
                <ChartKey
                  event={event}
                  clip={clip}
                  shortfallMs={shortfallMs}
                  hasClipFile={clipState === "ok"}
                />
              </div>
            </Panel>

            {video && (
              <Panel title="The camera">
                <VideoClip eventId={id} media={video} />
              </Panel>
            )}

            <Panel title="Your decision">
              {/* On paper the footer says this once, at the foot, where the
                  rest of what the record is and is not is said. */}
              {event.muted && (
                <p className="mb-3 max-w-prose text-xs text-muted-foreground print:hidden">
                  A mute window covers this event, so it is left out of the summary counts and never
                  written to the CSV export. The measurement and the clip are still here, and your
                  decision is still recorded.
                </p>
              )}
              <ReviewMark status={desk.reviewOf(event)?.status ?? null} />
              <ReviewControls event={event} desk={desk} className="mt-2 print:hidden" />
              <ReviewNote
                event={event}
                desk={desk}
                rows={3}
                className="mt-4 max-w-prose print:hidden"
              />
              {/* On paper the note is the text, not a box to type in. */}
              <div className="mt-3 hidden print:block">
                <p className="text-xs text-muted-foreground">Note</p>
                <p className="mt-0.5 max-w-prose text-sm whitespace-pre-wrap">
                  {desk.reviewOf(event)?.note?.trim() || "No note was written."}
                </p>
              </div>
              {desk.reviewOf(event) && (
                <>
                  <p className="mt-3 text-xs text-muted-foreground print:hidden">
                    {desk.reviewOf(event)!.reviewer} marked this{" "}
                    {formatDayAndClock(desk.reviewOf(event)!.reviewed_ms)}.
                  </p>
                  {/* The printed sheet carries the year and the offset: it is
                      read where the screen's short clock means nothing. */}
                  <p className="mt-3 hidden text-xs text-muted-foreground print:block">
                    {desk.reviewOf(event)!.reviewer} marked this{" "}
                    {formatStamp(desk.reviewOf(event)!.reviewed_ms)}.
                  </p>
                </>
              )}
            </Panel>

            {/* A sheet is narrower than a laptop but still wide enough for
                two columns, and two columns is what keeps this on one page. */}
            <div className="grid gap-4 lg:grid-cols-2 print:grid-cols-2 print:gap-3">
              <Panel title="What the detector measured">
                <EventFacts
                  event={event}
                  maxEventS={seconds(settings.data?.settings, "max_event_s")}
                />
              </Panel>
              <Panel title="The stored file">
                {media && media.purged_ms !== null ? (
                  // The row is still the record that the clip existed, so
                  // its size and its hash are still worth showing.
                  <div className="flex flex-col gap-4">
                    <ClipPurged media={media} />
                    <ClipFacts eventId={id} media={media} />
                  </div>
                ) : media && !media.missing ? (
                  <ClipFacts eventId={id} media={media} />
                ) : (
                  <ClipMissing reason={media ? "missing" : "none"} />
                )}
              </Panel>
            </div>

            <PrintFooter eventId={id} media={media} />
          </>
        ) : detail.loading ? (
          <Panel>
            <LoadingNote what="the event" />
          </Panel>
        ) : null}
      </Screen>

      <ShortcutHelp open={showKeys} onOpenChange={setShowKeys} />
    </>
  )
}
