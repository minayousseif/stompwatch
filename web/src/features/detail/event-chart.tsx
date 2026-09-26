import { useCallback, useEffect, useRef, useState } from "react"

import type { EventDetail, EventMedia, Waveform } from "@/lib/api"
import { formatDb, LEVEL_MAX_DB, LEVEL_MIN_DB, verticalRampStops } from "@/lib/level"
import { formatClockSeconds } from "@/lib/time"
import { cn } from "@/lib/utils"

export interface ClipWindow {
  from: number
  to: number
  /**
   * "recorded" when the collector stored the time of the clip's first
   * sample. "worked out" when it did not, and the window was placed from the
   * end instead, using the post-roll in force now.
   */
  source: "recorded" | "worked out"
}

/**
 * Where the clip sits on the time axis.
 *
 * The collector records the time of the clip's first sample, so a clip
 * written since then is placed exactly. A clip written before that column
 * existed has to be placed from its end: the recorder keeps the run of
 * samples that **ends** one post-roll after the event, so the end is the only
 * anchor left. That fallback uses the post-roll in force now, which may not
 * be the one the clip was written under, so it is marked as worked out and
 * the interface says so.
 */
export function clipWindow(
  event: EventDetail,
  waveform: Waveform,
  media: EventMedia | null,
  postRollMs: number | null,
): ClipWindow | null {
  if (media && media.start_ms !== null) {
    return { from: media.start_ms, to: media.start_ms + waveform.duration_ms, source: "recorded" }
  }
  if (postRollMs === null) return null
  const to = event.ended_ms + postRollMs
  return { from: to - waveform.duration_ms, to, source: "worked out" }
}

/** Measure the container, so the drawing uses real pixels and never stretches. */
function useWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = useRef<T>(null)
  const [width, setWidth] = useState(720)
  useEffect(() => {
    const node = ref.current
    if (!node) return
    const observer = new ResizeObserver(([entry]) => {
      setWidth(Math.max(240, Math.round(entry.contentRect.width)))
    })
    observer.observe(node)
    return () => observer.disconnect()
  }, [])
  return [ref, width]
}

const GUTTER = 36
const AXIS_HEIGHT = 22
const GAP = 10

/** Where the bottom of the envelope strip sits, in dB below its own peak. */
const ENVELOPE_FLOOR_DB = -50

export interface ChartProps {
  event: EventDetail
  waveform: Waveform | null
  clip: { from: number; to: number } | null
  /** Where the playhead is, in epoch milliseconds, or null when stopped. */
  playheadMs: number | null
  /** Seek the clip to this time. Absent when there is nothing to play. */
  onSeek?: (ms: number) => void
  compact?: boolean
}

/**
 * The clip, the measured level, and the envelope, one above the other on one
 * timestamp axis. Lining them up is the whole point: the waveform says what it
 * sounded like, the trace says how loud it was, and the band says which part
 * of the window is the event itself.
 */
export function EventChart({ event, waveform, clip, playheadMs, onSeek, compact }: ChartProps) {
  const [ref, width] = useWidth<HTMLDivElement>()

  const waveHeight = compact ? 54 : 72
  const traceHeight = compact ? 118 : 150
  const envelopeHeight = compact ? 40 : 52

  const samples = event.samples
  const traceFrom = samples.length > 0 ? samples[0].t : event.started_ms - 30_000
  const traceTo =
    samples.length > 0 ? samples[samples.length - 1].t + 1000 : event.ended_ms + 30_000

  const from = Math.min(traceFrom, clip?.from ?? traceFrom)
  const to = Math.max(traceTo, clip?.to ?? traceTo)
  const span = Math.max(1, to - from)

  const plot = Math.max(1, width - GUTTER - 4)
  const x = useCallback((ms: number) => GUTTER + ((ms - from) / span) * plot, [from, span, plot])

  const envelope = event.envelope
  const hasEnvelope = !!envelope && envelope.values.length > 1

  const waveTop = 0
  const traceTop = waveTop + waveHeight + GAP
  const envelopeTop = traceTop + traceHeight + GAP
  const axisTop = hasEnvelope ? envelopeTop + envelopeHeight : traceTop + traceHeight
  const height = axisTop + AXIS_HEIGHT

  const eventLeft = x(event.started_ms)
  const eventRight = Math.max(eventLeft + 1, x(event.ended_ms))

  const seek = (clientX: number, target: SVGSVGElement) => {
    if (!onSeek || !clip) return
    const box = target.getBoundingClientRect()
    const at = from + ((clientX - box.left - GUTTER) / plot) * span
    onSeek(Math.min(clip.to, Math.max(clip.from, at)))
  }

  const ramp = verticalRampStops()
  const id = `event-${event.id}`

  return (
    <div ref={ref} className="w-full min-w-0">
      <svg
        width={width}
        height={height}
        // The drawing is measured against the screen, and a sheet of paper is
        // narrower than that. The viewBox lets the print stylesheet scale the
        // whole thing to the sheet instead of cutting it off.
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={`The clip and the measured level around event ${event.id}`}
        // max-w-full stops the drawing from widening its own container. The
        // width attribute is what the container last measured, and until the
        // observer has measured it the first time that number is a guess; a
        // guess wider than the screen pushed the whole page sideways.
        className={cn("event-chart max-w-full", onSeek && clip && "cursor-crosshair")}
        onClick={(e) => seek(e.clientX, e.currentTarget)}
      >
        <defs>
          <linearGradient id={`${id}-stroke`} x1="0" y1="0" x2="0" y2="1">
            {ramp.map((stop) => (
              <stop key={stop.offset} offset={stop.offset} stopColor={stop.color} />
            ))}
          </linearGradient>
          <linearGradient id={`${id}-fill`} x1="0" y1="0" x2="0" y2="1">
            {ramp.map((stop) => (
              <stop
                key={stop.offset}
                offset={stop.offset}
                stopColor={stop.color}
                stopOpacity={0.22}
              />
            ))}
          </linearGradient>
        </defs>

        {/* The event itself, behind everything, so every strip agrees on it.
            The class is there so print can darken it: 8% of black survives a
            screen but not a sheet of paper. */}
        <rect
          className="event-band"
          x={eventLeft}
          y={0}
          width={eventRight - eventLeft}
          height={axisTop}
          fill="var(--foreground)"
          fillOpacity={0.08}
        />
        <line
          x1={eventLeft}
          x2={eventLeft}
          y1={0}
          y2={axisTop}
          stroke="var(--foreground)"
          strokeOpacity={0.4}
        />
        <line
          x1={eventRight}
          x2={eventRight}
          y1={0}
          y2={axisTop}
          stroke="var(--foreground)"
          strokeOpacity={0.4}
        />

        <WaveStrip
          top={waveTop}
          height={waveHeight}
          waveform={waveform}
          clip={clip}
          x={x}
          left={GUTTER}
          right={GUTTER + plot}
        />

        <TraceStrip
          top={traceTop}
          height={traceHeight}
          event={event}
          x={x}
          left={GUTTER}
          right={GUTTER + plot}
          gradient={id}
        />

        {hasEnvelope && (
          <EnvelopeStrip
            top={envelopeTop}
            height={envelopeHeight}
            envelope={envelope}
            x={x}
            left={GUTTER}
            compact={compact}
          />
        )}

        <TimeAxis top={axisTop} from={from} to={to} x={x} width={plot} />

        {/* The playhead crosses every strip, so the ear and the eye agree. */}
        {playheadMs !== null && (
          <line
            x1={x(playheadMs)}
            x2={x(playheadMs)}
            y1={0}
            y2={axisTop}
            stroke="var(--foreground)"
            strokeWidth={1.5}
          />
        )}
      </svg>
    </div>
  )
}

/**
 * The stored clip, drawn where it falls on the shared axis. The peaks are
 * scaled to the loudest sample in this clip: the stored audio is low-pass
 * filtered and quiet in linear terms, so an unscaled drawing is a flat line.
 */
function WaveStrip({
  top,
  height,
  waveform,
  clip,
  x,
  left,
  right,
}: {
  top: number
  height: number
  waveform: Waveform | null
  clip: { from: number; to: number } | null
  x: (ms: number) => number
  left: number
  right: number
}) {
  const middle = top + height / 2

  if (!waveform || !clip || waveform.peaks.length === 0) {
    return (
      <>
        <rect x={left} y={top} width={right - left} height={height} fill="var(--muted)" />
        <text x={left + 8} y={middle + 4} fontSize={11} fill="var(--muted-foreground)">
          {waveform && !clip ? "The clip cannot be placed on this axis." : "No clip."}
        </text>
      </>
    )
  }

  const peak = waveform.peaks.reduce(
    (most, [low, high]) => Math.max(most, Math.abs(low), Math.abs(high)),
    0.0001,
  )
  const half = height / 2 - 1
  const bucketMs = waveform.duration_ms / waveform.peaks.length

  const tops: string[] = []
  const bottoms: string[] = []
  waveform.peaks.forEach(([low, high], index) => {
    const at = x(clip.from + index * bucketMs)
    tops.push(`${at.toFixed(1)},${(middle - (high / peak) * half).toFixed(1)}`)
    bottoms.push(`${at.toFixed(1)},${(middle - (low / peak) * half).toFixed(1)}`)
  })

  return (
    <>
      <rect
        x={x(clip.from)}
        y={top}
        width={Math.max(1, x(clip.to) - x(clip.from))}
        height={height}
        fill="var(--muted)"
        fillOpacity={0.5}
      />
      <line
        x1={x(clip.from)}
        x2={x(clip.to)}
        y1={middle}
        y2={middle}
        stroke="var(--border)"
        strokeWidth={1}
      />
      <polygon
        points={[...tops, ...bottoms.reverse()].join(" ")}
        fill="var(--foreground)"
        fillOpacity={0.75}
      />
    </>
  )
}

/** The measured level, one second per point, on the fixed dB scale. */
function TraceStrip({
  top,
  height,
  event,
  x,
  left,
  right,
  gradient,
}: {
  top: number
  height: number
  event: EventDetail
  x: (ms: number) => number
  left: number
  right: number
  gradient: string
}) {
  const y = (db: number) => {
    const fraction = (db - LEVEL_MIN_DB) / (LEVEL_MAX_DB - LEVEL_MIN_DB)
    return top + height - Math.min(1, Math.max(0, fraction)) * height
  }

  const ticks = [30, 40, 50, 60, 70, 80].filter((db) => db > LEVEL_MIN_DB && db < LEVEL_MAX_DB)
  const points = event.samples

  // A second with no reading breaks the line. A straight line across a gap
  // would claim a measurement nobody made.
  const runs: { t: number; laeq: number; lamax: number }[][] = []
  let run: { t: number; laeq: number; lamax: number }[] = []
  for (let i = 0; i < points.length; i++) {
    const point = points[i]
    const previous = points[i - 1]
    const broken = point.laeq === null || point.lamax === null
    const jumped = previous !== undefined && point.t - previous.t > 1000
    if (broken || jumped) {
      if (run.length > 0) runs.push(run)
      run = []
    }
    if (!broken) run.push({ t: point.t, laeq: point.laeq!, lamax: point.lamax! })
  }
  if (run.length > 0) runs.push(run)

  const baseline = points.filter((point) => point.baseline !== null && point.baseline !== undefined)

  return (
    <>
      {ticks.map((db) => (
        <g key={db}>
          <line
            x1={left}
            x2={right}
            y1={y(db)}
            y2={y(db)}
            stroke="var(--border)"
            strokeOpacity={0.5}
            strokeDasharray="2 4"
          />
          <text
            x={left - 6}
            y={y(db) + 3}
            fontSize={10}
            textAnchor="end"
            fill="var(--muted-foreground)"
          >
            {db}
          </text>
        </g>
      ))}

      {runs.map((one, index) => (
        <g key={index}>
          <polygon
            points={[
              `${x(one[0].t)},${y(LEVEL_MIN_DB)}`,
              ...one.map((point) => `${x(point.t)},${y(point.lamax)}`),
              `${x(one[one.length - 1].t)},${y(LEVEL_MIN_DB)}`,
            ].join(" ")}
            fill={`url(#${gradient}-fill)`}
          />
          <polyline
            points={one.map((point) => `${x(point.t)},${y(point.laeq)}`).join(" ")}
            fill="none"
            stroke={`url(#${gradient}-stroke)`}
            strokeWidth={1.5}
          />
        </g>
      ))}

      {/* The baseline is the line the trigger level is measured from. */}
      {baseline.length > 1 && (
        <polyline
          points={baseline.map((point) => `${x(point.t)},${y(point.baseline!)}`).join(" ")}
          fill="none"
          stroke="var(--muted-foreground)"
          strokeWidth={1}
          strokeDasharray="4 3"
        />
      )}
    </>
  )
}

/**
 * The stored envelope, 100 values a second. The values are linear, so they
 * are drawn as dB below the loudest one in this event, not as dB SPL: nothing
 * in the response says what level a linear one would stand for.
 */
function EnvelopeStrip({
  top,
  height,
  envelope,
  x,
  left,
  compact,
}: {
  top: number
  height: number
  envelope: { rate_hz: number; start_ms: number; values: number[] }
  x: (ms: number) => number
  left: number
  compact?: boolean
}) {
  const peak = envelope.values.reduce((most, value) => Math.max(most, value), 1e-9)
  const step = 1000 / envelope.rate_hz
  const y = (db: number) => top + height - (1 - db / ENVELOPE_FLOOR_DB) * height

  const points = envelope.values.map((value, index) => {
    const db = Math.max(ENVELOPE_FLOOR_DB, 20 * Math.log10(Math.max(value, 1e-9) / peak))
    return `${x(envelope.start_ms + index * step).toFixed(1)},${y(db).toFixed(1)}`
  })

  const first = x(envelope.start_ms)
  const last = x(envelope.start_ms + (envelope.values.length - 1) * step)

  return (
    <>
      <polygon
        points={[`${first},${top + height}`, ...points, `${last},${top + height}`].join(" ")}
        fill="var(--foreground)"
        fillOpacity={0.35}
      />
      <text x={left} y={top + 10} fontSize={10} fill="var(--muted-foreground)">
        {compact
          ? `Envelope, dB below its peak`
          : `Envelope, ${envelope.rate_hz} a second, dB below this event's loudest sample`}
      </text>
    </>
  )
}

/** The one time axis both strips are drawn against. */
function TimeAxis({
  top,
  from,
  to,
  x,
  width,
}: {
  top: number
  from: number
  to: number
  x: (ms: number) => number
  width: number
}) {
  const span = to - from
  // Roughly one label every 110 px, on a whole number of seconds.
  const wanted = Math.max(2, Math.round(width / 110))
  const rough = span / wanted / 1000
  const steps = [1, 2, 5, 10, 15, 20, 30, 60, 120, 300]
  const step = (steps.find((one) => one >= rough) ?? 600) * 1000
  const first = Math.ceil(from / step) * step

  // A label is centered on its tick, so one within half a label of the right
  // edge is cut off by the edge of the drawing. Half a label is about 18 px.
  const lastLabel = x(to) - 18

  const ticks: number[] = []
  for (let t = first; t <= to; t += step) {
    if (x(t) <= lastLabel) ticks.push(t)
  }

  return (
    <g>
      <line x1={x(from)} x2={x(to)} y1={top} y2={top} stroke="var(--border)" />
      {ticks.map((t) => (
        <g key={t}>
          <line x1={x(t)} x2={x(t)} y1={top} y2={top + 4} stroke="var(--border)" />
          <text
            x={x(t)}
            y={top + 16}
            fontSize={10}
            textAnchor="middle"
            fill="var(--muted-foreground)"
          >
            {formatClockSeconds(t)}
          </text>
        </g>
      ))}
    </g>
  )
}

/** What the drawing shows, said once, under it. */
export function ChartKey({
  event,
  clip,
  shortfallMs,
  hasClipFile,
}: {
  event: EventDetail
  clip: ClipWindow | null
  /**
   * How much of the front of the clip the buffer could not supply, or null
   * when nobody measured it.
   */
  shortfallMs: number | null
  /** False when there is no clip at all, so there is nothing to line up. */
  hasClipFile: boolean
}) {
  return (
    <div className="mt-3 flex flex-col gap-1 text-xs text-muted-foreground">
      <p>
        The shaded band is the event, {formatClockSeconds(event.started_ms)} to{" "}
        {formatClockSeconds(event.ended_ms)}. The trace is one second per point, on the same{" "}
        {LEVEL_MIN_DB} to {LEVEL_MAX_DB} dB scale as the timeline. The dashed line is the baseline,
        which was {formatDb(event.baseline_at_trigger)} dB when the event triggered.
      </p>
      {clip ? (
        <>
          <p>
            The clip runs {formatClockSeconds(clip.from)} to {formatClockSeconds(clip.to)}, and its
            waveform is scaled to its own loudest sample.
            {shortfallMs !== null &&
              shortfallMs >= 100 &&
              ` It starts ${(shortfallMs / 1000).toFixed(1)} s late: the buffer did not hold that much of the run-up.`}
          </p>
          {clip.source === "worked out" && (
            <p>
              This clip was stored before the collector recorded where it starts, so its place on
              the axis is worked out from its end and the post-roll in force now. If that setting
              has changed since, the waveform sits a little off the trace.
            </p>
          )}
        </>
      ) : hasClipFile ? (
        <p>
          The clip cannot be placed on this axis, because the roll settings did not load. Only the
          trace is drawn against it.
        </p>
      ) : null}
    </div>
  )
}
