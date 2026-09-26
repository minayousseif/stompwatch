import { useCallback, useRef, useState } from "react"
import { toast } from "sonner"

import { api, errorMessage, type NoiseEvent, type Review, type ReviewStatus } from "@/lib/api"

/**
 * Who owns the decision on screen.
 *
 * The rule this file exists to keep: **a mark is only ever written from the
 * server's own answer.** While a save is in flight the row says it is saving;
 * it does not yet claim the decision. If the request fails there is nothing to
 * undo, because nothing was recorded, and the row shows what it showed before
 * with a sentence saying the save did not happen.
 *
 * The whole claim this product makes is "every event in this report was
 * reviewed by a human". A mark on screen that is not in the database would
 * break that claim quietly, which is the worst thing this screen could do.
 */
export interface ReviewDesk {
  /** The decision to show for an event: the server's latest word on it. */
  reviewOf: (event: NoiseEvent) => Review | null
  /** The decision being saved right now, or null. `"clear"` removes one. */
  pendingOf: (id: number) => ReviewStatus | "clear" | null
  /** Why the last save for this event did not happen, if it did not. */
  failureOf: (id: number) => string | null
  /** The event whose decision was stored a moment ago, for the row to settle. */
  settled: number | null
  /** The note being edited for an event, which starts as the stored note. */
  noteOf: (event: NoiseEvent) => string
  setNote: (id: number, note: string) => void
  /** True when the note on screen differs from the stored one. */
  noteChanged: (event: NoiseEvent) => boolean
  /** Record a decision, with whatever note is on screen. */
  mark: (event: NoiseEvent, status: ReviewStatus) => void
  /** Save the note again without changing the decision. */
  saveNote: (event: NoiseEvent) => void
  /** Remove the decision, so the event needs reviewing again. */
  clear: (event: NoiseEvent) => void
  /** How many saves are in flight. */
  saving: number
}

export function useReviewDesk(): ReviewDesk {
  // What the server has said since this screen loaded. An event not in here
  // shows the review that came with it.
  const [stored, setStored] = useState<Record<number, Review | null>>({})
  const [pending, setPending] = useState<Record<number, ReviewStatus | "clear">>({})
  const [failure, setFailure] = useState<Record<number, string>>({})
  const [drafts, setDrafts] = useState<Record<number, string>>({})
  const [settled, setSettled] = useState<number | null>(null)

  // The settle transition is cleared on a timer, so a row does not keep the
  // highlight for the rest of the night.
  const settleTimer = useRef<number | undefined>(undefined)

  const reviewOf = useCallback(
    (event: NoiseEvent) => (event.id in stored ? stored[event.id] : event.review),
    [stored],
  )

  const noteOf = useCallback(
    (event: NoiseEvent) => {
      if (event.id in drafts) return drafts[event.id]
      const current = event.id in stored ? stored[event.id] : event.review
      return current?.note ?? ""
    },
    [drafts, stored],
  )

  const noteChanged = useCallback(
    (event: NoiseEvent) => {
      if (!(event.id in drafts)) return false
      const current = event.id in stored ? stored[event.id] : event.review
      return drafts[event.id] !== (current?.note ?? "")
    },
    [drafts, stored],
  )

  const setNote = useCallback((id: number, note: string) => {
    setDrafts((all) => ({ ...all, [id]: note }))
  }, [])

  const forget = useCallback((id: number) => {
    setPending((all) => {
      const next = { ...all }
      delete next[id]
      return next
    })
  }, [])

  const succeeded = useCallback((id: number) => {
    setFailure((all) => {
      if (!(id in all)) return all
      const next = { ...all }
      delete next[id]
      return next
    })
    setSettled(id)
    window.clearTimeout(settleTimer.current)
    settleTimer.current = window.setTimeout(() => setSettled(null), 900)
  }, [])

  const mark = useCallback(
    (event: NoiseEvent, status: ReviewStatus) => {
      const id = event.id
      const note = event.id in drafts ? drafts[id] : (reviewOf(event)?.note ?? "")
      setPending((all) => ({ ...all, [id]: status }))
      api
        .saveReview(id, { status, note })
        .then((review) => {
          // The mark comes from the answer, never from the click.
          setStored((all) => ({ ...all, [id]: review }))
          setDrafts((all) => ({ ...all, [id]: review.note }))
          succeeded(id)
        })
        .catch((err) => {
          const message = errorMessage(err)
          setFailure((all) => ({ ...all, [id]: message }))
          toast.error(`Event ${id} was not saved.`, { description: message })
        })
        .finally(() => forget(id))
    },
    [drafts, forget, reviewOf, succeeded],
  )

  const saveNote = useCallback(
    (event: NoiseEvent) => {
      const current = reviewOf(event)
      if (!current) return
      mark(event, current.status)
    },
    [mark, reviewOf],
  )

  const clear = useCallback(
    (event: NoiseEvent) => {
      const id = event.id
      setPending((all) => ({ ...all, [id]: "clear" }))
      api
        .clearReview(id)
        .then(() => {
          setStored((all) => ({ ...all, [id]: null }))
          setDrafts((all) => ({ ...all, [id]: "" }))
          succeeded(id)
        })
        .catch((err) => {
          const message = errorMessage(err)
          setFailure((all) => ({ ...all, [id]: message }))
          toast.error(`The decision on event ${id} was not removed.`, { description: message })
        })
        .finally(() => forget(id))
    },
    [forget, succeeded],
  )

  return {
    reviewOf,
    pendingOf: (id) => pending[id] ?? null,
    failureOf: (id) => failure[id] ?? null,
    settled,
    noteOf,
    setNote,
    noteChanged,
    mark,
    saveNote,
    clear,
    saving: Object.keys(pending).length,
  }
}
