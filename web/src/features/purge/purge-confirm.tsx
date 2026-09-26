import { useEffect } from "react"

import { ErrorNote } from "@/components/state"
import { Button } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { formatBytes } from "@/features/system/status"
import { tallyPurge, usePurge } from "@/features/purge/use-purge"
import type { PurgeReply } from "@/lib/api"

/** What the owner is about to delete. */
export interface PurgeTarget {
  eventIds: number[]
  /** Absent means both kinds. */
  kinds?: ("audio" | "video")[]
  /** What the server says these recordings take. */
  bytes: number
  /** How many files they are, from the same reading. */
  files: number
  /**
   * True when the list is part of a larger match, so the panel can say the
   * rest stays behind.
   */
  more?: boolean
}

/**
 * The confirmation. Deleting a recording cannot be undone, so this is a
 * panel with its own button, not a hover and not a second click in the same
 * place. It names how many events and how many megabytes, and it states in
 * one sentence what survives.
 */
export function PurgeConfirm({
  open,
  onOpenChange,
  target,
  onDone,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  target: PurgeTarget
  /** Called once a purge has run, so the screen can re-read what it shows. */
  onDone: (reply: PurgeReply) => void
}) {
  const purge = usePurge()

  // A closed panel forgets the last run, so opening it asks again rather
  // than showing an old answer.
  useEffect(() => {
    if (!open) purge.reset()
  }, [open, purge.reset])

  const events = target.eventIds.length
  const kindWord = kindsWord(target.kinds)

  async function confirm() {
    const reply = await purge.run(target.eventIds, target.kinds)
    if (reply) onDone(reply)
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>
            {purge.reply ? "The recordings are deleted" : "Delete the recordings?"}
          </SheetTitle>
          <SheetDescription>
            {purge.reply
              ? "The measurements are untouched."
              : "This cannot be undone. Read it before you press it."}
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 text-sm">
          {purge.reply ? (
            <Outcome reply={purge.reply} />
          ) : (
            <>
              <p className="text-foreground">
                This deletes the {kindWord} of{" "}
                <span className="tabular-nums">
                  {events === 1 ? "1 event" : `${events} events`}
                </span>
                : <span className="tabular-nums">{fileWord(target.files)}</span>, {""}
                <span className="tabular-nums">{formatBytes(target.bytes)}</span> in all.
              </p>
              <p className="text-muted-foreground">
                What stays: the measurement, the level trace, your decision, and your note. Each
                event also keeps the size and the SHA-256 of the recording it had, so the record
                still says a recording existed and what it was.
              </p>
              <p className="text-muted-foreground">
                What goes: the file. There is no copy of it, and there is no undo.
              </p>
              {target.more && (
                <p className="text-muted-foreground">
                  This is as many events as one request may carry. Run it again for the rest.
                </p>
              )}
              {purge.error && <ErrorNote error={purge.error} />}
            </>
          )}
        </div>

        <SheetFooter>
          {purge.reply ? (
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              Close
            </Button>
          ) : (
            <div className="flex flex-col gap-2 sm:flex-row-reverse sm:justify-start">
              <Button onClick={confirm} disabled={purge.busy || events === 0}>
                {purge.busy ? "Deleting" : "Delete the recordings"}
              </Button>
              <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={purge.busy}>
                Keep them
              </Button>
            </div>
          )}
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}

/** What the purge actually did, in the server's own figures. */
function Outcome({ reply }: { reply: PurgeReply }) {
  const tally = tallyPurge(reply)
  return (
    <div className="flex flex-col gap-3">
      <p className="text-foreground">
        <span className="tabular-nums">{fileWord(tally.purged)}</span> deleted,{" "}
        <span className="tabular-nums">{formatBytes(tally.bytes)}</span> reclaimed.
      </p>
      {tally.alreadyGone > 0 && (
        <p className="text-muted-foreground">
          <span className="tabular-nums">{fileWord(tally.alreadyGone)}</span> had already gone from
          the disk. The deletion is recorded for them too, so the record says they were removed on
          purpose rather than lost.
        </p>
      )}
      {tally.alreadyPurged > 0 && (
        <p className="text-muted-foreground">
          <span className="tabular-nums">{fileWord(tally.alreadyPurged)}</span> had already been
          deleted. The first record of each stands.
        </p>
      )}
      {tally.refused > 0 && (
        <p className="text-foreground" role="alert">
          <span className="tabular-nums">{fileWord(tally.refused)}</span> were not deleted.{" "}
          {tally.refusedReason} Look at System for the detail.
        </p>
      )}
      {tally.unknownEvents > 0 && (
        <p className="text-muted-foreground">
          <span className="tabular-nums">{tally.unknownEvents}</span> of the numbers are not events.
          Nothing was deleted for them.
        </p>
      )}
      <p className="text-muted-foreground">
        The deletion is in the health log, with who asked and how many bytes came back.
      </p>
    </div>
  )
}

function fileWord(n: number): string {
  return n === 1 ? "1 file" : `${n} files`
}

function kindsWord(kinds?: ("audio" | "video")[]): string {
  if (!kinds || kinds.length === 2) return "audio and video recordings"
  if (kinds[0] === "audio") return "audio recordings"
  return "video recordings"
}
