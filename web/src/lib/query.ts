// The Events screen keeps its filters in the URL query string, using the same
// parameter names the API uses. A filtered view can then be left open in a
// tab, reloaded, or sent to yourself, and it comes back the same.
//
// docs/http-api.md lists the parameters. Nothing here invents a new one.

import type { EventClass, EventQuery, StatusFilter } from "@/lib/api"

export const CLASSES: EventClass[] = [
  "impact",
  "running",
  "jumping",
  "stomping",
  "unknown",
  "steady",
  "airborne",
]

/** The four review states a filter can ask for. "none" means unreviewed. */
export const STATUSES: StatusFilter[] = ["verified", "rejected", "unsure", "none"]

export type SortKey = "started_ms" | "lamax" | "duration_ms" | "class"

export const SORT_KEYS: SortKey[] = ["started_ms", "lamax", "duration_ms", "class"]

/** How the owner says each class and each review state out loud. */
export const CLASS_WORD: Record<EventClass, string> = {
  impact: "Impact",
  running: "Running",
  jumping: "Jumping",
  stomping: "Stomping",
  unknown: "Unknown",
  steady: "Steady sound",
  airborne: "Airborne",
}

export const STATUS_WORD: Record<StatusFilter, string> = {
  verified: "Confirmed",
  rejected: "Rejected",
  unsure: "Unsure",
  none: "Not reviewed",
}

export const DEFAULT_LIMIT = 50

/**
 * What the screen asks for when the URL says nothing. Muted events are out:
 * they are the ones the owner has already explained, and the list is for the
 * ones that still need a decision.
 */
export const DEFAULT_QUERY: EventQuery = {
  sort: "started_ms",
  order: "desc",
  muted: 0,
  limit: DEFAULT_LIMIT,
  offset: 0,
}

/**
 * How the URL says which muted events to show. The API takes `muted=0` for
 * the unmuted ones and `muted=1` for the muted ones, and leaves the parameter
 * out to take both. The URL needs a third word for "both", because an absent
 * parameter here means the default, which is to hide them.
 */
export type MutedChoice = "hide" | "only" | "both"

export function mutedChoice(q: EventQuery): MutedChoice {
  if (q.muted === 0) return "hide"
  if (q.muted === 1) return "only"
  return "both"
}

export function withMuted(q: EventQuery, choice: MutedChoice): EventQuery {
  const muted = choice === "hide" ? 0 : choice === "only" ? 1 : undefined
  return { ...q, muted, offset: 0 }
}

function integer(text: string | null): number | undefined {
  if (text === null || text.trim() === "") return undefined
  const value = Number(text)
  return Number.isFinite(value) ? Math.trunc(value) : undefined
}

function decimal(text: string | null): number | undefined {
  if (text === null || text.trim() === "") return undefined
  const value = Number(text)
  return Number.isFinite(value) ? value : undefined
}

/** "all" is the URL's word for both kinds. Anything else falls to the default. */
function readMuted(text: string | null): 0 | 1 | undefined {
  if (text === "all") return undefined
  if (text === "1") return 1
  return 0
}

function members<T extends string>(values: string[], allowed: readonly T[]): T[] | undefined {
  const kept = values.filter((one): one is T => (allowed as readonly string[]).includes(one))
  return kept.length > 0 ? kept : undefined
}

/**
 * Read the query the URL asks for. An unreadable value is left out rather
 * than guessed at, so a hand-edited URL never silently filters something.
 */
export function readQuery(search: URLSearchParams): EventQuery {
  const q: EventQuery = {
    from: integer(search.get("from")),
    to: integer(search.get("to")),
    class: members(search.getAll("class"), CLASSES),
    status: members(search.getAll("status"), STATUSES),
    min_lamax: decimal(search.get("min_lamax")),
    max_lamax: decimal(search.get("max_lamax")),
    min_duration_ms: integer(search.get("min_duration_ms")),
    quiet_only: search.get("quiet_only") === "1" ? true : undefined,
    muted: readMuted(search.get("muted")),
    q: search.get("q") || undefined,
    sort: members([search.get("sort") ?? ""], SORT_KEYS)?.[0] ?? DEFAULT_QUERY.sort,
    order: search.get("order") === "asc" ? "asc" : "desc",
    limit: integer(search.get("limit")) ?? DEFAULT_LIMIT,
    offset: integer(search.get("offset")) ?? 0,
  }
  // Keep the request inside what the API accepts, so a stale URL still loads.
  q.limit = Math.min(500, Math.max(1, q.limit ?? DEFAULT_LIMIT))
  q.offset = Math.max(0, q.offset ?? 0)
  return q
}

/** Write the query back, leaving out anything that is already the default. */
export function writeQuery(q: EventQuery): URLSearchParams {
  const search = new URLSearchParams()
  const put = (key: string, value: unknown) => {
    if (value === undefined || value === null || value === "") return
    search.set(key, String(value))
  }
  put("from", q.from)
  put("to", q.to)
  for (const one of q.class ?? []) search.append("class", one)
  for (const one of q.status ?? []) search.append("status", one)
  put("min_lamax", q.min_lamax)
  put("max_lamax", q.max_lamax)
  put("min_duration_ms", q.min_duration_ms)
  if (q.quiet_only) search.set("quiet_only", "1")
  if (q.muted === 1) search.set("muted", "1")
  if (q.muted === undefined) search.set("muted", "all")
  put("q", q.q)
  if (q.sort && q.sort !== DEFAULT_QUERY.sort) search.set("sort", q.sort)
  if (q.order === "asc") search.set("order", "asc")
  if (q.limit !== undefined && q.limit !== DEFAULT_LIMIT) search.set("limit", String(q.limit))
  if (q.offset) search.set("offset", String(q.offset))
  return search
}

/** True when the query asks for less than the whole history. */
export function isFiltered(q: EventQuery): boolean {
  return (
    q.from !== undefined ||
    q.to !== undefined ||
    (q.class?.length ?? 0) > 0 ||
    (q.status?.length ?? 0) > 0 ||
    q.min_lamax !== undefined ||
    q.max_lamax !== undefined ||
    q.min_duration_ms !== undefined ||
    q.quiet_only === true ||
    q.muted !== 0 ||
    (q.q ?? "") !== ""
  )
}

/**
 * Everything but the paging, cleared. Sorting is a view, not a filter, and
 * hiding muted events is where the list starts.
 */
export function withoutFilters(q: EventQuery): EventQuery {
  return { sort: q.sort, order: q.order, muted: 0, limit: q.limit, offset: 0 }
}

/** The query that shows only what still needs a decision. */
export function unreviewedQuery(q: EventQuery): EventQuery {
  return { ...q, status: ["none"], offset: 0 }
}

/** A stable key for the parts of a query that change what the server returns. */
export function queryKey(q: EventQuery): string {
  return writeQuery(q).toString()
}

/** The link to one event, carrying the query so the next event is reachable. */
export function eventHref(id: number, q: EventQuery): string {
  const search = writeQuery(q).toString()
  return search ? `/events/${id}?${search}` : `/events/${id}`
}
