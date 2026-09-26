import { useEffect, useState } from "react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import type { EventClass, EventQuery, StatusFilter } from "@/lib/api"
import {
  CLASSES,
  CLASS_WORD,
  isFiltered,
  mutedChoice,
  STATUSES,
  STATUS_WORD,
  withMuted,
  withoutFilters,
  type MutedChoice,
} from "@/lib/query"
import { fromDateTimeInput, toDateTimeInput } from "@/lib/time"

/** How long to wait after typing before asking the server again. */
const SETTLE_MS = 400

/** The three things the list can do with the events a mute window covers. */
const MUTED_CHOICES: { value: MutedChoice; word: string }[] = [
  { value: "hide", word: "Hidden" },
  { value: "both", word: "Shown" },
  { value: "only", word: "Only muted" },
]

const toLocalInput = toDateTimeInput
const fromLocalInput = fromDateTimeInput

function toNumber(text: string): number | undefined {
  if (text.trim() === "") return undefined
  const value = Number(text)
  return Number.isFinite(value) ? value : undefined
}

/**
 * What the list is asking for. Every control here maps to one parameter of
 * `GET /api/events`, and every change goes into the URL, so a filtered view
 * survives a reload and can be left open in a tab.
 */
export function EventFilters({
  query,
  onChange,
}: {
  query: EventQuery
  onChange: (next: EventQuery) => void
}) {
  // Typed fields settle before they are sent, so a search is one request and
  // not one per letter.
  const [text, setText] = useState(() => ({
    from: toLocalInput(query.from),
    to: toLocalInput(query.to),
    min_lamax: query.min_lamax?.toString() ?? "",
    max_lamax: query.max_lamax?.toString() ?? "",
    min_duration_s: query.min_duration_ms ? String(query.min_duration_ms / 1000) : "",
    q: query.q ?? "",
  }))

  // The URL can change without this bar: Live links here with a range.
  useEffect(() => {
    setText({
      from: toLocalInput(query.from),
      to: toLocalInput(query.to),
      min_lamax: query.min_lamax?.toString() ?? "",
      max_lamax: query.max_lamax?.toString() ?? "",
      min_duration_s: query.min_duration_ms ? String(query.min_duration_ms / 1000) : "",
      q: query.q ?? "",
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query.from, query.to, query.min_lamax, query.max_lamax, query.min_duration_ms, query.q])

  useEffect(() => {
    const seconds = toNumber(text.min_duration_s)
    // A datetime field only holds minutes. Live hands over a range that
    // ends on the second, so an untouched field keeps the time it was given
    // rather than quietly rounding the end of the range down.
    const kept = (typed: string, was: number | undefined) =>
      toLocalInput(was) === typed ? was : fromLocalInput(typed)
    const next: EventQuery = {
      ...query,
      from: kept(text.from, query.from),
      to: kept(text.to, query.to),
      min_lamax: toNumber(text.min_lamax),
      max_lamax: toNumber(text.max_lamax),
      min_duration_ms: seconds === undefined ? undefined : Math.round(seconds * 1000),
      q: text.q.trim() || undefined,
      offset: 0,
    }
    if (sameFilters(next, query)) return
    const timer = window.setTimeout(() => onChange(next), SETTLE_MS)
    return () => window.clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text])

  const set = (patch: Partial<EventQuery>) => onChange({ ...query, ...patch, offset: 0 })
  const field = (key: keyof typeof text) => (value: string) =>
    setText((all) => ({ ...all, [key]: value }))

  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Field label="From" htmlFor="filter-from">
          <Input
            id="filter-from"
            type="datetime-local"
            value={text.from}
            onChange={(e) => field("from")(e.target.value)}
          />
        </Field>
        <Field label="To" htmlFor="filter-to">
          <Input
            id="filter-to"
            type="datetime-local"
            value={text.to}
            onChange={(e) => field("to")(e.target.value)}
          />
        </Field>
        <Field label="Peak level, dB" htmlFor="filter-min-lamax">
          <div className="flex items-center gap-2">
            <Input
              id="filter-min-lamax"
              type="number"
              inputMode="decimal"
              placeholder="from"
              value={text.min_lamax}
              onChange={(e) => field("min_lamax")(e.target.value)}
            />
            <span className="text-xs text-muted-foreground">to</span>
            <Input
              type="number"
              inputMode="decimal"
              placeholder="to"
              aria-label="Highest peak level in dB"
              value={text.max_lamax}
              onChange={(e) => field("max_lamax")(e.target.value)}
            />
          </div>
        </Field>
        <Field label="Shortest event, seconds" htmlFor="filter-duration">
          <Input
            id="filter-duration"
            type="number"
            inputMode="decimal"
            min={0}
            placeholder="any"
            value={text.min_duration_s}
            onChange={(e) => field("min_duration_s")(e.target.value)}
          />
        </Field>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Field label="Class">
          <ToggleGroup
            type="multiple"
            variant="outline"
            size="sm"
            className="flex-wrap justify-start"
            value={query.class ?? []}
            onValueChange={(value) =>
              set({ class: value.length ? (value as EventClass[]) : undefined })
            }
          >
            {CLASSES.map((name) => (
              <ToggleGroupItem key={name} value={name} className="px-3 text-xs">
                {CLASS_WORD[name]}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </Field>
        <Field label="Review">
          <ToggleGroup
            type="multiple"
            variant="outline"
            size="sm"
            className="flex-wrap justify-start"
            value={query.status ?? []}
            onValueChange={(value) =>
              set({ status: value.length ? (value as StatusFilter[]) : undefined })
            }
          >
            {STATUSES.map((name) => (
              <ToggleGroupItem key={name} value={name} className="px-3 text-xs">
                {STATUS_WORD[name]}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </Field>
        <Field label="Muted events">
          <ToggleGroup
            type="single"
            variant="outline"
            size="sm"
            className="flex-wrap justify-start"
            value={mutedChoice(query)}
            onValueChange={(value) => value && onChange(withMuted(query, value as MutedChoice))}
          >
            {MUTED_CHOICES.map((choice) => (
              <ToggleGroupItem key={choice.value} value={choice.value} className="px-3 text-xs">
                {choice.word}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
          <p className="text-xs text-muted-foreground">
            A mute window marks the events you can explain. The measurement and the clip stay; the
            counts and the export leave them out.
          </p>
        </Field>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Note contains" htmlFor="filter-q">
          <Input
            id="filter-q"
            type="search"
            placeholder="kids, party, drilling"
            value={text.q}
            onChange={(e) => field("q")(e.target.value)}
          />
        </Field>
        <div className="flex items-end justify-between gap-4">
          <label className="flex items-center gap-2 pb-2 text-sm">
            <Checkbox
              checked={query.quiet_only === true}
              onCheckedChange={(checked) =>
                set({ quiet_only: checked === true ? true : undefined })
              }
            />
            Quiet hours only
          </label>
          {isFiltered(query) && (
            <Button
              variant="outline"
              size="sm"
              className="mb-1 h-8 text-xs"
              onClick={() => onChange(withoutFilters(query))}
            >
              Clear filters
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}

function Field({
  label,
  htmlFor,
  children,
}: {
  label: string
  htmlFor?: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={htmlFor} className="text-xs text-muted-foreground">
        {label}
      </Label>
      {children}
    </div>
  )
}

/** True when two queries ask the server for the same rows. */
function sameFilters(a: EventQuery, b: EventQuery): boolean {
  return (
    a.from === b.from &&
    a.to === b.to &&
    a.min_lamax === b.min_lamax &&
    a.max_lamax === b.max_lamax &&
    a.min_duration_ms === b.min_duration_ms &&
    (a.q ?? "") === (b.q ?? "")
  )
}
