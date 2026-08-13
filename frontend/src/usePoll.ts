import { useEffect, useRef } from 'react'
import { idleFactor, onWake } from './idle'

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
 *
 * Between those two states is the one `document.hidden` cannot see — a window
 * left open and untouched — so the interval is stretched by idle.ts and snaps
 * back the moment the mouse moves.
 */
export function usePoll(tick: () => unknown, everyMs: number, active = true) {
  // Held in a ref so a caller that rebuilds its closure every render does not
  // restart the timer — which would poll on every render instead of on time.
  const latest = useRef(tick)
  latest.current = tick

  useEffect(() => {
    if (!active) return

    let timer = 0
    // setTimeout rather than setInterval: the gap is re-read before every tick,
    // so going idle takes effect at the next tick instead of needing the timer
    // to be torn down and rebuilt.
    const clear = () => {
      if (timer) {
        window.clearTimeout(timer)
        timer = 0
      }
    }
    const schedule = () => {
      clear()
      timer = window.setTimeout(fire, everyMs * idleFactor())
    }
    const fire = () => {
      timer = 0
      if (document.hidden) return // start() will resume it
      void latest.current()
      schedule()
    }
    /** Tick now and restart the clock. */
    const start = () => {
      if (document.hidden) return
      void latest.current()
      schedule()
    }

    const onVisibility = () => (document.hidden ? clear() : start())
    const offWake = onWake(start)

    document.addEventListener('visibilitychange', onVisibility)
    start()
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      offWake()
      clear()
    }
  }, [everyMs, active])
}
