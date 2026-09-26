import { useCallback, useState } from "react"

import { api, type PurgeReply } from "@/lib/api"

/** One run of a purge: what it is doing and what it did. */
export interface PurgeRun {
  busy: boolean
  error: unknown
  reply: PurgeReply | null
  /** Delete the recordings of these events. Returns null on a failure. */
  run: (eventIds: number[], kinds?: ("audio" | "video")[]) => Promise<PurgeReply | null>
  /** Forget the last run, so the panel asks again rather than reporting. */
  reset: () => void
}

/**
 * Run a purge and hold its answer. The request always carries the
 * confirmation, because this hook is only ever called from the confirm
 * button of a confirmation panel.
 */
export function usePurge(): PurgeRun {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [reply, setReply] = useState<PurgeReply | null>(null)

  const run = useCallback(async (eventIds: number[], kinds?: ("audio" | "video")[]) => {
    setBusy(true)
    setError(null)
    try {
      const answer = await api.purgeMedia({ event_ids: eventIds, kinds, confirm: true })
      setReply(answer)
      return answer
    } catch (err) {
      setError(err)
      return null
    } finally {
      setBusy(false)
    }
  }, [])

  const reset = useCallback(() => {
    setError(null)
    setReply(null)
  }, [])

  return { busy, error, reply, run, reset }
}

/** How many files met each outcome, for the sentence after a purge. */
export interface PurgeTally {
  purged: number
  alreadyGone: number
  alreadyPurged: number
  noMedia: number
  refused: number
  /** The first refusal's reason, which is the same for every one of them. */
  refusedReason: string
  bytes: number
  unknownEvents: number
}

export function tallyPurge(reply: PurgeReply): PurgeTally {
  const out: PurgeTally = {
    purged: 0,
    alreadyGone: 0,
    alreadyPurged: 0,
    noMedia: 0,
    refused: 0,
    refusedReason: "",
    bytes: reply.purged_bytes,
    unknownEvents: reply.unknown_events.length,
  }
  for (const event of reply.events) {
    for (const result of event.results) {
      switch (result.outcome) {
        case "purged":
          out.purged++
          break
        case "already_gone":
          out.alreadyGone++
          break
        case "already_purged":
          out.alreadyPurged++
          break
        case "no_media":
          out.noMedia++
          break
        case "refused":
          out.refused++
          if (!out.refusedReason && result.reason) out.refusedReason = result.reason
          break
      }
    }
  }
  return out
}
