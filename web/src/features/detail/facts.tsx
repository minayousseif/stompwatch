import { Fact } from "@/components/state"
import { accuracySentence } from "@/lib/accuracy"
import type { EventCapture, EventDetail } from "@/lib/api"
import { formatDb, levelColor } from "@/lib/level"
import { CLASS_WORD } from "@/lib/query"
import { formatClockSeconds, formatDayAndClock, formatDuration } from "@/lib/time"

/** Everything the detector recorded about this event, in reading order. */
export function EventFacts({ event, maxEventS }: { event: EventDetail; maxEventS: number | null }) {
  return (
    <div className="flex flex-col gap-4">
      <dl className="grid gap-x-8 text-sm sm:grid-cols-2">
        <Fact label="Started" value={formatDayAndClock(event.started_ms)} />
        <Fact label="Ended" value={formatClockSeconds(event.ended_ms)} />
        <Fact label="Length" value={formatDuration(event.duration_ms)} />
        <Fact
          label="Peak level"
          value={`${formatDb(event.lamax)} dB`}
          swatch={levelColor(event.lamax)}
        />
        <Fact label="Average level" value={`${formatDb(event.laeq)} dB`} />
        <Fact label="Baseline at trigger" value={`${formatDb(event.baseline_at_trigger)} dB`} />
        <Fact
          label="Above the baseline"
          value={`${formatDb(event.lamax - event.baseline_at_trigger)} dB`}
        />
        <Fact
          label="Above the 30 s before"
          value={event.jump_db === null ? "Not measured" : `${formatDb(event.jump_db)} dB`}
        />
        <Fact
          label="Rise in one second"
          value={event.rise_db === null ? "Not measured" : `${formatDb(event.rise_db)} dB`}
        />
        <Fact
          label="Class"
          value={`${CLASS_WORD[event.class]}, ${(event.confidence * 100).toFixed(0)}% confident`}
        />
        <Fact label="Low band" value={`${formatDb(event.low_energy)} dB`} />
        <Fact label="High band" value={`${formatDb(event.high_energy)} dB`} />
        <Fact label="Low minus high" value={`${formatDb(event.low_high_ratio)} dB`} />
        <Fact label="Closed by the clock" value={event.forced ? "Yes" : "No"} />
      </dl>

      {/* Said here, next to the levels it applies to, and quietly: it is a
          property of every reading above, not a warning about this one. */}
      <p className="max-w-prose text-xs text-muted-foreground">{accuracyLine(event.capture)}</p>

      {event.forced && (
        <p className="max-w-prose text-xs text-muted-foreground">
          This event did not end by itself. It ran to the longest event the settings allow
          {maxEventS === null ? "" : `, ${maxEventS} seconds`}, and the detector closed it. The
          noise may have carried on into the next event.
        </p>
      )}
    </div>
  )
}

/**
 * How well the levels above are known. It comes from the settings in force
 * when this event was recorded, not from the settings in force now: an event
 * measured before the last calibration carries the accuracy it had
 * (SPEC.md section 15 decision 23).
 */
function accuracyLine(capture: EventCapture | null): string {
  if (!capture) {
    return (
      "How accurate these levels are is not known. This event was recorded before the collector " +
      "began keeping a record of the settings in force, and today's settings are not those " +
      "settings."
    )
  }
  return accuracySentence(capture)
}
