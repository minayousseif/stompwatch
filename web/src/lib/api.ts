// The typed client for the stompwatch API. docs/http-api.md is the contract;
// this file follows it and nothing else talks to the network.
//
// Every time in and out is epoch milliseconds. Every level is dB SPL.

import type { LevelAccuracy } from "@/lib/accuracy"

/** The server answered with an error. Its message is written for a person. */
export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
  }
}

/** The browser could not reach the server at all. */
export class OfflineError extends Error {
  constructor() {
    super("The dashboard cannot reach the collector. Check that it is running.")
    this.name = "OfflineError"
  }
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type EventClass =
  "running" | "jumping" | "stomping" | "impact" | "steady" | "airborne" | "unknown"

/** A review decision. The API also uses "none" to filter for unreviewed. */
export type ReviewStatus = "verified" | "rejected" | "unsure"

export type StatusFilter = ReviewStatus | "none"

export interface Summary {
  from: number
  to: number
  events: number
  reviewed: number
  verified: number
  /** Null when the range holds no events. */
  loudest: { id: number; lamax: number; started_ms: number } | null
  /** Null when the range holds no measured seconds. Zero would be a claim. */
  laeq: number | null
  baseline: number | null
  quiet_hour_events: number
  /** Events a mute window covers. They are left out of every count above. */
  muted: number
}

export interface TimelinePoint {
  t: number
  laeq: number | null
  lamax: number | null
  /** Only 1-second points carry a baseline. */
  baseline?: number | null
}

export interface TimelineEvent {
  id: number
  started_ms: number
  ended_ms: number
  lamax: number
  class: EventClass
  /** "" when nobody has reviewed the event. */
  status: ReviewStatus | ""
}

export interface QuietSpan {
  from: number
  to: number
}

export interface Timeline {
  resolution: "1s" | "1m"
  /** How wide one point is. A missing bucket is a gap, not a straight line. */
  bucket_ms: number
  from: number
  to: number
  points: TimelinePoint[]
  events: TimelineEvent[]
  quiet: QuietSpan[]
}

export interface Review {
  status: ReviewStatus
  note: string
  reviewer: string
  reviewed_ms: number
}

export interface NoiseEvent {
  id: number
  started_ms: number
  ended_ms: number
  duration_ms: number
  laeq: number
  lamax: number
  baseline_at_trigger: number
  low_energy: number
  high_energy: number
  low_high_ratio: number
  class: EventClass
  confidence: number
  forced: boolean
  /**
   * `jump_db` is the peak over the median level of the 30 s before the
   * event. `rise_db` is the largest step between two consecutive
   * one-second means, from one second before the event to its close. Both
   * are null when nothing measured them: events before schema 7, or less
   * than 10 s of levels before.
   */
  jump_db: number | null
  rise_db: number | null
  has_audio: boolean
  has_video: boolean
  /**
   * True when a mute window covers the event. The measurement still happened;
   * the owner has said they can explain it.
   */
  muted: boolean
  /** Null when nobody has reviewed the event. */
  review: Review | null
}

export interface EventMedia {
  kind: "audio" | "video"
  bytes: number
  duration_ms: number
  sha256: string
  /**
   * The time of the clip's first sample. Null for a clip stored before the
   * collector recorded it. Null means "not recorded", never the epoch.
   */
  start_ms: number | null
  /**
   * How much of the pre-roll the clip does not have, in milliseconds. Null
   * whenever `start_ms` is null, because it is only ever measured.
   */
  missing_head_ms: number | null
  /**
   * How many samples hit the 16-bit limit. Anything above zero means the
   * clip is distorted at those moments, and no playback setting can undo it.
   */
  clipped: number
  /**
   * The rate of the stored file, in Hz. The clip filter cutoff is half it.
   * Null when the file is gone. A clip keeps the rate it was recorded at,
   * so clips of more than one rate can sit side by side: never assume
   * 1 kHz.
   */
  rate_hz: number | null
  /**
   * The loudest sample of the stored clip, and how much playback raises it.
   * Both are null when the file is gone. A household impact sits tens of dB
   * below full scale, so a faithful clip needs the rise to be audible; the
   * stored file is never changed.
   */
  peak_dbfs: number | null
  playback_gain_db: number | null
  truncated: boolean
  /**
   * True when the file has vanished: the row is here, no purge was
   * recorded, and the file is not on disk. That is a fault. A purged clip
   * has this false, because the owner deleted it on purpose.
   */
  missing: boolean
  /**
   * When the recording was deleted on purpose, and the login of whoever
   * asked. Both null for a clip that was never purged. `bytes`,
   * `duration_ms` and `sha256` above keep the values the collector
   * recorded: the row is the record that the clip existed.
   */
  purged_ms: number | null
  purged_by: string | null
  /**
   * True when a video clip really carries the camera's own audio track,
   * unfiltered. The collector read the written file back to find out, so it is
   * the clip's own answer and not the setting in force now, nor the setting in
   * force when the clip was cut: clips with and without sound sit side by
   * side. Always false for an audio clip, which is the measuring microphone's
   * and is filtered.
   */
  camera_audio: boolean
}

/** What became of one file in a purge. */
export type PurgeOutcome = "purged" | "already_gone" | "already_purged" | "no_media" | "refused"

export interface PurgeResult {
  kind: "audio" | "video"
  outcome: PurgeOutcome
  bytes: number
  /** Only on a refusal. It never names a file. */
  reason?: string
}

export interface PurgeEventResult {
  event_id: number
  bytes: number
  results: PurgeResult[]
}

export interface PurgeReply {
  purged_files: number
  purged_bytes: number
  events: PurgeEventResult[]
  /** Ids with no event row. Nothing was deleted or recorded for them. */
  unknown_events: number[]
}

export interface MediaUsage {
  files: number
  /** How many distinct events the files belong to. */
  events: number
  bytes: number
}

export interface MediaSpace {
  /** Files still held, by kind. */
  audio: MediaUsage
  video: MediaUsage
  /** What has already been reclaimed. */
  purged: MediaUsage
  /** What a purge would reclaim now, inside the age asked for. */
  purgeable: MediaUsage
  started_before: number
}

/**
 * The capture and calibration settings in force **when an event was
 * recorded**. It is read from the capture-settings history, never from
 * `GET /api/system`: reprinting an event from three months ago must not
 * claim today's calibration.
 *
 * `calibration` is the **base name** of the calibration file, never its
 * path, and empty when none was configured.
 */
export interface EventCapture extends LevelAccuracy {
  /** When these settings came into force. */
  from_ms: number
  sensitivity_dbfs: number
  calibration: string
  calibration_offset_db: number
  device: string
  channel: number
}

export interface EventDetail extends NoiseEvent {
  media: EventMedia[]
  /** Linear envelope samples, not dB. The interface converts them. */
  envelope: { rate_hz: number; start_ms: number; values: number[] } | null
  /** One second per point, from 30 s before the event to 30 s after. */
  samples: TimelinePoint[]
  /**
   * The settings this event was measured under, or **null** when the history
   * does not reach back to it, which is every event recorded before schema
   * version 6. Null means the settings in force at capture were not
   * recorded. Never fall back to `GET /api/system`, which answers for today.
   */
  capture: EventCapture | null
}

export interface EventPage {
  /** The count before limit and offset. */
  total: number
  events: NoiseEvent[]
}

export interface Waveform {
  buckets: number
  duration_ms: number
  /** [min, max] per bucket, each between -1 and 1. */
  peaks: [number, number][]
}

export interface LiveReading {
  t: number
  /** Null until the first second has been measured. */
  laeq: number | null
  lamax: number | null
  baseline: number | null
  /** True when no new reading arrived for 5 seconds. */
  stale: boolean
}

export interface HealthRecord {
  id: number
  ts_ms: number
  kind: string
  detail: string
  duration_ms: number
}

export interface HealthPage {
  total: number
  records: HealthRecord[]
}

export interface SystemStatus {
  started_ms: number
  now_ms: number
  last_audio_ms: number
  last_commit_ms: number
  collecting: boolean
  /**
   * The daily recording pause. `span` is `HH:MM-HH:MM`, or several such
   * ranges joined by `, `, or empty when there is none. `active` says
   * whether now is inside one of them, and `until` is the end of that one,
   * `HH:MM`, or empty when `active` is false. Inside a window no event opens
   * and no audio clip or video is recorded; the level is still measured.
   */
  recording_pause: { span: string; active: boolean; until: string }
  db_bytes: number
  wal_bytes: number
  schema_version: number
  disk: { name: string; free_mb: number }[]
  clips: { count: number; bytes: number }
  counts: {
    events_today: number
    events_total: number
    unreviewed: number
    samples_today: number
  }
  capture: {
    device: string
    channel: number
    gain: string
    sensitivity_dbfs: number
    /** The base name of the calibration file and its correction, or "none". */
    calibration: string
  }
  /**
   * How well the levels are known **now**, which is the right thing for a
   * System screen. An event does not read its uncertainty from here: it
   * cites its own `capture` block.
   */
  levels: LevelAccuracy
  pipeline: {
    chunks: number
    dropped_samples: number
    bins_dropped: number
    events_dropped: number
    write_failures: number
    loop_restarts: number
  }
  heartbeat: { configured: boolean; interval_ms: number }
  auth: { mode: string; login: string; name: string }
  /**
   * The camera. `uptime_ms` is time with video, not time since start, so a
   * gap in coverage shows as uptime short of the process uptime. Every
   * disconnect is also a `camera_disconnect` row in the health log.
   */
  camera: {
    enabled: boolean
    connected: boolean
    /** Zero when not connected. */
    connected_since_ms: number
    uptime_ms: number
    disconnects: number
    /** Zero before the first segment. */
    last_segment_ms: number
    ring: { segments: number; bytes: number }
    clips: { count: number; bytes: number }
    config: CameraConfig
  }
  tailscale: Tailscale
}

/**
 * What Tailscale says about this box. Every field is best effort and none
 * of it is a failure: a box with no Tailscale answers `installed: false`
 * with `err` saying so, and the collector is unaffected either way.
 *
 * The dashboard reports this and never configures it. Setting up
 * `tailscale serve` is the owner's job (SPEC.md section 9.0).
 */
export interface Tailscale {
  /** The `tailscale` command is on the box. */
  installed: boolean
  /** `tailscaled` answered and its backend state is `Running`. */
  running: boolean
  /** The backend state as Tailscale words it: `Running`, `NeedsLogin`. */
  backend: string
  /** The node's MagicDNS name, without the trailing dot. */
  name: string
  /** A `tailscale serve` target points at `http_addr`. */
  serving: boolean
  /** What to open when `serving` is true, otherwise empty. */
  serve_url: string
  /**
   * The dashboard is published to the **public internet**. This is a fault,
   * not a reading: `auth_mode` trusts an identity header that is only safe
   * while `tailscale serve` is the sole path to the loopback listener.
   */
  funnel: boolean
  /** When this node's key expires, or 0 when expiry is disabled. */
  key_expiry_ms: number
  /** Why the answer is incomplete, in plain words, or empty. */
  err: string
  /**
   * The dashboard's listen address: what a serve target has to point at,
   * and what the command that fixes it names.
   */
  http_addr: string
}

/**
 * How the camera is set up. It is here whether or not a camera is
 * configured, because the owner reads it while bringing one up.
 *
 * `credentials` carries no password and never will. It says whether a login
 * was read and where it came from; `user` is the login name, which is not
 * the secret. The password lives in the environment or in a 0600 file on
 * the box, and nothing in this interface can read it or set it.
 */
export interface CameraConfig {
  host: string
  port: number
  sub_path: string
  main_path: string
  /**
   * "configured" when `camera_rtsp_path` names a path, "discovered" when the
   * collector found one by trying the defaults, and "none" when there is no
   * path at all yet. A discovered path is tried again on every reconnect.
   */
  sub_path_source: "configured" | "discovered" | "none"
  ring_minutes: number
  segment_seconds: number
  /**
   * `camera_audio`: true means the ring and the event clips carry the
   * camera's own audio track, unfiltered, so they hold speech in clear.
   */
  audio: boolean
  /** What the camera actually sends. Many cameras send no audio at all. */
  audio_track: AudioTrack
  /** The version line, or empty when ffmpeg is not installed. */
  ffmpeg: string
  credentials: {
    loaded: boolean
    source: "environment" | "file" | "none"
    user: string
  }
}

/**
 * Whether the camera's stream carries an audio track. `known` is false until
 * something has asked the camera, and then `present` is false because nothing
 * was measured, not because the camera is silent.
 */
export interface AudioTrack {
  known: boolean
  present: boolean
  codec: string
  rate_hz: number
}

/** What the camera test found. Every `detail` has had the password removed. */
export interface ProbeResult {
  tried: { path: string; ok: boolean; detail: string }[]
  /** The paths that answered, or empty when none did. */
  sub_path: string
  main_path: string
  /** Zero when `clock_readable` is false. */
  clock_drift_ms: number
  clock_readable: boolean
  /** The audio track of the sub-stream that answered. */
  audio: AudioTrack
}

export type LogLevel = "DEBUG" | "INFO" | "WARN" | "ERROR" | "RAW"

export interface LogLine {
  ts_ms: number
  level: LogLevel
  msg: string
  attrs: Record<string, unknown> | null
}

export interface LogPage {
  /** True when the scan hit its byte budget before reaching `from`. */
  truncated: boolean
  /**
   * True when at least one older matching line exists past this page. The log
   * has no total: counting every matching line means reading the whole log.
   */
  more: boolean
  lines: LogLine[]
}

export interface Setting {
  key: string
  value: string
  default: string
  /** "file" when the config file supplies the value, "db" when it is overridden. */
  source: "file" | "db"
  type: string
  min?: number
  max?: number
  label: string
  unit?: string
  /** True when the running process applies the change without a restart. */
  live: boolean
  /**
   * One sentence that must be shown with the field, or empty. It carries
   * what a range cannot: `camera_audio` says that the camera will record
   * speech in clear.
   */
  note: string
}

export interface MuteWindow {
  id: number
  start_ms: number
  end_ms: number
  reason: string
}

// ---------------------------------------------------------------------------
// Query parameters
// ---------------------------------------------------------------------------

export interface Range {
  from: number
  to: number
}

export interface EventQuery {
  from?: number
  to?: number
  class?: EventClass[]
  status?: StatusFilter[]
  min_lamax?: number
  max_lamax?: number
  min_duration_ms?: number
  quiet_only?: boolean
  /** 0 keeps only unmuted events, 1 only muted ones. Absent keeps both. */
  muted?: 0 | 1
  q?: string
  sort?: "started_ms" | "lamax" | "duration_ms" | "class"
  order?: "asc" | "desc"
  limit?: number
  offset?: number
}

export interface HealthQuery {
  from?: number
  to?: number
  kind?: string[]
  limit?: number
  offset?: number
}

export interface LogQuery {
  from?: number
  to?: number
  level?: LogLevel[]
  q?: string
  limit?: number
  offset?: number
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

/** Build a query string. An undefined value is left out; an array repeats. */
function query(params: Record<string, unknown>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null) continue
    if (Array.isArray(value)) {
      for (const one of value) search.append(key, String(one))
      continue
    }
    if (typeof value === "boolean") {
      if (value) search.append(key, "1")
      continue
    }
    search.append(key, String(value))
  }
  const text = search.toString()
  return text ? `?${text}` : ""
}

async function request<T>(path: string, init?: RequestInit & { signal?: AbortSignal }): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      ...init,
      headers: { Accept: "application/json", ...(init?.headers ?? {}) },
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") throw err
    throw new OfflineError()
  }

  if (response.status === 204) return undefined as T

  const text = await response.text()
  let body: unknown = null
  if (text) {
    try {
      body = JSON.parse(text)
    } catch {
      body = null
    }
  }

  if (!response.ok) {
    // The server writes its errors for a person. Show its sentence.
    const message =
      body && typeof body === "object" && typeof (body as { error?: unknown }).error === "string"
        ? (body as { error: string }).error
        : `The collector answered ${response.status} and said nothing more.`
    throw new ApiError(response.status, message)
  }

  return body as T
}

function send<T>(path: string, method: string, payload: unknown, signal?: AbortSignal): Promise<T> {
  return request<T>(path, {
    method,
    signal,
    headers: { "Content-Type": "application/json" },
    body: payload === undefined ? undefined : JSON.stringify(payload),
  })
}

// ---------------------------------------------------------------------------
// Endpoints
// ---------------------------------------------------------------------------

export const api = {
  /** GET /api/summary */
  summary(range: Range, signal?: AbortSignal): Promise<Summary> {
    return request(`/api/summary${query({ ...range })}`, { signal })
  },

  /**
   * GET /api/timeline. Ask for `1m` when the range may cross six hours:
   * `auto` switches resolution as the range grows and the chart changes shape.
   */
  timeline(
    range: Range,
    res: "auto" | "1s" | "1m" = "auto",
    signal?: AbortSignal,
  ): Promise<Timeline> {
    return request(`/api/timeline${query({ ...range, res })}`, { signal })
  },

  /** GET /api/events */
  events(q: EventQuery = {}, signal?: AbortSignal): Promise<EventPage> {
    return request(`/api/events${query({ ...q })}`, { signal })
  },

  /** GET /api/events/{id} */
  event(id: number, signal?: AbortSignal): Promise<EventDetail> {
    return request(`/api/events/${id}`, { signal })
  },

  /** PUT /api/events/{id}/review. The reviewer comes from the identity. */
  saveReview(
    id: number,
    body: { status: ReviewStatus; note: string },
    signal?: AbortSignal,
  ): Promise<Review> {
    return send(`/api/events/${id}/review`, "PUT", body, signal)
  },

  /** DELETE /api/events/{id}/review. The event becomes unreviewed again. */
  clearReview(id: number, signal?: AbortSignal): Promise<void> {
    return send(`/api/events/${id}/review`, "DELETE", undefined, signal)
  },

  /** GET /api/events/{id}/waveform */
  waveform(id: number, buckets?: number, signal?: AbortSignal): Promise<Waveform> {
    return request(`/api/events/${id}/waveform${query({ buckets })}`, { signal })
  },

  /** GET /api/health */
  health(q: HealthQuery = {}, signal?: AbortSignal): Promise<HealthPage> {
    return request(`/api/health${query({ ...q })}`, { signal })
  },

  /** GET /api/system */
  system(signal?: AbortSignal): Promise<SystemStatus> {
    return request(`/api/system`, { signal })
  },

  /**
   * POST /api/camera/probe. Tries each RTSP path on the camera and reports
   * what works. It changes no setting: the owner applies what it found
   * through the settings form. One runs at a time; a second gets 409.
   */
  probeCamera(signal?: AbortSignal): Promise<ProbeResult> {
    return send(`/api/camera/probe`, "POST", undefined, signal)
  },

  /** GET /api/logs */
  logs(q: LogQuery = {}, signal?: AbortSignal): Promise<LogPage> {
    return request(`/api/logs${query({ ...q })}`, { signal })
  },

  /** GET /api/settings */
  settings(signal?: AbortSignal): Promise<{ settings: Setting[] }> {
    return request(`/api/settings`, { signal })
  },

  /**
   * PUT /api/settings. One bad value rejects the whole request and changes
   * nothing. Send null for a key to drop it back to the file default.
   */
  saveSettings(
    values: Record<string, string | null>,
    signal?: AbortSignal,
  ): Promise<{ settings: Setting[] }> {
    return send(`/api/settings`, "PUT", values, signal)
  },

  /** GET /api/mute-windows */
  muteWindows(signal?: AbortSignal): Promise<{ windows: MuteWindow[] }> {
    return request(`/api/mute-windows`, { signal })
  },

  /** POST /api/mute-windows */
  addMuteWindow(
    body: { start_ms: number; end_ms: number; reason: string },
    signal?: AbortSignal,
  ): Promise<MuteWindow> {
    return send(`/api/mute-windows`, "POST", body, signal)
  },

  /** DELETE /api/mute-windows/{id} */
  removeMuteWindow(id: number, signal?: AbortSignal): Promise<void> {
    return send(`/api/mute-windows/${id}`, "DELETE", undefined, signal)
  },

  /**
   * POST /api/media/purge. Deletes clip files and nothing else: the event,
   * its level trace, its decision and its note all stay, and so does the
   * media row with its size and its hash.
   *
   * `confirm` must be true. It is the confirmation step, carried in the
   * request so nothing but a deliberate act can delete a recording. At most
   * 500 events in one request.
   */
  purgeMedia(
    body: { event_ids: number[]; kinds?: ("audio" | "video")[]; confirm: true },
    signal?: AbortSignal,
  ): Promise<PurgeReply> {
    return send(`/api/media/purge`, "POST", body, signal)
  },

  /**
   * GET /api/media/usage. What the recordings take and what a purge would
   * reclaim. `startedBefore` narrows what is purgeable to events that
   * started before that instant.
   */
  mediaUsage(startedBefore?: number, signal?: AbortSignal): Promise<MediaSpace> {
    return request(`/api/media/usage${query({ started_before: startedBefore })}`, { signal })
  },

  /** GET /api/media/usage for exactly these events, at most 500 of them. */
  mediaUsageOf(eventIds: number[], signal?: AbortSignal): Promise<MediaSpace> {
    return request(`/api/media/usage${query({ event_id: eventIds })}`, { signal })
  },

  /**
   * GET /api/media/purgeable. The events that still hold a clip and started
   * before an instant, oldest first. Purging by age is two steps: read the
   * list, then purge exactly those events, so nothing is deleted that the
   * owner was not shown.
   */
  purgeable(
    startedBefore: number,
    limit?: number,
    signal?: AbortSignal,
  ): Promise<{ event_ids: number[]; more: boolean }> {
    return request(`/api/media/purgeable${query({ started_before: startedBefore, limit })}`, {
      signal,
    })
  },
}

/**
 * Addresses the browser loads directly: media elements, downloads, and the
 * live stream. They are not fetched as JSON, so they are URLs, not calls.
 */
export const url = {
  /** The clip, resampled so a browser will play it. Range requests work. */
  /**
   * The clip, resampled and levelled for listening. tone "tilt" lifts the
   * upper part so a phone speaker can reproduce it; "flat" keeps the
   * recorded balance. Neither changes the stored file.
   */
  audio: (id: number, tone: "tilt" | "flat" = "tilt") =>
    `/api/events/${id}/audio${tone === "flat" ? "?tone=flat" : ""}`,
  /** The stored file, byte for byte, as a download. */
  audioOriginal: (id: number) => `/api/events/${id}/audio/original`,
  /** The stored video clip, byte for byte. Range requests work. */
  video: (id: number) => `/api/events/${id}/video`,
  live: () => `/api/live`,
  eventsCsv: (range: Range, status: ReviewStatus[] = ["verified"]) =>
    `/api/export/events.csv${query({ ...range, status })}`,
  timelinePng: (
    range: Range,
    options: { status?: ReviewStatus[]; width?: number; height?: number } = {},
  ) => `/api/export/timeline.png${query({ ...range, ...options })}`,
}

/** The sentence to show the owner for any failure this client can raise. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError || err instanceof OfflineError) return err.message
  if (err instanceof Error && err.message) return err.message
  return "Something went wrong. Reload the page."
}
