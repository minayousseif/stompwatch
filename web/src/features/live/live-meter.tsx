import { Panel } from "@/components/state"
import { useLive, type Live } from "@/hooks/use-live"
import { formatDb, horizontalRampCss, levelFraction, LEVEL_MAX_DB, LEVEL_MIN_DB } from "@/lib/level"
import { formatClockSeconds } from "@/lib/time"
import { cn } from "@/lib/utils"

/**
 * The live meter. A number that has stopped updating but still looks live is
 * the worst thing this screen can do, so the state of the stream is always on
 * screen next to the reading, and a reading that is not current is dimmed.
 */
export function LiveMeter() {
  const live = useLive()
  const { reading, current } = live
  const level = reading?.laeq ?? null

  return (
    <Panel title="Live" aside={<StreamState live={live} />} bodyClassName="p-4 sm:p-5">
      <div className="flex flex-wrap items-end gap-x-8 gap-y-4">
        <div>
          <div className="flex items-baseline gap-1.5">
            <span
              className={cn(
                "text-5xl leading-none font-medium sm:text-6xl",
                current ? "text-foreground" : "text-muted-foreground",
              )}
            >
              {formatDb(level)}
            </span>
            <span className="text-base text-muted-foreground">dB</span>
          </div>
          <p className="mt-1.5 text-xs text-muted-foreground">A-weighted, one second</p>
        </div>

        <dl className="flex gap-8 text-sm">
          <Readout label="Peak" value={formatDb(reading?.lamax)} dim={!current} />
          <Readout label="Baseline" value={formatDb(reading?.baseline)} dim={!current} />
        </dl>
      </div>

      <MeterBar level={level} baseline={reading?.baseline ?? null} dim={!current} />

      <p className="mt-3 text-xs text-muted-foreground">{explain(live)}</p>
    </Panel>
  )
}

/** One sentence saying what the meter is doing, and what to make of it. */
function explain({ reading, connection, silentFor, current }: Live): string {
  if (connection === "lost") {
    return "The dashboard lost the live stream. It keeps trying to reconnect."
  }
  if (reading === null || reading.laeq === null) {
    return "Waiting for the first second of measurement."
  }
  if (reading.stale) {
    return `Measurement stopped. This reading is from ${formatClockSeconds(reading.t)}.`
  }
  if (!current) {
    return `No reading for ${silentFor} seconds. This one is from ${formatClockSeconds(reading.t)}.`
  }
  return `Updated ${formatClockSeconds(reading.t)}`
}

function Readout({ label, value, dim }: { label: string; value: string; dim: boolean }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={cn("text-lg", dim ? "text-muted-foreground" : "text-foreground")}>
        {value} <span className="text-xs text-muted-foreground">dB</span>
      </dd>
    </div>
  )
}

function StreamState({ live }: { live: Live }) {
  if (live.connection === "lost") return <span className="text-destructive">Reconnecting</span>
  if (live.connection === "connecting") return <span>Connecting</span>
  if (live.reading === null) return <span>Connected</span>
  if (!live.current) return <span className="text-unsure">Not measuring</span>
  return <span>Measuring</span>
}

/**
 * The scale a sound level meter has always had: where this reading sits
 * between quiet and loud, with the baseline marked so the trigger threshold
 * has something to mean.
 */
function MeterBar({
  level,
  baseline,
  dim,
}: {
  level: number | null
  baseline: number | null
  dim: boolean
}) {
  const filled = level === null ? 0 : levelFraction(level)
  const mark = baseline === null ? null : levelFraction(baseline)

  return (
    <div className="mt-5">
      <div
        className="relative h-3 w-full overflow-hidden rounded-xs bg-muted"
        role="img"
        aria-label={
          level === null
            ? `The scale runs from ${LEVEL_MIN_DB} to ${LEVEL_MAX_DB} dB. There is no reading.`
            : `The scale runs from ${LEVEL_MIN_DB} to ${LEVEL_MAX_DB} dB. The level is ${level.toFixed(
                1,
              )} dB.`
        }
      >
        <div
          className={cn("h-full", dim && "opacity-35")}
          style={{
            width: `${(filled * 100).toFixed(2)}%`,
            backgroundImage: horizontalRampCss(),
            // The gradient stays fixed to the scale, so a given level is
            // always the same color whatever the bar's length.
            backgroundSize: `${(100 / Math.max(filled, 0.001)).toFixed(2)}% 100%`,
          }}
        />
        {mark !== null && (
          <div
            className="absolute inset-y-0 w-px bg-foreground/60"
            style={{ left: `${(mark * 100).toFixed(2)}%` }}
            aria-hidden
          />
        )}
      </div>
      {/* The note stays beside the low end of the scale, where the baseline
          tick usually sits, rather than floating in the middle of a wide bar. */}
      <div className="mt-1 flex justify-between gap-4 text-xs text-muted-foreground">
        <span>
          {LEVEL_MIN_DB} dB
          {baseline === null ? "" : " - the line marks the baseline"}
        </span>
        <span>{LEVEL_MAX_DB} dB</span>
      </div>
    </div>
  )
}
