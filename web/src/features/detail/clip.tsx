import { useCallback, useEffect, useRef, useState } from "react"
import { DownloadIcon, PauseIcon, PlayIcon, RotateCcwIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { url, type EventMedia } from "@/lib/api"
import { formatClockSeconds, formatDay, formatDuration } from "@/lib/time"

export interface ClipPlayer {
  playing: boolean
  /** Where the playhead is inside the clip, in seconds. */
  position: number
  /** How long the browser says the clip is, in seconds. Zero until it knows. */
  length: number
  /** The playhead as an epoch time, so the chart can draw it. */
  playheadMs: number | null
  /** True when the browser could not load or play the clip. */
  failed: boolean
  toggle: () => void
  restart: () => void
  /** Move the playhead to an epoch time inside the clip. */
  seekTo: (ms: number) => void
  /** Spread these on the `audio` element the player drives. */
  audioProps: React.AudioHTMLAttributes<HTMLAudioElement> & {
    ref: React.RefObject<HTMLAudioElement | null>
  }
}

/**
 * Playback for one clip. The position follows the frame rate while the clip
 * plays, because `timeupdate` fires about four times a second and a playhead
 * that moves four times a second reads as a stutter.
 */
export function useClipPlayer(clipFrom: number | null): ClipPlayer {
  const audio = useRef<HTMLAudioElement>(null)
  const [playing, setPlaying] = useState(false)
  const [position, setPosition] = useState(0)
  const [length, setLength] = useState(0)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (!playing) return
    let frame = 0
    const follow = () => {
      if (audio.current) setPosition(audio.current.currentTime)
      frame = window.requestAnimationFrame(follow)
    }
    frame = window.requestAnimationFrame(follow)
    return () => window.cancelAnimationFrame(frame)
  }, [playing])

  const toggle = useCallback(() => {
    const node = audio.current
    if (!node) return
    if (node.paused) void node.play().catch(() => setFailed(true))
    else node.pause()
  }, [])

  const restart = useCallback(() => {
    const node = audio.current
    if (!node) return
    node.currentTime = 0
    setPosition(0)
  }, [])

  const seekTo = useCallback(
    (ms: number) => {
      const node = audio.current
      if (!node || clipFrom === null) return
      const seconds = Math.max(0, (ms - clipFrom) / 1000)
      node.currentTime = seconds
      setPosition(seconds)
    },
    [clipFrom],
  )

  return {
    playing,
    position,
    length,
    failed,
    playheadMs: clipFrom === null ? null : clipFrom + position * 1000,
    toggle,
    restart,
    seekTo,
    audioProps: {
      ref: audio,
      preload: "metadata",
      onPlay: () => setPlaying(true),
      onPause: () => setPlaying(false),
      onEnded: () => setPlaying(false),
      onTimeUpdate: (e) => {
        if (!playing) setPosition(e.currentTarget.currentTime)
      },
      onLoadedMetadata: (e) => {
        const seconds = e.currentTarget.duration
        setLength(Number.isFinite(seconds) ? seconds : 0)
      },
      onError: () => setFailed(true),
    },
  }
}

/** Play, pause, start again, and where in the clip the playhead is. */
export function ClipTransport({
  player,
  durationMs,
  gainDb,
  tone,
  onTone,
}: {
  player: ClipPlayer
  durationMs: number | null
  /** How much playback is raised above the stored level, in dB. */
  gainDb?: number | null
  tone?: "tilt" | "flat"
  onTone?: (tone: "tilt" | "flat") => void
}) {
  const whole = player.length > 0 ? player.length : (durationMs ?? 0) / 1000

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
      <Button variant="outline" size="sm" className="h-8 gap-1.5" onClick={player.toggle}>
        {player.playing ? (
          <PauseIcon className="size-4" aria-hidden />
        ) : (
          <PlayIcon className="size-4" aria-hidden />
        )}
        {player.playing ? "Pause" : "Play"}
      </Button>
      <Button
        variant="ghost"
        size="sm"
        className="h-8 gap-1.5 text-muted-foreground"
        onClick={player.restart}
      >
        <RotateCcwIcon className="size-4" aria-hidden />
        Start again
      </Button>
      <p className="text-sm">
        {clock(player.position)} <span className="text-muted-foreground">of {clock(whole)}</span>
      </p>
      <p className="text-xs text-muted-foreground">Click the waveform to move the playhead.</p>
      {tone !== undefined && onTone !== undefined && (
        <div className="flex items-center gap-2 text-xs">
          <button
            type="button"
            onClick={() => onTone(tone === "tilt" ? "flat" : "tilt")}
            className="rounded-sm border border-border px-2 py-1 text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
            title={
              tone === "tilt"
                ? "Playing the clip shaped for a small speaker. A clip holds 20 to 450 Hz " +
                  "and most of its energy is below 80 Hz, which a phone cannot reproduce. " +
                  "Switch to hear the balance the microphone recorded."
                : "Playing the balance the microphone recorded. On a phone speaker most of " +
                  "this is below what the speaker can make."
            }
          >
            {tone === "tilt" ? "Shaped for a small speaker" : "As recorded"}
          </button>
        </div>
      )}
      {gainDb !== null && gainDb !== undefined && gainDb > 0.1 && (
        <p
          className="text-xs text-muted-foreground"
          title={
            "A household impact is recorded tens of dB below full scale, so a faithful clip is " +
            "close to inaudible. Playback is raised to a level you can hear. The stored file and " +
            "its hash do not change, and the trace below keeps the measured level."
          }
        >
          Turned up {gainDb.toFixed(0)} dB to be audible
        </p>
      )}
    </div>
  )
}

/**
 * What the stored file is. The clip filter cutoff is half the stored rate,
 * so one number says both. A clip keeps the rate it was recorded at, so this
 * is read from the file and never assumed.
 */
function storedRate(rateHz: number | null): string {
  if (rateHz === null) return "Not read"
  return `${khz(rateHz)}, filtered at ${khz(rateHz / 2)}`
}

function khz(hz: number): string {
  return hz >= 1000 ? `${Number((hz / 1000).toFixed(2))} kHz` : `${hz} Hz`
}

function clock(seconds: number): string {
  const whole = Math.max(0, Math.floor(seconds))
  const minutes = Math.floor(whole / 60)
  return `${minutes}:${String(whole - minutes * 60).padStart(2, "0")}`
}

/**
 * What is on disk, and why it is safe to keep. The owner should be able to
 * explain this to anybody who asks what the recording contains.
 */
export function ClipFacts({ eventId, media }: { eventId: number; media: EventMedia }) {
  return (
    <div className="flex flex-col gap-4">
      <dl className="grid gap-x-6 text-sm sm:grid-cols-2">
        <Row label="Stored length" value={formatDuration(media.duration_ms)} />
        <Row label="Size" value={`${(media.bytes / 1024).toFixed(0)} kB`} />
        <Row label="Stored rate" value={storedRate(media.rate_hz)} />
        <Row
          label="First sample"
          value={media.start_ms === null ? "Not recorded" : formatClockSeconds(media.start_ms)}
        />
        <Row label="Complete" value={completeness(media)} />
        <Row
          label="Loudest sample"
          value={media.peak_dbfs === null ? "Not read" : `${media.peak_dbfs.toFixed(1)} dBFS`}
        />
        <Row label="Distortion" value={distortion(media)} />
      </dl>

      <div>
        <p className="text-xs text-muted-foreground">SHA-256 of the stored file</p>
        <p className="mt-0.5 font-mono text-xs break-all">{media.sha256}</p>
      </div>

      {/* A purged clip has no file to send, so the button goes rather than
          failing when it is pressed. */}
      {media.purged_ms === null && (
        <Button asChild variant="outline" size="sm" className="h-8 w-fit gap-1.5 print:hidden">
          <a href={url.audioOriginal(eventId)} download>
            <DownloadIcon className="size-4" aria-hidden />
            Download the stored clip
          </a>
        </Button>
      )}

      {media.purged_ms === null ? (
        <p className="max-w-prose text-xs text-muted-foreground">
          The stored clip is low-pass filtered, so it cannot hold anything above the cutoff above.
          That is the property that makes it safe to keep. Playback raises the rate to at least 8
          kHz, because browsers refuse to play a clip this slow. The rate change adds nothing above
          the cutoff and changes neither the file nor the hash above. The download is the bytes on
          disk, and the server checks the hash again before it sends them.
        </p>
      ) : (
        <p className="max-w-prose text-xs text-muted-foreground">
          The file is gone, so there is nothing to play and nothing to download. The figures above
          are what this event recorded, kept as the record that a clip existed: the size it took and
          the SHA-256 of the bytes that were on disk.
        </p>
      )}

      {media.clipped > 0 && (
        <p className="max-w-prose text-xs text-muted-foreground">
          This clip is distorted. {media.clipped.toLocaleString()} of its samples reached the
          microphone's limit, which is about 107 dB SPL, so the loudest moments are flattened rather
          than recorded. The measured level is still right, because it comes from the meter and not
          from the clip. The EM-01's gain is fixed in hardware, so there is nothing to turn down.
        </p>
      )}
    </div>
  )
}

/** How much of a clip is flattened against the microphone's limit. */
function distortion(media: EventMedia): string {
  if (media.clipped === 0) return "None"
  return `${media.clipped.toLocaleString()} samples at the limit`
}

/**
 * Whether the clip holds the whole run-up it asked for. `truncated` says a
 * clip is short; `missing_head_ms` says by how much, so a 62 ms shortfall and
 * a three second one do not read the same.
 */
function completeness(media: EventMedia): string {
  if (media.missing_head_ms === null) {
    return media.truncated ? "No, it starts late by an unrecorded amount" : "Yes"
  }
  if (media.missing_head_ms < 100) return "Yes"
  return `No, it starts ${(media.missing_head_ms / 1000).toFixed(1)} s late`
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border py-1.5">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="text-right">{value}</dd>
    </div>
  )
}

/**
 * A recording the owner deleted. It reads as a decision, not as a fault:
 * the file is gone because somebody chose that, and everything measured is
 * still on the screen below.
 */
export function ClipPurged({ media }: { media: EventMedia }) {
  if (media.purged_ms === null) return null
  return (
    <p className="max-w-prose text-sm text-muted-foreground">
      The recording was deleted on {formatDay(media.purged_ms)}
      {media.purged_by ? ` by ${media.purged_by}` : ""}. The measurements below are unchanged, and
      this event still carries the size and the SHA-256 of the recording it had.
    </p>
  )
}

/** A clip that is not there, said plainly instead of drawn as a broken player. */
export function ClipMissing({ reason }: { reason: "missing" | "none" | "failed" | "purged" }) {
  if (reason === "purged") {
    // Reached only when the purge date was not read. ClipPurged says it
    // with the date and the person whenever the detail carries them.
    return (
      <p className="text-sm text-muted-foreground">
        The recording was deleted. The measurements below are unchanged.
      </p>
    )
  }
  if (reason === "none") {
    return (
      <p className="text-sm text-muted-foreground">
        No clip was recorded for this event. The level trace below is the whole record of it.
      </p>
    )
  }
  if (reason === "failed") {
    return (
      <p className="text-sm" role="alert">
        The clip would not play. Reload the page, and if it still will not play, look at System for
        a recent media error.
      </p>
    )
  }
  return (
    <p className="text-sm" role="alert">
      The clip file is gone. The event and its measurements are still here, but the audio is no
      longer on disk. The collector recorded the loss, so System shows when it was noticed.
    </p>
  )
}
