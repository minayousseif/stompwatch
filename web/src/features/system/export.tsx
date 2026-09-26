import { DownloadIcon } from "lucide-react"

import { ErrorNote, LoadingNote } from "@/components/state"
import { Button } from "@/components/ui/button"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { Field, RangeFields } from "@/features/system/range-fields"
import { useResource } from "@/hooks/use-resource"
import { api, url, type ReviewStatus } from "@/lib/api"
import { STATUS_WORD } from "@/lib/query"
import { EXPORT_STATUSES, type ExportQuery } from "@/lib/system-query"
import { formatDayAndClock, nightWindow } from "@/lib/time"

const DAY_MS = 86_400_000

/** The longest range the server will draw, from docs/http-api.md. */
const LONGEST_DAYS = 31

/**
 * A date range in, a CSV and a rendered chart out. The two files are what
 * gets attached to an email or a complaint, so the screen says what they
 * will contain before either is downloaded.
 */
export function ExportSection({
  query,
  onChange,
}: {
  query: ExportQuery
  onChange: (next: ExportQuery) => void
}) {
  const ready = query.from !== undefined && query.to !== undefined && query.to > query.from
  const range = ready ? { from: query.from!, to: query.to! } : null
  const days = range ? (range.to - range.from) / DAY_MS : 0
  const tooWide = days > LONGEST_DAYS

  // The same filter the CSV runs, so the number on screen is the number of
  // rows in the file. A muted event is never written, whatever its status.
  const count = useResource(
    (signal) =>
      range && !tooWide
        ? api
            .events({ ...range, status: query.status, muted: 0, limit: 1 }, signal)
            .then((page) => page.total)
        : Promise.resolve(null),
    [range?.from, range?.to, query.status.join(","), tooWide],
  )

  const setRange = (from: number, to: number) => onChange({ ...query, from, to })
  const night = nightWindow()

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end gap-3">
        <RangeFields
          idPrefix="export"
          from={query.from}
          to={query.to}
          onChange={(from, to) => onChange({ ...query, from, to })}
        />
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            className="h-9 text-xs"
            onClick={() => setRange(night.from, night.to)}
          >
            {night.title}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="h-9 text-xs"
            onClick={() => setRange(Date.now() - 7 * DAY_MS, Date.now())}
          >
            Last 7 days
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="h-9 text-xs"
            onClick={() => setRange(Date.now() - 30 * DAY_MS, Date.now())}
          >
            Last 30 days
          </Button>
        </div>
      </div>

      <Field label="Which decisions to include">
        <ToggleGroup
          type="multiple"
          variant="outline"
          size="sm"
          className="flex-wrap justify-start"
          value={query.status}
          onValueChange={(value) =>
            onChange({
              ...query,
              status: value.length ? (value as ReviewStatus[]) : ["verified"],
            })
          }
        >
          {EXPORT_STATUSES.map((status) => (
            <ToggleGroupItem key={status} value={status} className="px-3 text-xs">
              {STATUS_WORD[status]}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </Field>

      <p className="max-w-prose text-sm text-muted-foreground">
        The CSV holds only the events you have reviewed and marked with one of the decisions above,
        and never an event a mute window covers. The claim it supports is not that the detector is
        perfect, but that a person looked at every event in the file. The chart draws the measured
        level for the same range, with quiet hours shaded and a tick for each event in the file.
      </p>

      {!ready ? (
        <p className="text-sm">
          Choose a start and an end. Both are needed, because the file names the range it covers.
        </p>
      ) : tooWide ? (
        <p className="text-sm" role="alert">
          That range is {Math.round(days)} days. The chart is drawn for at most {LONGEST_DAYS} days
          at a time, so narrow the range.
        </p>
      ) : (
        <>
          <div className="border-y border-border py-3 text-sm">
            {count.error ? (
              <ErrorNote error={count.error} onRetry={count.reload} />
            ) : count.data !== null && count.data !== undefined ? (
              <p>
                {count.data === 0
                  ? "No event matches. The file would hold a header row and nothing else."
                  : `${count.data} ${count.data === 1 ? "event goes" : "events go"} into the file.`}{" "}
                <span className="text-muted-foreground">
                  {formatDayAndClock(range!.from)} to {formatDayAndClock(range!.to)}.
                </span>
              </p>
            ) : count.loading ? (
              <LoadingNote what="the count" className="py-0" />
            ) : null}
          </div>

          <div className="flex flex-wrap gap-2">
            <Button asChild size="sm" className="h-9 gap-1.5">
              <a href={url.eventsCsv(range!, query.status)} download>
                <DownloadIcon className="size-4" aria-hidden />
                Download the CSV
              </a>
            </Button>
            <Button asChild variant="outline" size="sm" className="h-9 gap-1.5">
              <a href={url.timelinePng(range!, { status: query.status })} download>
                <DownloadIcon className="size-4" aria-hidden />
                Download the chart
              </a>
            </Button>
          </div>

          <figure className="flex flex-col gap-2">
            <img
              // The server draws this on a light background on purpose: it is
              // printed or pasted into an email, not read at 2am.
              src={url.timelinePng(range!, { status: query.status, width: 1200, height: 400 })}
              alt={`The measured level from ${formatDayAndClock(range!.from)} to ${formatDayAndClock(
                range!.to,
              )}, with a tick for each event in the export`}
              width={1200}
              height={400}
              className="w-full max-w-3xl rounded-md border border-border bg-white"
            />
            <figcaption className="text-xs text-muted-foreground">
              This is the file you would download, at the size the server draws it.
            </figcaption>
          </figure>
        </>
      )}
    </div>
  )
}
