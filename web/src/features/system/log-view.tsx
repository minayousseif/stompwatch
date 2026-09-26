import { useEffect, useState } from "react"

import { Pager } from "@/components/pager"
import { EmptyNote, ErrorNote, LoadingNote } from "@/components/state"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { Field, RangeFields } from "@/features/system/range-fields"
import { useResource } from "@/hooks/use-resource"
import { api, type LogLevel, type LogLine, type LogQuery } from "@/lib/api"
import { LOG_LEVELS, LOG_LIMIT, systemQueryKey } from "@/lib/system-query"
import { formatDayAndSeconds } from "@/lib/time"
import { cn } from "@/lib/utils"

/** How long to wait after typing before asking the server again. */
const SETTLE_MS = 400

/** What each level means, for the owner who has not read the Go source. */
const LEVEL_HELP: Record<LogLevel, string> = {
  DEBUG: "Detail the collector writes for a developer",
  INFO: "Normal running",
  WARN: "Something worth knowing",
  ERROR: "Something failed",
  RAW: "A line that is not valid JSON, shown as it was written",
}

/**
 * The application log, newest first. It answers "what exactly happened",
 * which is the question the counters and the health log cannot answer.
 */
export function LogView({
  query,
  onChange,
}: {
  query: LogQuery
  onChange: (next: LogQuery) => void
}) {
  const page = useResource((signal) => api.logs(query, signal), [systemQueryKey(query)])
  const limit = query.limit ?? LOG_LIMIT
  const offset = query.offset ?? 0

  // Any change to what is shown starts at the newest page again.
  const set = (patch: Partial<LogQuery>) => onChange({ ...query, ...patch, offset: 0 })

  // The search box settles before it is sent, so a word is one request and
  // not one per letter.
  const [text, setText] = useState(query.q ?? "")
  useEffect(() => setText(query.q ?? ""), [query.q])
  useEffect(() => {
    const wanted = text.trim() || undefined
    if (wanted === query.q) return
    const timer = window.setTimeout(() => onChange({ ...query, q: wanted, offset: 0 }), SETTLE_MS)
    return () => window.clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text])

  const filtered =
    (query.level?.length ?? 0) > 0 ||
    query.from !== undefined ||
    query.to !== undefined ||
    (query.q ?? "") !== ""

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end gap-3">
        <RangeFields
          idPrefix="log"
          from={query.from}
          to={query.to}
          onChange={(from, to) => set({ from, to })}
        />
        <Field label="Line contains" htmlFor="log-q">
          <Input
            id="log-q"
            type="search"
            className="h-9 w-full sm:w-56"
            placeholder="arecord, sha256, gain"
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        </Field>
        {filtered && (
          <Button
            variant="ghost"
            size="sm"
            className="h-9 text-xs"
            onClick={() => set({ level: undefined, from: undefined, to: undefined, q: undefined })}
          >
            Clear
          </Button>
        )}
      </div>

      <Field label="Level">
        <ToggleGroup
          type="multiple"
          variant="outline"
          size="sm"
          className="flex-wrap justify-start"
          value={query.level ?? []}
          onValueChange={(value) =>
            set({ level: value.length ? (value as LogLevel[]) : undefined })
          }
        >
          {LOG_LEVELS.map((level) => (
            <ToggleGroupItem
              key={level}
              value={level}
              className="px-3 text-xs"
              title={LEVEL_HELP[level]}
            >
              {level}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </Field>

      {page.error ? (
        <ErrorNote error={page.error} onRetry={page.reload} />
      ) : page.data ? (
        page.data.lines.length === 0 ? (
          offset > 0 ? (
            <EmptyNote
              headline="This page is past the end of the log."
              detail="The lines it held have been read already, or the filter changed under it."
              action={{ label: "Back to the newest page", onClick: () => set({}) }}
            />
          ) : (
            <EmptyNote
              headline="No line matches."
              detail="The log holds the newest files only. Widen the range, or clear the level filter."
            />
          )
        ) : (
          <>
            {page.data.truncated && (
              <p className="text-xs text-muted-foreground" role="status">
                The scan stopped before it reached the start of the range, because the log is bigger
                than one read. Everything below is real; there may be older lines it did not reach.
                Ask for a narrower range to see them.
              </p>
            )}
            {/* The log is the longest thing on the screen. It scrolls inside
                its own box, so the sections below stay reachable. */}
            <ul className="max-h-[32rem] divide-y divide-border overflow-y-auto border-y border-border">
              {page.data.lines.map((line, index) => (
                <LogRow key={`${line.ts_ms}-${index}`} line={line} />
              ))}
            </ul>
            {/* There is no total. Counting every matching line means reading
                the whole log, which is the cost the backwards scan exists to
                avoid, so the footer says where this page starts and whether
                another page follows. */}
            <Pager
              count={countLine(offset, page.data.lines.length)}
              canNewer={offset > 0}
              canOlder={page.data.more}
              onNewer={() => onChange({ ...query, offset: Math.max(0, offset - limit) })}
              onOlder={() => onChange({ ...query, offset: offset + limit })}
            />
          </>
        )
      ) : page.loading ? (
        <LoadingNote what="the log" />
      ) : null}
    </div>
  )
}

/** Which lines are on screen. The log has no total to count them against. */
function countLine(offset: number, shown: number): string {
  if (offset === 0) {
    return `The newest ${shown} matching ${shown === 1 ? "line" : "lines"}.`
  }
  return `${offset + 1} to ${offset + shown}`
}

/**
 * One line. The attributes are shown as named values rather than as the JSON
 * they arrived in: a wall of braces is not a readable log.
 */
function LogRow({ line }: { line: LogLine }) {
  const attrs = Object.entries(line.attrs ?? {})

  return (
    <li className="flex flex-col gap-1 py-2 sm:flex-row sm:gap-4">
      <div className="flex shrink-0 items-baseline gap-3 sm:w-52">
        <span className="text-xs text-muted-foreground">{formatDayAndSeconds(line.ts_ms)}</span>
        <span
          className={cn(
            "text-xs",
            line.level === "ERROR" || line.level === "WARN"
              ? "font-medium text-foreground"
              : "text-muted-foreground",
          )}
          title={LEVEL_HELP[line.level]}
        >
          {line.level}
        </span>
      </div>
      <div className="min-w-0 flex-1">
        <p className="text-sm break-words">{line.msg}</p>
        {attrs.length > 0 && (
          <dl className="mt-1 flex flex-wrap gap-x-4 gap-y-0.5">
            {attrs.map(([key, value]) => (
              <div key={key} className="flex min-w-0 gap-1.5 text-xs">
                <dt className="text-muted-foreground">{key}</dt>
                <dd className="max-w-96 truncate" title={show(value)}>
                  {show(value)}
                </dd>
              </div>
            ))}
          </dl>
        )}
      </div>
    </li>
  )
}

/** One attribute value as text. A nested object is rare and is shown short. */
function show(value: unknown): string {
  if (value === null) return "null"
  if (typeof value === "object") return JSON.stringify(value)
  return String(value)
}
