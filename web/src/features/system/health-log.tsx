import { ChevronDownIcon } from "lucide-react"

import { Pager } from "@/components/pager"
import { EmptyNote, ErrorNote, LoadingNote } from "@/components/state"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useResource } from "@/hooks/use-resource"
import { api, type HealthQuery } from "@/lib/api"
import { healthKindWord, HEALTH_KINDS, HEALTH_LIMIT, systemQueryKey } from "@/lib/system-query"
import { formatDayAndSeconds, formatDuration } from "@/lib/time"
import { RangeFields } from "@/features/system/range-fields"

/**
 * What the collector noticed, newest first. This is the other half of the
 * counters above: they say whether it is working, this says what happened.
 */
export function HealthLog({
  query,
  onChange,
  onShowLogAt,
}: {
  query: HealthQuery
  onChange: (next: HealthQuery) => void
  /** Open the application log around this moment. */
  onShowLogAt: (tsMs: number) => void
}) {
  const page = useResource((signal) => api.health(query, signal), [systemQueryKey(query)])
  const chosen = query.kind ?? []
  const limit = query.limit ?? HEALTH_LIMIT
  const offset = query.offset ?? 0

  // Any change to what is shown starts at the newest page again. Page 4 of a
  // filter nobody is looking at any more is an empty screen that reads as a
  // fault.
  const set = (patch: Partial<HealthQuery>) => onChange({ ...query, ...patch, offset: 0 })

  const toggle = (kind: string) => {
    const next = chosen.includes(kind) ? chosen.filter((one) => one !== kind) : [...chosen, kind]
    set({ kind: next.length > 0 ? next : undefined })
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end gap-3">
        <RangeFields
          idPrefix="health"
          from={query.from}
          to={query.to}
          onChange={(from, to) => set({ from, to })}
        />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" className="h-9 gap-1.5 text-xs">
              {chosen.length === 0 ? "Every kind" : `${chosen.length} kinds`}
              <ChevronDownIcon className="size-3.5" aria-hidden />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="max-h-80 overflow-y-auto">
            <DropdownMenuLabel>Show these kinds</DropdownMenuLabel>
            <DropdownMenuSeparator />
            {HEALTH_KINDS.map((one) => (
              <DropdownMenuItem
                key={one.kind}
                onSelect={(e) => {
                  e.preventDefault()
                  toggle(one.kind)
                }}
                className="gap-2"
              >
                <Checkbox checked={chosen.includes(one.kind)} tabIndex={-1} aria-hidden />
                {one.word}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
        {(chosen.length > 0 || query.from !== undefined || query.to !== undefined) && (
          <Button
            variant="ghost"
            size="sm"
            className="h-9 text-xs"
            onClick={() => set({ kind: undefined, from: undefined, to: undefined })}
          >
            Clear
          </Button>
        )}
      </div>

      {page.error ? (
        <ErrorNote error={page.error} onRetry={page.reload} />
      ) : page.data ? (
        page.data.records.length === 0 ? (
          offset > 0 ? (
            <EmptyNote
              headline="This page is past the end."
              detail="The records it held have been read already, or the filter changed under it."
              action={{ label: "Back to the newest page", onClick: () => set({}) }}
            />
          ) : (
            <EmptyNote
              headline="The collector has noticed nothing here."
              detail="That is the good outcome. Widen the range to read older records."
            />
          )
        ) : (
          <>
            <ul className="max-h-[28rem] divide-y divide-border overflow-y-auto border-y border-border">
              {page.data.records.map((record) => (
                <li key={record.id} className="flex flex-col gap-1 py-2.5 sm:flex-row sm:gap-4">
                  <div className="flex shrink-0 items-baseline gap-3 sm:w-64">
                    <span className="text-sm">{formatDayAndSeconds(record.ts_ms)}</span>
                    <span className="text-sm text-muted-foreground">
                      {healthKindWord(record.kind)}
                    </span>
                  </div>
                  <p className="min-w-0 flex-1 text-sm break-words">
                    {record.detail || "No detail was recorded."}
                    {record.duration_ms > 0 && (
                      <span className="text-muted-foreground">
                        {" "}
                        It lasted {formatDuration(record.duration_ms)}.
                      </span>
                    )}
                  </p>
                  <button
                    type="button"
                    onClick={() => onShowLogAt(record.ts_ms)}
                    className="shrink-0 text-left text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
                  >
                    Read the log here
                  </button>
                </li>
              ))}
            </ul>
            <Pager
              count={countLine(offset, page.data.records.length, page.data.total)}
              canNewer={offset > 0}
              canOlder={offset + page.data.records.length < page.data.total}
              onNewer={() => onChange({ ...query, offset: Math.max(0, offset - limit) })}
              onOlder={() => onChange({ ...query, offset: offset + limit })}
            />
          </>
        )
      ) : page.loading ? (
        <LoadingNote what="the health log" />
      ) : null}
    </div>
  )
}

/** Which records are on screen, out of how many the filter matches. */
function countLine(offset: number, shown: number, total: number): string {
  if (total <= shown && offset === 0) {
    return `${total} ${total === 1 ? "record" : "records"} match.`
  }
  return `${offset + 1} to ${offset + shown} of ${total}`
}
