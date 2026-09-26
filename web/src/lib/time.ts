// Times, ranges, and the rule for what "tonight" means.

/**
 * A night runs from 18:00 to noon the next day. Before 18:00 you are still
 * looking at the window that opened yesterday evening.
 */
export const NIGHT_STARTS_HOUR = 18
export const NIGHT_ENDS_HOUR = 12

/** The sentence the interface shows so the rule is never a guess. */
export const NIGHT_RULE = "A night runs from 18:00 to noon the next day."

/** Past this hour the night is over in the way a person speaks about it. */
const MORNING_HOUR = 6

export interface NightWindow {
  from: number
  to: number
  /** True while the window is still filling, so the range ends at "now". */
  running: boolean
  /** "Tonight" while the night is under way, "Last night" once morning comes. */
  title: string
}

/** Work out which night is on screen, from the browser's local clock. */
export function nightWindow(now = new Date()): NightWindow {
  const start = new Date(now)
  start.setHours(NIGHT_STARTS_HOUR, 0, 0, 0)

  if (now.getHours() >= NIGHT_STARTS_HOUR) {
    // The evening has begun. The window opened a few hours ago.
    return { from: start.getTime(), to: now.getTime(), running: true, title: "Tonight" }
  }

  start.setDate(start.getDate() - 1)

  if (now.getHours() < NIGHT_ENDS_HOUR) {
    // Still inside the window, so it is still filling. At 2am this is still
    // tonight; by breakfast the same window is last night.
    const title = now.getHours() < MORNING_HOUR ? "Tonight" : "Last night"
    return { from: start.getTime(), to: now.getTime(), running: true, title }
  }

  const end = new Date(now)
  end.setHours(NIGHT_ENDS_HOUR, 0, 0, 0)
  return { from: start.getTime(), to: end.getTime(), running: false, title: "Last night" }
}

const clock = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
})
const clockSeconds = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
})
const dayAndClock = new Intl.DateTimeFormat(undefined, {
  weekday: "short",
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
})
const dayAndSeconds = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
})

/** 23:41 */
export function formatClock(ms: number): string {
  return clock.format(ms)
}

/** 23:41:07 */
export function formatClockSeconds(ms: number): string {
  return clockSeconds.format(ms)
}

/** Thu 11 Sep, 18:00 */
export function formatDayAndClock(ms: number): string {
  return dayAndClock.format(ms)
}

/** The range under the heading, as a person would read it out. */
export function formatRange(window: NightWindow): string {
  const end = window.running ? "now" : formatClock(window.to)
  return `${formatDayAndClock(window.from)} to ${end}`
}

/** 4.8 s, 1 min 12 s. Durations are short here, so seconds are the unit. */
export function formatDuration(ms: number): string {
  const seconds = ms / 1000
  if (seconds < 60) return `${seconds.toFixed(1)} s`
  const minutes = Math.floor(seconds / 60)
  const rest = Math.round(seconds - minutes * 60)
  return `${minutes} min ${rest} s`
}

const fullDay = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "long",
  year: "numeric",
})

/** 12 September 2026. For a date a sentence reads out loud. */
export function formatDay(ms: number): string {
  return fullDay.format(ms)
}

/** 11 Sep, 23:41:07. For a log line, where the second is the point. */
export function formatDayAndSeconds(ms: number): string {
  return dayAndSeconds.format(ms)
}

/** How long something has been running: 3 days 4 h, 5 h 12 min, 42 s. */
export function formatSpan(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000))
  if (seconds < 60) return `${seconds} s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} h ${minutes - hours * 60} min`
  const days = Math.floor(hours / 24)
  return `${days} ${days === 1 ? "day" : "days"} ${hours - days * 24} h`
}

/**
 * The value a `datetime-local` field holds, in the browser's time zone. The
 * field has no time zone of its own, so the conversion has to be written out.
 */
export function toDateTimeInput(ms: number | undefined): string {
  if (ms === undefined) return ""
  const when = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${when.getFullYear()}-${pad(when.getMonth() + 1)}-${pad(when.getDate())}T${pad(
    when.getHours(),
  )}:${pad(when.getMinutes())}`
}

/** What a `datetime-local` field says, as epoch milliseconds. */
export function fromDateTimeInput(text: string): number | undefined {
  if (!text) return undefined
  const ms = new Date(text).getTime()
  return Number.isFinite(ms) ? ms : undefined
}

/** How long ago, for a reading that has stopped updating. */
export function formatAgo(ms: number, now = Date.now()): string {
  const seconds = Math.max(0, Math.round((now - ms) / 1000))
  if (seconds < 60) return `${seconds} s ago`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  return `${hours} h ago`
}

const fullStamp = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "long",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
})

/**
 * The browser's offset from UTC at this time, written out: `UTC-04:00`.
 * A printed page leaves the building, so a clock reading on its own is not
 * enough: the reader cannot know which zone it was taken in.
 */
export function utcOffset(ms: number): string {
  // getTimezoneOffset counts the other way round: it is minutes to add to
  // local time to reach UTC.
  const minutes = -new Date(ms).getTimezoneOffset()
  const pad = (n: number) => String(n).padStart(2, "0")
  const whole = Math.abs(minutes)
  return `UTC${minutes < 0 ? "-" : "+"}${pad(Math.floor(whole / 60))}:${pad(whole % 60)}`
}

/** The time zone the browser is set to, or "" when it will not say. */
export function timeZoneName(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? ""
  } catch {
    return ""
  }
}

/**
 * 11 September 2026 at 21:18:49 UTC-04:00 (America/New_York). The long form,
 * for a page that has to say what it means with nothing else around it.
 */
export function formatStamp(ms: number): string {
  const zone = timeZoneName()
  return `${fullStamp.format(ms)} ${utcOffset(ms)}${zone ? ` (${zone})` : ""}`
}
