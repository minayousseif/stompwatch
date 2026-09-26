import { useEffect, useRef, useState } from "react"

import { url, type LiveReading } from "@/lib/api"

/** How the stream itself is doing, which is not the same as what it last said. */
export type LiveConnection = "connecting" | "open" | "lost"

export interface Live {
  reading: LiveReading | null
  connection: LiveConnection
  /** When the browser last received a line, so the interface can age it. */
  receivedAt: number | null
  /** How long the stream has been silent, in seconds. */
  silentFor: number
  /** True when the reading on screen is a live measurement right now. */
  current: boolean
}

const FIRST_RETRY_MS = 1000
const MAX_RETRY_MS = 15000

/** The server sends a line every second, so this much silence is wrong. */
const SILENT_S = 8

/**
 * Silence for this long means the socket is open but dead. A proxy in the
 * middle can hold a connection whose far end has gone, and the browser never
 * raises an error for it. Start again rather than trust it.
 */
const GIVE_UP_S = 20

/**
 * Read the live meter over server-sent events and reconnect on its own.
 *
 * A frozen number that still looks live is the worst failure this screen has,
 * so the reading is only called current while lines keep arriving, and the
 * state of the stream sits next to it.
 */
export function useLive(): Live {
  const [reading, setReading] = useState<LiveReading | null>(null)
  const [connection, setConnection] = useState<LiveConnection>("connecting")
  const [receivedAt, setReceivedAt] = useState<number | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const [restart, setRestart] = useState(0)
  const retry = useRef(FIRST_RETRY_MS)

  useEffect(() => {
    let source: EventSource | null = null
    let timer = 0
    let stopped = false

    const open = () => {
      if (stopped) return
      source = new EventSource(url.live())

      source.onopen = () => {
        retry.current = FIRST_RETRY_MS
        setConnection("open")
      }

      source.onmessage = (message) => {
        try {
          setReading(JSON.parse(message.data) as LiveReading)
          setReceivedAt(Date.now())
          setConnection("open")
        } catch {
          // A line we cannot read is not a reason to drop the stream.
        }
      }

      source.onerror = () => {
        source?.close()
        source = null
        if (stopped) return
        setConnection("lost")
        // Back off, so a collector that is down is not hammered.
        timer = window.setTimeout(open, retry.current)
        retry.current = Math.min(retry.current * 2, MAX_RETRY_MS)
      }
    }

    open()

    return () => {
      stopped = true
      window.clearTimeout(timer)
      source?.close()
    }
  }, [restart])

  // Age the reading once a second, so silence shows up on screen by itself.
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])

  const silentFor = receivedAt === null ? 0 : Math.max(0, Math.round((now - receivedAt) / 1000))

  useEffect(() => {
    if (connection === "open" && silentFor >= GIVE_UP_S) {
      setReceivedAt(null)
      setRestart((n) => n + 1)
    }
  }, [connection, silentFor])

  const current =
    connection === "open" &&
    reading !== null &&
    reading.laeq !== null &&
    !reading.stale &&
    silentFor < SILENT_S

  return { reading, connection, receivedAt, silentFor, current }
}
