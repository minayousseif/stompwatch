import { useCallback, useEffect, useRef, useState } from "react"

/** A fast answer should not flash a spinner, so waiting is announced late. */
const SPINNER_DELAY_MS = 300

export interface Resource<T> {
  data: T | null
  /** True only once the request has been slow enough to be worth saying. */
  loading: boolean
  error: unknown
  /** Fetch again, keeping whatever is already on screen. */
  reload: () => void
}

/**
 * Fetch once and again whenever `deps` change. The fetch gets an AbortSignal,
 * so a reload or an unmount cancels the request that is no longer wanted.
 */
export function useResource<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  deps: unknown[],
): Resource<T> {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [attempt, setAttempt] = useState(0)

  // Keep the latest fetcher without making it a dependency: it is usually a
  // new closure on every render, which would loop.
  const latest = useRef(fetcher)
  latest.current = fetcher

  useEffect(() => {
    const controller = new AbortController()
    let done = false

    const timer = window.setTimeout(() => {
      if (!done) setLoading(true)
    }, SPINNER_DELAY_MS)

    latest
      .current(controller.signal)
      .then((value) => {
        if (controller.signal.aborted) return
        done = true
        setData(value)
        setError(null)
      })
      .catch((err) => {
        if (controller.signal.aborted) return
        done = true
        setError(err)
      })
      .finally(() => {
        if (controller.signal.aborted) return
        window.clearTimeout(timer)
        setLoading(false)
      })

    return () => {
      done = true
      window.clearTimeout(timer)
      controller.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, attempt])

  const reload = useCallback(() => setAttempt((n) => n + 1), [])

  return { data, loading, error, reload }
}
