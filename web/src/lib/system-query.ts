// The System screen keeps its three filters in the URL, the way the Events
// screen does. A health row can then link to the log view filtered to its own
// timestamp, and the view survives a reload.
//
// The parameters are prefixed, because one screen carries three filters and
// `from` on its own would not say from what.

import type { HealthQuery, LogLevel, LogQuery, ReviewStatus } from "@/lib/api"

export const LOG_LEVELS: LogLevel[] = ["DEBUG", "INFO", "WARN", "ERROR", "RAW"]

/** Every kind the collector writes, and how the owner would say it. */
export const HEALTH_KINDS: { kind: string; word: string }[] = [
  { kind: "capture_gap", word: "Capture gap" },
  { kind: "stuck_stream", word: "Stuck stream" },
  { kind: "frame_drop", word: "Dropped frames" },
  { kind: "clock_step", word: "Clock step" },
  { kind: "clock_drift", word: "Clock drift" },
  { kind: "gain_change", word: "Gain change" },
  { kind: "write_error", word: "Write error" },
  { kind: "disk_low", word: "Disk low" },
  { kind: "loop_restart", word: "Loop restart" },
  { kind: "clip_truncated", word: "Clip cut short" },
  { kind: "clip_no_audio", word: "Clip has no sound" },
  { kind: "media_missing", word: "Clip file gone" },
  { kind: "media_purge", word: "Recordings deleted" },
  { kind: "camera_disconnect", word: "Camera gone" },
  { kind: "settings_change", word: "Settings change" },
  { kind: "data_reset", word: "Data reset" },
  { kind: "recording_pause", word: "Recording pause" },
  { kind: "settings_changed", word: "Settings saved" },
  { kind: "mute_window_added", word: "Mute window added" },
  { kind: "mute_window_removed", word: "Mute window removed" },
]

const KIND_WORDS = new Map(HEALTH_KINDS.map((one) => [one.kind, one.word]))

/** A kind in words. An unknown kind is shown as the collector wrote it. */
export function healthKindWord(kind: string): string {
  return KIND_WORDS.get(kind) ?? kind
}

/** How many health rows and log lines one page holds. */
export const HEALTH_LIMIT = 50
export const LOG_LIMIT = 200

export interface ExportQuery {
  from?: number
  to?: number
  status: ReviewStatus[]
}

export interface SystemQuery {
  health: HealthQuery
  log: LogQuery
  export: ExportQuery
}

function integer(text: string | null): number | undefined {
  if (text === null || text.trim() === "") return undefined
  const value = Number(text)
  return Number.isFinite(value) ? Math.trunc(value) : undefined
}

function members<T extends string>(values: string[], allowed: readonly T[]): T[] | undefined {
  const kept = values.filter((one): one is T => (allowed as readonly string[]).includes(one))
  return kept.length > 0 ? kept : undefined
}

/** The three reviewed states the export offers. "Not reviewed" is not one. */
export const EXPORT_STATUSES: ReviewStatus[] = ["verified", "rejected", "unsure"]

/** Read what the URL asks for. An unreadable value is left out, not guessed. */
export function readSystemQuery(search: URLSearchParams): SystemQuery {
  return {
    health: {
      from: integer(search.get("health_from")),
      to: integer(search.get("health_to")),
      kind: members(
        search.getAll("health_kind"),
        HEALTH_KINDS.map((one) => one.kind),
      ),
      limit: HEALTH_LIMIT,
      offset: integer(search.get("health_offset")) ?? 0,
    },
    log: {
      from: integer(search.get("log_from")),
      to: integer(search.get("log_to")),
      level: members(search.getAll("log_level"), LOG_LEVELS),
      q: search.get("log_q") || undefined,
      limit: LOG_LIMIT,
      offset: integer(search.get("log_offset")) ?? 0,
    },
    export: {
      from: integer(search.get("export_from")),
      to: integer(search.get("export_to")),
      status: members(search.getAll("export_status"), EXPORT_STATUSES) ?? ["verified"],
    },
  }
}

/** Write the query back, leaving out anything that is already the default. */
export function writeSystemQuery(q: SystemQuery): URLSearchParams {
  const search = new URLSearchParams()
  const put = (key: string, value: number | string | undefined) => {
    if (value === undefined || value === "") return
    search.set(key, String(value))
  }
  put("health_from", q.health.from)
  put("health_to", q.health.to)
  if ((q.health.offset ?? 0) > 0) put("health_offset", q.health.offset)
  for (const one of q.health.kind ?? []) search.append("health_kind", one)
  put("log_from", q.log.from)
  put("log_to", q.log.to)
  if ((q.log.offset ?? 0) > 0) put("log_offset", q.log.offset)
  for (const one of q.log.level ?? []) search.append("log_level", one)
  put("log_q", q.log.q)
  put("export_from", q.export.from)
  put("export_to", q.export.to)
  if (!(q.export.status.length === 1 && q.export.status[0] === "verified")) {
    for (const one of q.export.status) search.append("export_status", one)
  }
  return search
}

/** A stable key for the parts of a query that change what the server returns. */
export function systemQueryKey(q: HealthQuery | LogQuery): string {
  return JSON.stringify(q)
}

/** How wide a window the log view opens around a health row. */
export const AROUND_MS = 60_000

/** The query that shows the log around one health row's timestamp. */
export function aroundHealthRow(q: SystemQuery, tsMs: number): SystemQuery {
  return {
    ...q,
    log: { ...q.log, from: tsMs - AROUND_MS, to: tsMs + AROUND_MS, level: undefined, offset: 0 },
  }
}
