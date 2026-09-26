import { useState } from "react"

import { Button } from "@/components/ui/button"
import { PurgeConfirm } from "@/features/purge/purge-confirm"
import { formatBytes } from "@/features/system/status"
import { useResource } from "@/hooks/use-resource"
import { api, type PurgeReply } from "@/lib/api"

/**
 * The bar over the event list. It is there only while something is
 * selected, says how many and what their recordings take, and opens the
 * confirmation. It never deletes anything itself.
 */
export function SelectionBar({
  selected,
  onClear,
  onPurged,
}: {
  selected: number[]
  onClear: () => void
  /** Called after a purge, so the list re-reads itself. */
  onPurged: (reply: PurgeReply) => void
}) {
  const [confirming, setConfirming] = useState(false)
  // What the last purge did, held until the panel is closed. Clearing the
  // selection takes this bar off the screen and the panel with it, so the
  // clearing waits: the owner has to be able to read what happened.
  const [done, setDone] = useState<PurgeReply | null>(null)

  // The exact size comes from the server, for exactly these events. A
  // figure the interface guessed would be a figure nobody measured.
  const key = selected.join(",")
  const space = useResource((signal) => api.mediaUsageOf(selected, signal), [key])
  const purgeable = space.data?.purgeable

  return (
    <div className="sticky top-12 z-20 flex flex-wrap items-center gap-x-4 gap-y-2 rounded-md border border-border bg-card px-4 py-2.5">
      <p className="text-sm">
        <span className="tabular-nums">
          {selected.length === 1 ? "1 event" : `${selected.length} events`}
        </span>{" "}
        selected
        {purgeable && (
          <span className="ml-2 text-muted-foreground">
            <span className="tabular-nums">
              {purgeable.files === 1 ? "1 recording" : `${purgeable.files} recordings`}
            </span>
            , <span className="tabular-nums">{formatBytes(purgeable.bytes)}</span>
          </span>
        )}
      </p>
      <div className="ml-auto flex items-center gap-2">
        <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={onClear}>
          Clear the selection
        </Button>
        {/* "Delete the recordings", never "Delete": the event, its level
            trace and your decision all stay. The word has to carry that. */}
        <Button
          variant="outline"
          size="sm"
          className="h-7 text-xs"
          disabled={!purgeable || purgeable.files === 0}
          onClick={() => setConfirming(true)}
        >
          Delete the recordings
        </Button>
      </div>
      {purgeable?.files === 0 && (
        <p className="w-full text-xs text-muted-foreground">
          Nothing is left to delete for these events. Their recordings are already gone.
        </p>
      )}

      <PurgeConfirm
        open={confirming}
        onOpenChange={(open) => {
          setConfirming(open)
          if (!open && done) {
            setDone(null)
            onPurged(done)
          }
        }}
        target={{
          eventIds: selected,
          bytes: purgeable?.bytes ?? 0,
          files: purgeable?.files ?? 0,
        }}
        onDone={(reply) => {
          setDone(reply)
          space.reload()
        }}
      />
    </div>
  )
}
