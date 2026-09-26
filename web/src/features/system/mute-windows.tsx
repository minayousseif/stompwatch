import { useState } from "react"
import { Trash2Icon } from "lucide-react"
import { toast } from "sonner"

import { EmptyNote, ErrorNote, LoadingNote } from "@/components/state"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Field, RangeFields } from "@/features/system/range-fields"
import { useResource } from "@/hooks/use-resource"
import { api, errorMessage, type MuteWindow } from "@/lib/api"
import { formatDayAndClock, formatSpan } from "@/lib/time"

/**
 * The windows the owner can explain. A window changes what the reading says,
 * never what the collector does, and the copy has to carry that: believing
 * an evening was excluded when the measurement is still on disk would be a
 * bad thing to find out in front of somebody else.
 */
export function MuteWindows() {
  const list = useResource((signal) => api.muteWindows(signal), [])
  const [from, setFrom] = useState<number | undefined>(undefined)
  const [to, setTo] = useState<number | undefined>(undefined)
  const [reason, setReason] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const windows = list.data?.windows ?? []
  const ordered = [...windows].sort((a, b) => b.start_ms - a.start_ms)
  const ready = from !== undefined && to !== undefined && to > from

  const add = () => {
    if (!ready) return
    setBusy(true)
    setError(null)
    api
      .addMuteWindow({ start_ms: from, end_ms: to, reason: reason.trim() })
      .then(() => {
        setFrom(undefined)
        setTo(undefined)
        setReason("")
        list.reload()
        toast.success("The window is in force. Events inside it are marked from now on.")
      })
      .catch(setError)
      .finally(() => setBusy(false))
  }

  const remove = (window: MuteWindow) => {
    setBusy(true)
    setError(null)
    api
      .removeMuteWindow(window.id)
      .then(() => {
        list.reload()
        toast.success("The window is gone. Its events count again.")
      })
      .catch(setError)
      .finally(() => setBusy(false))
  }

  return (
    <div className="flex flex-col gap-4">
      <p className="max-w-prose text-sm text-muted-foreground">
        A mute window marks the events you already know about, such as an evening you had guests or
        work being done in your own home. It never stops the collector: it keeps measuring, it keeps
        detecting, and it keeps recording clips through the window. What changes is the reading. The
        events it covers are marked as muted, left out of the summary counts, left out of the CSV
        export, and hidden from the events list until you ask for them. Nothing is deleted, and the
        measurement is still there to look at.
      </p>

      <div className="flex flex-wrap items-end gap-3 border-y border-border py-4">
        <RangeFields
          idPrefix="mute"
          from={from}
          to={to}
          onChange={(nextFrom, nextTo) => {
            setFrom(nextFrom)
            setTo(nextTo)
          }}
          fromLabel="Starts"
          toLabel="Ends"
        />
        <Field
          label="Reason"
          htmlFor="mute-reason"
          className="flex w-full min-w-0 flex-col gap-1.5 sm:w-auto sm:flex-1"
        >
          <Input
            id="mute-reason"
            className="h-9 w-full"
            placeholder="we had guests"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </Field>
        <Button size="sm" className="h-9" disabled={!ready || busy} onClick={add}>
          Add the window
        </Button>
      </div>

      {from !== undefined && to !== undefined && to <= from && (
        <p className="text-sm" role="alert">
          The end has to come after the start. Change one of the two.
        </p>
      )}

      {error !== null && (
        <p className="text-sm" role="alert">
          {errorMessage(error)}
        </p>
      )}

      {list.error ? (
        <ErrorNote error={list.error} onRetry={list.reload} />
      ) : list.data ? (
        ordered.length === 0 ? (
          <EmptyNote
            headline="No mute window is set."
            detail="Every detected event counts, which is the state to be in unless you know better."
          />
        ) : (
          <ul className="divide-y divide-border border-y border-border">
            {ordered.map((window) => (
              <li key={window.id} className="flex flex-wrap items-baseline gap-x-4 gap-y-1 py-2.5">
                <span className="text-sm">
                  {formatDayAndClock(window.start_ms)} to {formatDayAndClock(window.end_ms)}
                </span>
                <span className="text-xs text-muted-foreground">
                  {formatSpan(window.end_ms - window.start_ms)}
                </span>
                <span className="min-w-0 flex-1 truncate text-sm text-muted-foreground">
                  {window.reason || "No reason was written."}
                </span>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-8 gap-1.5 text-xs"
                  disabled={busy}
                  onClick={() => remove(window)}
                >
                  <Trash2Icon className="size-3.5" aria-hidden />
                  Remove
                </Button>
              </li>
            ))}
          </ul>
        )
      ) : list.loading ? (
        <LoadingNote what="the mute windows" />
      ) : null}
    </div>
  )
}
