import { useEffect, useRef } from 'react'

/**
 * Polls while somebody is actually looking (§3.2d).
 *
 * Every view here refreshes on a timer, and the server pays for each tick — an
 * Exec channel, a forked sshd, a shell. That is a fair price for a window
 * somebody is reading and a bad one for a window that has been minimised since
 * this morning. Servers are usually specced for the job they run and not much
 * more, so the app stops asking when the answer cannot be seen.
 *
 * Hidden means hidden, not unfocused: watching a graph while typing in another
 * window is exactly the case this must not break.
 *
 * A tick fires immediately on becoming visible, so coming back shows fresh data
 * rather than however stale the last frame was.
 */
export function usePoll(tick: () => unknown, everyMs: number, active = true) {
  // Held in a ref so a caller that rebuilds its closure every render does not
  // restart the timer — which would poll on every render instead of on time.
  const latest = useRef(tick)
  latest.current = tick

  useEffect(() => {
    if (!active) return

    let timer = 0
    const stop = () => {
      if (timer) {
        window.clearInterval(timer)
        timer = 0
      }
    }
    const start = () => {
      if (timer || document.hidden) return
      void latest.current()
      timer = window.setInterval(() => void latest.current(), everyMs)
    }
    const onVisibility = () => (document.hidden ? stop() : start())

    document.addEventListener('visibilitychange', onVisibility)
    start()
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      stop()
    }
  }, [everyMs, active])
}
