import { useState } from "react"

import { EmptyNote, ErrorNote, Fact, Group, LoadingNote } from "@/components/state"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { PurgeConfirm } from "@/features/purge/purge-confirm"
import { formatBytes } from "@/features/system/status"
import { useResource } from "@/hooks/use-resource"
import { api } from "@/lib/api"

/** The ages the buttons offer, in days. */
const AGES = [7, 30, 90, 365]

/** How many events one purge may carry. The server refuses more. */
const MAX_EVENTS = 500

/** The instant an age in days cuts at. */
function cutAt(days: number, now = Date.now()): number {
  return now - days * 24 * 60 * 60 * 1000
}

/**
 * What the recordings take, and a way to delete the old ones. The
 * measurements are never touched, and the copy has to keep saying so: the
 * owner is deleting evidence files, not the evidence.
 */
export function PurgeSection() {
  const [days, setDays] = useState(30)
  const [confirming, setConfirming] = useState(false)
  const [tick, setTick] = useState(0)

  const cut = cutAt(days)
  const space = useResource((signal) => api.mediaUsage(cut, signal), [days, tick])
  // The list a purge would act on, read before anything is deleted. The
  // purge then names exactly these events, so nothing is swept up that the
  // count did not cover.
  const list = useResource((signal) => api.purgeable(cut, MAX_EVENTS, signal), [days, tick])

  const usage = space.data
  const matching = usage?.purgeable
  const ids = list.data?.event_ids ?? []
  // The list is capped at MAX_EVENTS, so when there are more the figures
  // for the whole age would overstate what one run deletes. Read the
  // figures for exactly the events the run will name.
  const capped = list.data?.more ?? false
  const exact = useResource(
    (signal) => (capped && ids.length > 0 ? api.mediaUsageOf(ids, signal) : Promise.resolve(null)),
    [ids.join(","), capped],
  )
  const target = capped ? exact.data?.purgeable : matching
  const reload = () => setTick((n) => n + 1)

  return (
    <div className="flex flex-col gap-6">
      {space.error ? (
        <ErrorNote error={space.error} onRetry={reload} />
      ) : usage ? (
        <>
          <div className="grid gap-6 sm:grid-cols-2">
            <Group title="Held on disk">
              <Fact
                label="Audio"
                value={<span className="tabular-nums">{formatBytes(usage.audio.bytes)}</span>}
                note={`${usage.audio.files} across ${usage.audio.events} events`}
                quiet={usage.audio.files === 0}
              />
              <Fact
                label="Video"
                value={<span className="tabular-nums">{formatBytes(usage.video.bytes)}</span>}
                note={`${usage.video.files} across ${usage.video.events} events`}
                quiet={usage.video.files === 0}
              />
              <Fact
                label="Already deleted"
                value={<span className="tabular-nums">{formatBytes(usage.purged.bytes)}</span>}
                note={`${usage.purged.files} across ${usage.purged.events} events`}
                quiet={usage.purged.files === 0}
              />
            </Group>

            <Group title="Older than the age below">
              <Fact
                label="Events"
                value={<span className="tabular-nums">{matching?.events ?? 0}</span>}
                quiet={!matching || matching.events === 0}
              />
              <Fact
                label="Recordings"
                value={<span className="tabular-nums">{matching?.files ?? 0}</span>}
                quiet={!matching || matching.files === 0}
              />
              <Fact
                label="Space they take"
                value={<span className="tabular-nums">{formatBytes(matching?.bytes ?? 0)}</span>}
                quiet={!matching || matching.bytes === 0}
              />
            </Group>
          </div>

          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-end gap-3">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="purge-days" className="text-xs text-muted-foreground">
                  Recordings older than
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="purge-days"
                    type="number"
                    min={1}
                    max={3650}
                    value={days}
                    onChange={(e) => setDays(clampDays(e.target.value, days))}
                    className="h-8 w-24 tabular-nums"
                  />
                  <span className="text-sm text-muted-foreground">days</span>
                </div>
              </div>
              <div className="flex flex-wrap gap-1.5">
                {AGES.map((one) => (
                  <Button
                    key={one}
                    variant="outline"
                    size="sm"
                    className="h-8 text-xs tabular-nums"
                    aria-pressed={days === one}
                    onClick={() => setDays(one)}
                  >
                    {one} days
                  </Button>
                ))}
              </div>
              <Button
                variant="outline"
                size="sm"
                className="h-8"
                disabled={!matching || matching.files === 0 || !list.data}
                onClick={() => setConfirming(true)}
              >
                Delete these recordings
              </Button>
            </div>

            {matching && matching.files === 0 ? (
              <EmptyNote
                headline="Nothing that old still has a recording."
                detail="Widen the age, or leave it: this is the state you want the disk in."
              />
            ) : (
              <p className="max-w-prose text-xs text-muted-foreground">
                Deleting a recording removes the file and nothing else. The event, its level trace,
                your decision and your note all stay, and each event keeps the size and the SHA-256
                of the recording it had. Every deletion is recorded and shows in the health log
                above.
              </p>
            )}
            {list.data?.more && (
              <p className="max-w-prose text-xs text-muted-foreground">
                More than {MAX_EVENTS} events match. One run deletes the oldest {MAX_EVENTS}; run it
                again for the rest.
              </p>
            )}
            {list.error ? <ErrorNote error={list.error} onRetry={reload} /> : null}
          </div>
        </>
      ) : space.loading ? (
        <LoadingNote what="what the recordings take" />
      ) : null}

      <PurgeConfirm
        open={confirming}
        onOpenChange={setConfirming}
        target={{
          eventIds: ids,
          bytes: target?.bytes ?? 0,
          files: target?.files ?? 0,
          more: capped,
        }}
        onDone={reload}
      />
    </div>
  )
}

/** Keep the field a whole number of days, and keep the old value if it is not. */
function clampDays(text: string, was: number): number {
  const value = Number(text)
  if (!Number.isFinite(value)) return was
  return Math.min(3650, Math.max(1, Math.round(value)))
}
