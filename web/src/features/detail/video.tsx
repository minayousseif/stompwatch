import { useState } from "react"

import { url, type EventMedia } from "@/lib/api"
import { formatClockSeconds, formatDay, formatDuration } from "@/lib/time"

/**
 * The camera's clip of the event. The file is served as it was stored, and
 * the browser's own controls play it: seeking works because the server
 * answers range requests.
 */
export function VideoClip({ eventId, media }: { eventId: number; media: EventMedia }) {
  const [failed, setFailed] = useState(false)

  if (media.purged_ms !== null) {
    return (
      <div className="flex flex-col gap-3">
        <p className="max-w-prose text-sm text-muted-foreground">
          The video was deleted on {formatDay(media.purged_ms)}
          {media.purged_by ? ` by ${media.purged_by}` : ""}. The measurements on this page are
          unchanged, and the event still carries the size and the SHA-256 of the clip it had.
        </p>
        <dl className="grid gap-x-6 text-sm sm:grid-cols-2">
          <Row label="Stored length" value={formatDuration(media.duration_ms)} />
          <Row label="Size it took" value={`${(media.bytes / 1024 / 1024).toFixed(1)} MB`} />
        </dl>
        <div>
          <p className="text-xs text-muted-foreground">SHA-256 of the file that was stored</p>
          <p className="mt-0.5 font-mono text-xs break-all">{media.sha256}</p>
        </div>
      </div>
    )
  }

  if (media.missing) {
    return (
      <p className="text-sm" role="alert">
        The video file is gone. The event and its measurements are still here, but the clip is no
        longer on disk. The collector recorded the loss, so System shows when it was noticed.
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      {/* A video that suddenly speaks is a surprise worth removing, so a clip
          with the camera's audio in it says so above the player. The element
          is not muted either way: a clip that has sound is meant to be heard,
          and one that has none is silent on its own.
          The contrast is the useful part: an audio clip of the same event
          cannot hold speech, and this one can.
          It is hidden on paper. The warning is for whoever is about to press
          play, and a sheet has no player. The fact itself stays on the sheet:
          the Sound row below says the clip carries the camera's microphone. */}
      {media.camera_audio && (
        <p className="max-w-prose text-sm print:hidden">
          <span className="text-foreground">This clip has sound.</span> It is the camera's own
          microphone, unfiltered, so speech in it is intelligible. The measuring microphone's clips
          are filtered so that speech in them is not.
        </p>
      )}
      {failed ? (
        <p className="text-sm" role="alert">
          The video would not play. Reload the page, and if it still will not play, look at System
          for a recent media error.
        </p>
      ) : (
        <video
          controls
          preload="metadata"
          playsInline
          className="max-h-[70vh] w-full rounded-md bg-black print:hidden"
          src={url.video(eventId)}
          onError={() => setFailed(true)}
        />
      )}
      <dl className="grid gap-x-6 text-sm sm:grid-cols-2">
        <Row label="Stored length" value={formatDuration(media.duration_ms)} />
        <Row label="Size" value={`${(media.bytes / 1024 / 1024).toFixed(1)} MB`} />
        <Row
          label="Starts"
          value={media.start_ms === null ? "Not recorded" : formatClockSeconds(media.start_ms)}
        />
        <Row
          label="Complete"
          value={media.truncated ? "No, part of the window has no video" : "Yes"}
        />
        <Row label="Sound" value={media.camera_audio ? "Yes, the camera's own" : "None"} />
      </dl>
      <div>
        <p className="text-xs text-muted-foreground">SHA-256 of the stored file</p>
        <p className="mt-0.5 font-mono text-xs break-all">{media.sha256}</p>
      </div>
      <p className="max-w-prose text-xs text-muted-foreground">
        The clip is the camera's own stream, the picture copied and never re-encoded, cut on the
        segment edges either side of the run-up and the run-out, so it starts a little before the
        audio clip and ends a little after.
        {media.camera_audio
          ? ""
          : " It holds no sound: the camera's microphone was not recorded when this clip was cut."}
      </p>
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border py-1.5">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="text-right">{value}</dd>
    </div>
  )
}
