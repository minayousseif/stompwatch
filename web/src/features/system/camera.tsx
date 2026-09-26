import { useState } from "react"

import { EmptyNote, ErrorNote, Fact, Group, LoadingNote, Panel } from "@/components/state"
import { Button } from "@/components/ui/button"
import { SettingsForm } from "@/features/system/settings-form"
import { formatBytes } from "@/features/system/status"
import { api, errorMessage, type CameraTestResult, type SystemStatus } from "@/lib/api"
import { formatAgo, formatDayAndSeconds, formatSpan } from "@/lib/time"
import { cn } from "@/lib/utils"

/**
 * The camera settings the dashboard may change. They are named here only to
 * put them beside the camera rather than under Settings; the fields
 * themselves are built from what the server sends. The host and port are not
 * here: the login is sent to them, so only the config file may set them.
 */
export const CAMERA_SETTING_KEYS = [
  "camera_rtsp_path",
  "camera_rtsp_path_main",
  "video_ring_minutes",
  "video_segment_seconds",
  "camera_audio",
]

/** Where the credentials file goes, and what has to be in it. */
const CREDENTIALS_FILE = "/etc/stompwatch/camera.env"

/**
 * Everything about the camera in one place: what it is doing, how it is set
 * up, whether a login is loaded, and a test that says what the camera
 * answers on.
 *
 * The password is not here and cannot be. It is read from the environment
 * or from a mode 0600 file on the box, and no field on this screen can
 * show it, set it, or choose which file the collector reads.
 */
export function CameraSection({
  status,
  loading,
  error,
  onRetry,
}: {
  status: SystemStatus | null
  loading: boolean
  error: unknown
  onRetry: () => void
}) {
  // What the camera test found and is offering for the form. The owner
  // still presses save: a test must not write a setting by itself.
  const [offered, setOffered] = useState<Record<string, string> | null>(null)

  return (
    <>
      <Panel title="Camera">
        {error ? (
          <ErrorNote error={error} onRetry={onRetry} />
        ) : status ? (
          <CameraFacts status={status} />
        ) : loading ? (
          <LoadingNote what="the camera" />
        ) : null}
      </Panel>

      <Panel title="Camera settings">
        <SettingsForm
          only={CAMERA_SETTING_KEYS}
          offered={offered}
          onSaved={onRetry}
          restartTitle="These take effect the next time the collector starts"
          restartDetail={
            "None of these is applied while the collector runs: the segment ring and the clip " +
            "writer are built once, at start. The value is stored as soon as you save, and the " +
            "collector picks it up on its next restart."
          }
        />
        <StreamPathNote />
      </Panel>

      <Panel title="Test the camera">
        <CameraTest configured={status?.camera.config ?? null} onOffer={setOffered} />
      </Panel>

      {offered && (
        <p className="px-1 text-xs text-muted-foreground">
          The test filled the settings above in. Nothing is saved until you press Save changes.
        </p>
      )}
    </>
  )
}

/** What the camera is doing, how it is reached, and what it is signed in as. */
function CameraFacts({ status }: { status: SystemStatus }) {
  const camera = status.camera
  const config = camera.config
  const uptime = formatSpan(status.now_ms - status.started_ms)
  const discovered = config.sub_path_source === "discovered"

  return (
    <div className="flex flex-col gap-6">
      <p className="flex items-center gap-2.5 text-base">
        <span
          className={cn(
            "size-2.5 shrink-0 rounded-full",
            // Gray, not invisible: video being off is a state, not a gap.
            camera.enabled
              ? camera.connected
                ? "bg-verified"
                : "bg-rejected"
              : "bg-muted-foreground",
          )}
          aria-hidden
        />
        {!camera.enabled
          ? "No camera is set up. Video is off."
          : camera.connected
            ? "Video is arriving from the camera."
            : "The camera is set up, but no video is arriving."}
      </p>

      <div className="grid gap-x-8 gap-y-6 lg:grid-cols-3">
        <Group title="Recording">
          {camera.enabled ? (
            <>
              <Fact
                label="Video"
                value={camera.connected ? "Arriving" : "Not arriving"}
                note={
                  camera.connected
                    ? `since ${formatDayAndSeconds(camera.connected_since_ms)}`
                    : camera.last_segment_ms > 0
                      ? `last segment ${formatAgo(camera.last_segment_ms, status.now_ms)}`
                      : "no segment has arrived since the collector started"
                }
              />
              <Fact
                label="Video in all"
                value={formatSpan(camera.uptime_ms)}
                note={`of the ${uptime} the collector has been up`}
              />
              <Fact
                label="Disconnects"
                value={camera.disconnects === 0 ? "None" : String(camera.disconnects)}
                note={camera.disconnects === 0 ? undefined : "each one is in the health log below"}
                quiet={camera.disconnects === 0}
              />
              <Fact
                label="Ring"
                value={`${camera.ring.segments} segments`}
                note={formatBytes(camera.ring.bytes)}
              />
              <Fact
                label="Video clips"
                value={String(camera.clips.count)}
                note={formatBytes(camera.clips.bytes)}
              />
              <Fact
                label="Camera audio"
                value={config.audio ? "Recorded" : "Not recorded"}
                note={
                  config.audio
                    ? "unfiltered, so video recorded from now on holds speech in clear"
                    : "every ffmpeg command drops it"
                }
                quiet={!config.audio}
              />
            </>
          ) : (
            <Fact
              label="Video"
              value="Off"
              note="set camera_host in the config file and restart to switch it on"
              quiet
            />
          )}
        </Group>

        <Group title="Stream">
          <Fact
            label="Address"
            value={config.host === "" ? "Not set" : `${config.host}:${config.port}`}
            note="set in the config file, as camera_host and camera_port"
            quiet={config.host === ""}
          />
          <Fact
            label="Sub-stream"
            value={config.sub_path === "" ? "None yet" : config.sub_path}
            note={
              config.sub_path_source === "none"
                ? "nothing is set, so the collector tries the known cameras in turn"
                : discovered
                  ? "found by trying the known cameras, and tried again on every reconnect"
                  : "from the settings below"
            }
            quiet={config.sub_path === ""}
          />
          <Fact
            label="Main stream"
            value={config.main_path === "" ? "None" : config.main_path}
            quiet={config.main_path === ""}
          />
          <Fact label="Ring holds" value={`${config.ring_minutes} min`} />
          <Fact label="Segment" value={`${config.segment_seconds} s`} />
          <Fact
            label="Camera microphone"
            value={audioTrackValue(config.audio_track)}
            note={audioTrackNote(config.audio_track)}
            quiet={!config.audio_track.present}
          />
        </Group>

        <Group title="What it needs">
          <Fact
            label="Login"
            value={config.credentials.loaded ? config.credentials.user : "None"}
            note={config.credentials.loaded ? `from the ${config.credentials.source}` : undefined}
            quiet={!config.credentials.loaded}
          />
          <Fact
            label="ffmpeg"
            value={config.ffmpeg === "" ? "Not installed" : "Installed"}
            note={config.ffmpeg === "" ? undefined : config.ffmpeg}
            quiet={config.ffmpeg === ""}
          />
        </Group>
      </div>

      <LoginNote config={config} />
      <CameraAudioNote config={config} />
      <FFmpegNote version={config.ffmpeg} />
    </div>
  )
}

/**
 * What the camera sends, in three words for a fact line. "Not checked" is
 * not "no audio": the collector only asks while camera_audio is on, and a
 * reading nobody made must not read as a silent camera.
 */
function audioTrackValue(track: SystemStatus["camera"]["config"]["audio_track"]): string {
  if (!track.known) return "Not checked"
  if (!track.present) return "None"
  return track.codec === "" ? "Sends audio" : track.codec
}

function audioTrackNote(
  track: SystemStatus["camera"]["config"]["audio_track"],
): string | undefined {
  if (!track.known) return "switch the camera audio on, or use the test below, to find out"
  if (!track.present)
    return "this camera sends no audio track, so recording it would record nothing"
  return track.rate_hz > 0
    ? `${track.rate_hz} Hz, as the camera sends it`
    : "as the camera sends it"
}

/**
 * Which mode the box is in, in prose. It is here and not only on the field
 * because the owner has to be able to read it without opening a form, and a
 * clip that suddenly holds speech is not something to discover by accident
 * (SPEC.md section 15 decision 22).
 */
function CameraAudioNote({ config }: { config: SystemStatus["camera"]["config"] }) {
  if (!config.audio) {
    return (
      <p className="max-w-prose text-sm text-muted-foreground">
        The camera's own microphone is not recorded. Every ffmpeg command line carries{" "}
        <span className="text-foreground">-an</span>, so no camera audio reaches the disk. Switching{" "}
        <span className="text-foreground">Record the camera's own audio</span> on below changes that
        for video recorded after the next restart, and the setting says what it means.
      </p>
    )
  }
  return (
    <div className="max-w-prose text-sm">
      <p>
        <span className="text-foreground">The camera records its own audio, unfiltered.</span> Video
        recorded while this is on holds speech in clear: the camera's microphone is full bandwidth,
        so its track is a recording of the room rather than a measurement.
      </p>
      <p className="mt-1 text-muted-foreground">
        The measuring microphone's clips are not affected. They are low-pass filtered as they are
        recorded, so speech in them is not intelligible, and nothing here can change that. Clips
        already on disk keep what they hold, and the event screen says which ones have sound.
        {config.audio_track.known && !config.audio_track.present
          ? " This camera sends no audio track, so nothing is being recorded yet."
          : ""}
      </p>
    </div>
  )
}

/**
 * Whether a login is loaded and where it came from. It never offers to set
 * one: the password belongs in a file on the box that only the service user
 * can read, and a web form must not choose which file that is.
 */
function LoginNote({ config }: { config: SystemStatus["camera"]["config"] }) {
  const creds = config.credentials
  if (creds.loaded) {
    return (
      <p className="max-w-prose text-sm text-muted-foreground">
        A login is loaded from the {creds.source === "file" ? "credentials file" : "environment"},
        as <span className="text-foreground">{creds.user}</span>. The password is not on this screen
        and cannot be put here. To change it, edit the file or the service environment on the box
        and restart the collector.
      </p>
    )
  }
  return (
    <div className="max-w-prose text-sm">
      <p>No login is loaded, so every stream will answer 401.</p>
      <p className="mt-1 text-muted-foreground">To load one, on the box:</p>
      <ol className="mt-1 list-decimal pl-5 text-muted-foreground">
        <li>
          Write <span className="text-foreground">{CREDENTIALS_FILE}</span> with two lines:{" "}
          <span className="text-foreground">CAMERA_USER=</span> and{" "}
          <span className="text-foreground">CAMERA_PASS=</span>.
        </li>
        <li>
          Run <span className="text-foreground">chmod 0600 {CREDENTIALS_FILE}</span>, so only the
          service user can read it.
        </li>
        <li>Restart the collector.</li>
      </ol>
    </div>
  )
}

/** Whether ffmpeg is there, and how to put it there. */
function FFmpegNote({ version }: { version: string }) {
  if (version !== "") return null
  return (
    <p className="max-w-prose text-sm">
      ffmpeg is not installed, so no video can be recorded and the camera cannot be tested. Install
      it on the box with <span className="text-muted-foreground">apt install ffmpeg</span>, then
      restart the collector.
    </p>
  )
}

/**
 * What goes in the stream path fields. The owner types these, so the shape
 * is spelled out here rather than left to the camera test: a path may carry a
 * query, and a camera that is on no list is set by hand.
 */
function StreamPathNote() {
  return (
    <div className="max-w-prose text-sm text-muted-foreground">
      <p>
        The sub-stream path is what comes after the port, with no leading slash. It may carry a
        query: an Amcrest or Dahua camera wants{" "}
        <span className="text-foreground break-all">cam/realmonitor?channel=1&amp;subtype=1</span>,
        a Reolink wants <span className="text-foreground">Preview_01_sub</span>.
      </p>
      <p className="mt-1">
        Leave it empty and the collector tries the known cameras in turn, one on each reconnect, so
        a sweep of all seven takes about a minute. Setting it stops that. The main-stream path can
        stay empty for a camera on that list, because it comes from the same row; a path that is on
        no list has no main path, so set it too if the main stream is wanted. The test below tries
        the same list and prints what answered.
      </p>
    </div>
  )
}

/**
 * The dashboard's form of `stompwatch test-camera`. It connects out to the
 * camera and says what answers. It changes nothing: what it finds is
 * offered to the form above, and the owner presses save.
 */
function CameraTest({
  configured,
  onOffer,
}: {
  configured: SystemStatus["camera"]["config"] | null
  onOffer: (values: Record<string, string>) => void
}) {
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<CameraTestResult | null>(null)
  const [error, setError] = useState<unknown>(null)

  const run = () => {
    setRunning(true)
    setError(null)
    api
      .testCamera()
      .then((answer) => {
        setResult(answer)
        setError(null)
      })
      .catch(setError)
      .finally(() => setRunning(false))
  }

  // What the test found that the settings do not already say. The
  // configured sub path is empty when the collector is discovering one, and
  // then anything the test finds is worth writing down.
  const newSub =
    result && result.sub_path !== "" && result.sub_path !== configured?.sub_path
      ? result.sub_path
      : ""
  const newMain =
    result && result.main_path !== "" && result.main_path !== configured?.main_path
      ? result.main_path
      : ""

  return (
    <div className="flex flex-col gap-4">
      <p className="max-w-prose text-sm text-muted-foreground">
        With the sub-stream path empty, this tries each known camera in turn: Reolink, Amcrest and
        Dahua, Hikvision, TP-Link Tapo, Foscam, and Ubiquiti. With a path set, it tries that one. It
        takes up to half a minute and changes no setting.
      </p>

      <div className="flex flex-wrap items-center gap-3">
        <Button size="sm" className="h-8" disabled={running} onClick={run}>
          {running ? "Testing" : "Test the camera"}
        </Button>
        {running && (
          <span className="text-xs text-muted-foreground" aria-live="polite">
            Trying each path. One test runs at a time.
          </span>
        )}
      </div>

      {error !== null && (
        <div className="border border-border bg-card p-3 text-sm" role="alert">
          <p>The test did not run. The collector said:</p>
          <p className="mt-1 text-foreground">{errorMessage(error)}</p>
        </div>
      )}

      {result && !running && (
        <div className="flex flex-col gap-4">
          <ul className="flex flex-col text-sm">
            {result.tried.map((one) => (
              <li
                key={one.path}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 border-b border-border py-1.5"
              >
                <span className="flex items-center gap-2">
                  <span
                    className={cn(
                      "size-2 shrink-0 rounded-full",
                      one.ok ? "bg-verified" : "bg-rejected",
                    )}
                    aria-hidden
                  />
                  <span className={cn(!one.ok && "text-muted-foreground")}>{one.path}</span>
                </span>
                {/* ffmpeg's reason is a URL with no spaces in it, so it is
                    broken anywhere rather than pushing the page sideways on
                    a phone. */}
                <span className="min-w-0 flex-1 break-all text-xs text-muted-foreground">
                  {one.ok ? one.detail : `failed: ${one.detail}`}
                </span>
              </li>
            ))}
          </ul>

          {result.tried.length === 0 && (
            <EmptyNote
              headline="Nothing was tried."
              detail="The collector has no path to try. Set the sub-stream path above, or leave it empty to try the known cameras in turn."
            />
          )}

          <dl className="grid gap-x-8 text-sm sm:grid-cols-2">
            <Fact
              label="Sub-stream"
              value={result.sub_path === "" ? "None worked" : result.sub_path}
              quiet={result.sub_path === ""}
            />
            <Fact
              label="Main stream"
              value={result.main_path === "" ? "Did not answer" : result.main_path}
              note={result.main_path === "" ? "the ring does not need it" : undefined}
              quiet={result.main_path === ""}
            />
            <Fact
              label="Camera microphone"
              value={
                result.audio.known
                  ? result.audio.present
                    ? result.audio.codec || "Sends audio"
                    : "None"
                  : "Not known"
              }
              note={
                !result.audio.known
                  ? "no path answered, so nothing was measured"
                  : result.audio.present
                    ? `${result.audio.rate_hz} Hz. Recording it is off unless the camera audio setting is on.`
                    : "this camera sends no audio track, so the camera audio setting would record nothing"
              }
              quiet={!result.audio.present}
            />
            <Fact
              label="Camera clock"
              value={
                result.clock_readable
                  ? `${(result.clock_drift_ms / 1000).toFixed(1)} s`
                  : "Not read"
              }
              note={
                result.clock_readable
                  ? Math.abs(result.clock_drift_ms) > 2000
                    ? "more than 2 s from this box; a clip could be stamped with the wrong moment"
                    : "from this box's clock; the limit is 2 s"
                  : "this camera does not report its clock. Check it by hand."
              }
              quiet={!result.clock_readable}
            />
          </dl>

          {result.sub_path === "" && (
            <p className="max-w-prose text-sm">
              No path answered. Check the address and port above, that the login is loaded, and that
              RTSP is switched on in the camera's own settings.
            </p>
          )}

          {(newSub || newMain) && (
            <div className="flex flex-col gap-2 border-t border-border pt-4">
              <p className="max-w-prose text-sm">
                The test found a path the settings do not name. Putting it in the settings stops the
                collector rediscovering it on every reconnect.
              </p>
              <div>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8"
                  onClick={() => {
                    const values: Record<string, string> = {}
                    if (newSub) values.camera_rtsp_path = newSub
                    if (newMain) values.camera_rtsp_path_main = newMain
                    onOffer(values)
                  }}
                >
                  Put {newSub || newMain} in the settings
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                This fills the field in. Nothing is saved until you press Save changes.
              </p>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
