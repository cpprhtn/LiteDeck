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
 *
 * The next tick is scheduled from when the last one **finished**, never from
 * when it started, and never sooner than the last one took. A view whose query
 * is slower than its interval would otherwise restart the moment it returned
 * and hold an Exec channel continuously — and there are three of those for the
 * whole connection (A-1), so one slow view makes every other command queue
 * behind it. Observed on a host where `docker ps -a` took 19.8 seconds against
 * a 5 second interval: nothing overlapped, because the view guards that, but
 * the channel was never free.
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
    let stopped = false
    const schedule = (tookMs: number) => {
      clear()
      if (stopped) return
      // Never ask again sooner than the last answer took. At the interval this
      // is a no-op; past it, it is what keeps a slow query from owning a
      // channel end to end.
      const gap = Math.max(everyMs * idleFactor(), tookMs)
      timer = window.setTimeout(fire, gap)
    }
    /** Run one tick and schedule the next from when this one settles. */
    const run = async () => {
      const began = Date.now()
      try {
        await latest.current()
      } catch {
        // A failing view reports its own error; the timer must survive it, or
        // one bad answer stops the view refreshing for the rest of the session.
      }
      schedule(Date.now() - began)
    }
    const fire = () => {
      timer = 0
      if (document.hidden) return // start() will resume it
      void run()
    }
    /** Tick now and restart the clock. */
    const start = () => {
      if (document.hidden) return
      clear()
      void run()
    }

    const onVisibility = () => (document.hidden ? clear() : start())
    const offWake = onWake(start)

    document.addEventListener('visibilitychange', onVisibility)
    start()
    return () => {
      stopped = true
      document.removeEventListener('visibilitychange', onVisibility)
      offWake()
      clear()
    }
  }, [everyMs, active])
}
