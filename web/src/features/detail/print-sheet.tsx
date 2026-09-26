// The part of the event page that exists only on paper.
//
// A printed event leaves the building. Nobody reading it can ask what the
// clock was set to, what the microphone was, or what the recording can and
// cannot hold, so the sheet says all of that itself. Every line here is read
// from the API. Nothing is claimed that the instrument did not measure, and
// there is no signature line: this is a measurement record, not an affidavit.

import { useEffect, useState, type ReactNode } from "react"
import { flushSync } from "react-dom"

import { accuracySentence } from "@/lib/accuracy"
import type { EventCapture, EventDetail, EventMedia } from "@/lib/api"
import { formatClockSeconds, formatStamp } from "@/lib/time"

/**
 * When the sheet went to the printer. A page can sit open all night, so the
 * time is taken again as the print starts rather than when the tab loaded.
 * `flushSync` puts the new time in the document before the browser lays the
 * pages out.
 */
export function usePrintTime(): number {
  const [when, setWhen] = useState(() => Date.now())

  useEffect(() => {
    const mark = () => flushSync(() => setWhen(Date.now()))
    window.addEventListener("beforeprint", mark)
    return () => window.removeEventListener("beforeprint", mark)
  }, [])

  return when
}

/** A clip's own rate, and the cutoff it was filtered at, which is half it. */
function rateWords(rateHz: number): { rate: string; cutoff: string } {
  const khz = (hz: number) => (hz >= 1000 ? `${Number((hz / 1000).toFixed(2))} kHz` : `${hz} Hz`)
  return { rate: khz(rateHz), cutoff: khz(rateHz / 2) }
}

/** What the document is, when it was measured, and on what. */
export function PrintHeader({ event, printedMs }: { event: EventDetail; printedMs: number }) {
  return (
    <header className="hidden print:block print:break-inside-avoid">
      <div className="flex items-baseline justify-between gap-4 border-b-2 border-foreground pb-1">
        <h1 className="text-base font-medium">Impact noise event record</h1>
        <p className="text-sm">Event {event.id}</p>
      </div>
      <dl className="mt-2 grid grid-cols-[9rem_1fr] gap-x-4 text-xs">
        <Line label="Measured">
          {formatStamp(event.started_ms)}, ending {formatClockSeconds(event.ended_ms)}
        </Line>
        <Line label="Instrument">{instrument(event.capture)}</Line>
        <Line label="Accuracy">{accuracy(event.capture)}</Line>
        {/* Said at the top, not buried: a reader deciding what this sheet is
            worth needs to know the owner has already accounted for it. */}
        {event.muted && (
          <Line label="Mute window">
            A mute window covers this event, so it is left out of the summary counts and the CSV
            export. The measurement and the clip are unchanged.
          </Line>
        )}
        <Line label="Printed">{formatStamp(printedMs)}</Line>
      </dl>
    </header>
  )
}

/**
 * The microphone and the numbers this event's levels were worked out from.
 *
 * Every figure comes from the event's own capture block, which is the
 * settings the collector recorded as being in force when this event was
 * measured. It is never read from the collector's current state: this sheet
 * may be printed months later, and a reprint that claimed today's
 * calibration would be a document stating something nobody measured
 * (SPEC.md section 15 decision 23).
 */
function instrument(capture: EventCapture | null): string {
  const head = "A calibrated measuring microphone, recording A-weighted sound level."
  if (!capture) {
    return `${head} The sensitivity and the calibration file it was measured under were not recorded.`
  }
  const sensitivity = Number.isFinite(capture.sensitivity_dbfs)
    ? `Sensitivity at capture: ${capture.sensitivity_dbfs.toFixed(1)} dBFS.`
    : "Sensitivity at capture: not recorded."
  const calibration = capture.calibration
    ? `Calibration file: ${capture.calibration}, correcting ${signedDb(capture.calibration_offset_db)}.`
    : "Calibration file: none."
  return `${head} ${sensitivity} ${calibration}`
}

/**
 * How well the levels on this sheet are known. The sentence is built from
 * the event's own settings, so an event measured against a reference and one
 * measured on the datasheet figure do not read the same.
 */
function accuracy(capture: EventCapture | null): string {
  if (!capture) return NOT_RECORDED
  return accuracySentence(capture)
}

/**
 * What the sheet says when the history does not reach back to this event.
 * That is the truth for every event recorded before the collector began
 * keeping the history, and it must not be papered over: a sheet that filled
 * the gap from today's settings would be the exact fault this wording
 * exists to prevent.
 */
const NOT_RECORDED =
  "The settings in force at capture were not recorded. This event was measured before the " +
  "collector began keeping that history, so how accurate these levels are cannot be stated " +
  "here. The settings in force today are not those settings, and are deliberately left off " +
  "this sheet."

/** A correction, with its sign, so plus 0.5 dB cannot read as minus. */
function signedDb(db: number): string {
  if (!Number.isFinite(db)) return "an unrecorded amount"
  const rounded = Number(db.toFixed(2))
  return `${rounded > 0 ? "+" : ""}${rounded.toFixed(2)} dB`
}

/**
 * What the clip is and what it is not, said from this clip's own numbers. An
 * old clip and a new one were filtered at different cutoffs, so the sentence
 * is built rather than fixed.
 */
export function PrintFooter({ eventId, media }: { eventId: number; media: EventMedia | null }) {
  const sentences: string[] = []

  if (!media || media.missing) {
    sentences.push(
      media
        ? "The clip file for this event is no longer on disk. The measured level is the whole record of it."
        : "No clip was recorded for this event. The measured level is the whole record of it.",
    )
  } else if (media.rate_hz === null) {
    sentences.push(
      "The stored clip is a mono WAV. Its rate could not be read, so the cutoff it was filtered at is not on this sheet.",
    )
  } else {
    const { rate, cutoff } = rateWords(media.rate_hz)
    sentences.push(
      `The stored clip is a ${rate} mono WAV. The collector low-pass filtered it at ${cutoff} in the capture path, before it reached the disk, so the file cannot hold anything above ${cutoff}.`,
    )
  }

  sentences.push(
    "The levels on this sheet are measured by the meter from the full-rate input. They are not read from the clip.",
  )

  if (media && !media.missing) {
    const gain =
      media.playback_gain_db === null
        ? "Playback in the browser is raised and shaped so a small speaker can reproduce it."
        : `Playback in the browser is raised by ${media.playback_gain_db.toFixed(1)} dB and shaped so a small speaker can reproduce it.`
    sentences.push(
      `${gain} The stored file and the SHA-256 above are what is on disk, and neither changes.`,
    )
  }

  return (
    <footer className="hidden border-t border-foreground pt-2 print:block print:break-inside-avoid">
      {/* The number is here as well as in the header, so that a sheet that
          runs to two pages is named on both of them. */}
      <div className="flex items-baseline justify-between gap-4">
        <h2 className="text-xs font-medium">What this recording is</h2>
        <p className="text-xs text-muted-foreground">Event {eventId}</p>
      </div>
      <div className="mt-1 flex flex-col gap-1 text-xs">
        {sentences.map((sentence) => (
          <p key={sentence}>{sentence}</p>
        ))}
      </div>
    </footer>
  )
}

function Line({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd>{children}</dd>
    </>
  )
}
