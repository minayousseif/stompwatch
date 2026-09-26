import { Fact, Group } from "@/components/state"
import { accuracySentence, formatDay } from "@/lib/accuracy"
import { TailscaleFacts, TailscaleNotice } from "@/features/system/tailscale"
import type { SystemStatus } from "@/lib/api"
import { formatAgo, formatDayAndSeconds, formatSpan } from "@/lib/time"
import { cn } from "@/lib/utils"

/**
 * The question the owner opens this screen to answer, answered first and in
 * words. A dot on its own tells you the color of a dot.
 */
export function CollectingNote({ status }: { status: SystemStatus }) {
  const audio = formatAgo(status.last_audio_ms, status.now_ms)
  const commit = formatAgo(status.last_commit_ms, status.now_ms)

  return (
    <div className="flex flex-col gap-2">
      <p className="flex items-center gap-2.5 text-base">
        <span
          className={cn(
            "size-2.5 shrink-0 rounded-full",
            status.collecting ? "bg-verified" : "bg-rejected",
          )}
          aria-hidden
        />
        {status.collecting
          ? "The collector is measuring."
          : "The collector is not measuring right now."}
      </p>
      <p className="max-w-prose text-sm text-muted-foreground">
        Audio last arrived {audio}, and measurements last reached the disk {commit}.{" "}
        {status.collecting
          ? "Both are inside the window the heartbeat allows, so nothing is being missed."
          : "Audio has stopped, or it is not reaching the disk. The health log below says what the collector noticed."}
      </p>
      {/* The pause explains a stretch with no events, which would otherwise
          look like the detector had stopped working. Optional chaining,
          because a collector older than the pause sends no such block. */}
      {status.recording_pause?.active && (
        <p className="max-w-prose text-sm text-muted-foreground">
          <span className="text-foreground">
            Recording is paused until {status.recording_pause.until}.
          </span>{" "}
          In the daily pause no event opens and no audio clip or video is recorded. The level of
          every second is still measured.
        </p>
      )}
    </div>
  )
}

/**
 * What is capturing, where it is stored, how long it has been up, and how
 * the box is reached from outside.
 */
export function SystemFacts({ status }: { status: SystemStatus }) {
  const uptime = formatSpan(status.now_ms - status.started_ms)

  return (
    <div className="grid gap-x-8 gap-y-6 lg:grid-cols-3">
      <Group title="Capture">
        <Fact label="Device" value={status.capture.device} />
        <Fact label="Channel" value={String(status.capture.channel)} />
        <Fact label="Gain" value={status.capture.gain} />
        <Fact label="Sensitivity" value={`${status.capture.sensitivity_dbfs.toFixed(1)} dBFS`} />
        <Fact label="Calibration" value={status.capture.calibration} />
      </Group>

      <LevelAccuracyFacts levels={status.levels} />

      <Group title="Storage">
        {status.disk.map((one) => (
          <Fact
            key={one.name}
            label={`Free space for the ${one.name}`}
            value={formatMB(one.free_mb)}
          />
        ))}
        <Fact label="Database" value={formatBytes(status.db_bytes)} />
        <Fact label="Write-ahead log" value={formatBytes(status.wal_bytes)} />
        <Fact
          label="Clips"
          value={`${status.clips.count}`}
          note={formatBytes(status.clips.bytes)}
        />
        <Fact label="Schema version" value={String(status.schema_version)} />
      </Group>

      <Group title="Running">
        <Fact
          label="Up for"
          value={uptime}
          note={`since ${formatDayAndSeconds(status.started_ms)}`}
        />
        <Fact
          label="Heartbeat"
          value={status.heartbeat.configured ? "Set up" : "Not set up"}
          note={
            status.heartbeat.configured
              ? `every ${formatSpan(status.heartbeat.interval_ms)}`
              : "nothing is watching from outside"
          }
        />
        <Fact label="Events today" value={String(status.counts.events_today)} />
        <Fact label="Events in all" value={String(status.counts.events_total)} />
        <Fact label="Waiting for a decision" value={String(status.counts.unreviewed)} />
        <Fact label="Seconds measured today" value={status.counts.samples_today.toLocaleString()} />
        <Fact label="Signed in as" value={`${status.auth.login} (${status.auth.mode})`} />
      </Group>

      <TailscaleFacts ts={status.tailscale} />

      {/* What to do about Tailscale, when there is something to do. It
            takes the rest of the row beside its own readings, because a
            fault needs room for a sentence and a command. */}
      {/* min-w-0 so the command, which cannot wrap, scrolls inside its own
          box rather than widening the whole grid column at 375 px. */}
      <div className="min-w-0 lg:col-span-2">
        <TailscaleNotice ts={status.tailscale} />
      </div>
    </div>
  )
}

/**
 * How well the levels are known. Every level the instrument reports rests on
 * one constant, so this belongs beside Capture and not in a footnote.
 *
 * The sentence is built from the readings, never written out here. A fixed
 * sentence would go on saying "plus or minus 2 dB" the day the owner
 * measures the real sensitivity (SPEC.md section 15 decision 23).
 */
export function LevelAccuracyFacts({ levels }: { levels: SystemStatus["levels"] }) {
  const measured = levels.source === "measured"
  return (
    <div>
      <Group title="Levels">
        <Fact
          label="Accurate to"
          value={`plus or minus ${Number(levels.uncertainty_db.toFixed(2))} dB`}
        />
        <Fact
          label="Sensitivity is"
          value={measured ? "Measured" : "The datasheet figure"}
          note={measured ? undefined : "the manufacturer's figure for the model"}
        />
        {measured && (
          <Fact label="Measured on" value={formatDay(levels.measured_on) || "Not recorded"} />
        )}
        {measured && <Fact label="Against" value={levels.reference || "Not recorded"} />}
      </Group>
      <p className="mt-2 max-w-prose text-xs text-muted-foreground">
        {accuracySentence(levels)} Every level on every screen carries this, because every one of
        them is worked out from that one figure. An event states the accuracy it was recorded under,
        which is not always this one.
      </p>
    </div>
  )
}

/**
 * What the pipeline dropped or failed to write. A zero is the good news, so
 * a zero reads as a word and only a real count is set in the plain color.
 */
export function PipelineCounters({ status }: { status: SystemStatus }) {
  const counters: { label: string; count: number; what: string }[] = [
    {
      label: "Dropped samples",
      count: status.pipeline.dropped_samples,
      what: "audio the meter never saw",
    },
    {
      label: "Dropped bins",
      count: status.pipeline.bins_dropped,
      what: "seconds that never reached the writer",
    },
    {
      label: "Dropped events",
      count: status.pipeline.events_dropped,
      what: "detections the writer could not take",
    },
    {
      label: "Write failures",
      count: status.pipeline.write_failures,
      what: "batches the database refused",
    },
    {
      label: "Loop restarts",
      count: status.pipeline.loop_restarts,
      what: "times capture had to start again",
    },
    {
      label: "Camera disconnects",
      count: status.camera.disconnects,
      what: "times the video stream ended or stalled",
    },
  ]
  const bad = counters.filter((one) => one.count > 0)

  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm">
        {bad.length === 0
          ? "Nothing has been dropped since the collector started."
          : `${bad.length === 1 ? "One counter is" : `${bad.length} counters are`} above zero. Every count below is measurement that did not survive.`}
      </p>
      <dl className="grid gap-x-8 text-sm sm:grid-cols-2 lg:grid-cols-3">
        {counters.map((one) => (
          <Fact
            key={one.label}
            label={one.label}
            value={one.count === 0 ? "None" : one.count.toLocaleString()}
            note={one.count === 0 ? undefined : one.what}
            quiet={one.count === 0}
          />
        ))}
      </dl>
      <p className="max-w-prose text-xs text-muted-foreground">
        These count what the running process has lost since it started. They answer whether the
        instrument is working. The health log below says what happened and when.
      </p>
    </div>
  )
}

/** Free space, as the owner would say it. */
export function formatMB(mb: number): string {
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`
  return `${mb.toFixed(0)} MB`
}

export function formatBytes(bytes: number): string {
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(2)} GB`
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MB`
  return `${(bytes / 1024).toFixed(0)} kB`
}
