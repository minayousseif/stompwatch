import { useEffect, useRef } from "react"

/**
 * Run something on a timer, without restarting the timer on every render.
 * Live and System both re-read their numbers while they are open.
 */
export function useInterval(run: () => void, everyMs: number) {
  const saved = useRef(run)
  saved.current = run
  useEffect(() => {
    const timer = window.setInterval(() => saved.current(), everyMs)
    return () => window.clearInterval(timer)
  }, [everyMs])
}
