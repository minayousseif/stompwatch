import {
  Area,
  CartesianGrid,
  ComposedChart,
  Line,
  ReferenceArea,
  ReferenceDot,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts"

import { useIsMobile } from "@/hooks/use-mobile"
import type { Timeline, TimelinePoint } from "@/lib/api"
import {
  formatDb,
  horizontalRampCss,
  levelColor,
  LEVEL_MAX_DB,
  LEVEL_MIN_DB,
  verticalRampStops,
} from "@/lib/level"
import { formatClock } from "@/lib/time"

/**
 * A point the chart can draw. A null level breaks the trace, which is how a
 * gap in the measurement stays a gap.
 */
type Drawn = {
  t: number
  laeq: number | null
  lamax: number | null
  /** Only 1-second points carry a baseline. Minute rows have none. */
  baseline: number | null
}

/**
 * The API leaves an empty bucket out. Put an explicit hole back in, or the
 * chart draws a straight line across a stretch where nothing was measured.
 */
export function withGaps(points: TimelinePoint[], bucketMs: number): Drawn[] {
  const drawn: Drawn[] = []
  for (let i = 0; i < points.length; i++) {
    const point = points[i]
    const previous = points[i - 1]
    if (previous && point.t - previous.t > bucketMs) {
      drawn.push({ t: previous.t + bucketMs, laeq: null, lamax: null, baseline: null })
    }
    drawn.push({
      t: point.t,
      laeq: point.laeq,
      lamax: point.lamax,
      baseline: point.baseline ?? null,
    })
  }
  return drawn
}

/** Whole hours across the range, thinned until the labels fit. */
function hourTicks(from: number, to: number, most: number): number[] {
  const hour = 3_600_000
  const first = Math.ceil(from / hour) * hour
  const hours = Math.max(1, Math.round((to - from) / hour))
  const step = Math.max(1, Math.ceil(hours / most))
  const ticks: number[] = []
  for (let t = first; t <= to; t += hour * step) ticks.push(t)
  return ticks
}

const Y_TICKS = [30, 40, 50, 60, 70, 80]

const horizontalRamp = horizontalRampCss()

/**
 * The dBA trace for the night. The level decides the color, the scale is
 * fixed so two nights compare, quiet hours are shaded as ground truth, and
 * each event sits on the trace at its own peak.
 */
export function NightTimeline({ timeline }: { timeline: Timeline }) {
  const isMobile = useIsMobile()
  const data = withGaps(timeline.points, timeline.bucket_ms)
  const ticks = hourTicks(timeline.from, timeline.to, isMobile ? 4 : 9)
  const stops = verticalRampStops()

  return (
    <div className="h-[220px] w-full min-w-0 sm:h-[300px] lg:h-[340px]">
      {/* The first render happens before the container has been measured, so
          give it a size to start from rather than letting it draw at -1. */}
      <ResponsiveContainer
        width="100%"
        height="100%"
        minWidth={0}
        initialDimension={{ width: 720, height: 300 }}
      >
        <ComposedChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: -8 }}>
          <defs>
            {/* The ramp runs down the plot, so a point's color is its level.
                It only lines up because the y scale is fixed. */}
            <linearGradient id="level-stroke" x1="0" y1="0" x2="0" y2="1">
              {stops.map((stop) => (
                <stop key={stop.offset} offset={stop.offset} stopColor={stop.color} />
              ))}
            </linearGradient>
            <linearGradient id="level-fill" x1="0" y1="0" x2="0" y2="1">
              {stops.map((stop) => (
                <stop
                  key={stop.offset}
                  offset={stop.offset}
                  stopColor={stop.color}
                  stopOpacity={0.24}
                />
              ))}
            </linearGradient>
          </defs>

          <CartesianGrid
            vertical={false}
            stroke="var(--border)"
            strokeOpacity={0.5}
            strokeDasharray="2 4"
          />

          {/* Quiet hours are the ground truth the whole record rests on. */}
          {timeline.quiet.map((span) => (
            <ReferenceArea
              key={`${span.from}-${span.to}`}
              x1={Math.max(span.from, timeline.from)}
              x2={Math.min(span.to, timeline.to)}
              fill="var(--foreground)"
              fillOpacity={0.05}
              stroke="var(--border)"
              strokeOpacity={0.6}
              ifOverflow="hidden"
            />
          ))}

          <XAxis
            dataKey="t"
            type="number"
            scale="time"
            domain={[timeline.from, timeline.to]}
            ticks={ticks}
            tickFormatter={(value: number) => formatClock(value)}
            tickLine={false}
            axisLine={{ stroke: "var(--border)" }}
            tick={{ fill: "var(--muted-foreground)", fontSize: 11 }}
            minTickGap={8}
          />
          <YAxis
            domain={[LEVEL_MIN_DB, LEVEL_MAX_DB]}
            ticks={Y_TICKS}
            tickFormatter={(value: number) => String(value)}
            tickLine={false}
            axisLine={false}
            width={38}
            tick={{ fill: "var(--muted-foreground)", fontSize: 11 }}
            label={undefined}
          />

          {/* The loudest part of each bucket, filled. This is where impacts show. */}
          <Area
            dataKey="lamax"
            type="monotone"
            baseValue={LEVEL_MIN_DB}
            stroke="none"
            fill="url(#level-fill)"
            isAnimationActive={false}
            connectNulls={false}
          />
          {/* The average level, which is the trace itself. */}
          <Line
            dataKey="laeq"
            type="monotone"
            stroke="url(#level-stroke)"
            strokeWidth={1.5}
            dot={false}
            activeDot={{ r: 3, fill: "var(--foreground)", stroke: "none" }}
            isAnimationActive={false}
            connectNulls={false}
          />

          {/* The baseline the trigger level is measured from. It is the most
              useful context on the chart: an event is a rise above this line,
              not above a fixed number. */}
          <Line
            dataKey="baseline"
            type="monotone"
            stroke="var(--muted-foreground)"
            strokeWidth={1}
            strokeDasharray="4 3"
            dot={false}
            activeDot={false}
            isAnimationActive={false}
            connectNulls={false}
          />

          {/* Every detected event, as structure behind the trace. */}
          {timeline.events.map((event) => (
            <ReferenceLine
              key={`line-${event.id}`}
              x={event.started_ms}
              stroke={levelColor(event.lamax)}
              strokeOpacity={0.35}
              strokeWidth={1}
              ifOverflow="hidden"
            />
          ))}
          {timeline.events.map((event) => (
            <ReferenceDot
              key={`dot-${event.id}`}
              x={event.started_ms}
              y={Math.min(event.lamax, LEVEL_MAX_DB)}
              r={3}
              fill={levelColor(event.lamax)}
              stroke="var(--background)"
              strokeWidth={1}
              ifOverflow="hidden"
            />
          ))}

          <Tooltip
            content={<TraceTooltip />}
            cursor={{ stroke: "var(--muted-foreground)", strokeWidth: 1 }}
            isAnimationActive={false}
          />
        </ComposedChart>
      </ResponsiveContainer>
    </div>
  )
}

/** What the colors and the shading mean, said once, under the chart. */
export function TimelineKey({
  quietHours,
  baseline,
}: {
  quietHours: boolean
  /** True when the points carry a baseline, which only seconds do. */
  baseline: boolean
}) {
  return (
    <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-muted-foreground">
      <div className="flex items-center gap-2">
        <span>{LEVEL_MIN_DB} dB</span>
        <span
          className="h-2 w-24 rounded-xs sm:w-32"
          style={{ backgroundImage: horizontalRamp }}
          aria-hidden
        />
        <span>{LEVEL_MAX_DB} dB</span>
      </div>
      <span>A dot marks an event, at its peak.</span>
      {baseline && <span>The dashed line is the baseline.</span>}
      {quietHours && <span>The shaded band is quiet hours.</span>}
    </div>
  )
}

interface TooltipPayload {
  payload?: { payload: Drawn }[]
  active?: boolean
}

function TraceTooltip({ active, payload }: TooltipPayload) {
  const point = payload?.[0]?.payload
  if (!active || !point) return null
  if (point.laeq === null && point.lamax === null) {
    return (
      <div className="rounded-md border border-border bg-popover px-2.5 py-2 text-xs shadow-none">
        <p className="text-popover-foreground">{formatClock(point.t)}</p>
        <p className="text-muted-foreground">Nothing was measured.</p>
      </div>
    )
  }
  return (
    <div className="rounded-md border border-border bg-popover px-2.5 py-2 text-xs">
      <p className="text-popover-foreground">{formatClock(point.t)}</p>
      <p className="mt-1 text-muted-foreground">Average {formatDb(point.laeq)} dB</p>
      <p className="text-muted-foreground">Peak {formatDb(point.lamax)} dB</p>
      {point.baseline !== null && (
        <p className="text-muted-foreground">Baseline {formatDb(point.baseline)} dB</p>
      )}
    </div>
  )
}
