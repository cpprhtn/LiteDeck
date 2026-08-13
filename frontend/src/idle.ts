// How long the window has gone untouched, and how much that should slow polling.
//
// `document.hidden` already stops the timers when the window is minimised —
// measured on macOS 2026-08-13, minimising does fire `visibilitychange`. What it
// does not cover is the more common state: the window sitting open on a second
// monitor while the user works elsewhere. Switching apps leaves the page
// `visible`, so without this the app keeps asking the server every two seconds
// for a screen nobody has looked at since this morning.
//
// The server pays for each tick — an Exec channel, a forked sshd, a shell — and
// servers are usually specced for the job they run and not much more (§3.2d).
//
// Slowing down rather than stopping: a glance at a window that has been idle for
// an hour should still show something recent, and the wake path below refreshes
// it the instant the mouse moves.

/** Idle thresholds, longest first. */
const STEPS: { after: number; factor: number }[] = [
  { after: 10 * 60_000, factor: 15 },
  { after: 2 * 60_000, factor: 4 },
]

/** Overridden by tests and probes; production never changes it. */
let steps = STEPS

let lastActivity = Date.now()
const listeners = new Set<() => void>()

/** Multiplier to apply to a poll interval right now. 1 while in use. */
export function idleFactor(): number {
  const quiet = Date.now() - lastActivity
  for (const s of steps) {
    if (quiet >= s.after) return s.factor
  }
  return 1
}

/**
 * Called when the user touches the window after having been idle.
 *
 * Pollers use it to refresh immediately rather than serving whatever was on
 * screen when they slowed down — coming back to a stale table is the failure
 * this whole mechanism has to avoid being blamed for.
 */
export function onWake(fn: () => void): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

function touched() {
  const wasIdle = idleFactor() > 1
  lastActivity = Date.now()
  if (wasIdle) {
    for (const l of listeners) l()
  }
}

// Capture, and passive where it matters: these must not interfere with any
// handler in the app, and pointermove fires constantly.
const EVENTS = ['pointerdown', 'pointermove', 'keydown', 'wheel', 'focus'] as const
for (const e of EVENTS) {
  window.addEventListener(e, touched, { capture: true, passive: true })
}
// Returning from a minimised window counts as activity: visibilitychange fires
// before any pointer event, and the poller restarting should not immediately
// inherit an hour-old idle factor.
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) touched()
})

/** Test seam. Pass nothing to restore the shipped thresholds. */
export function setIdleSteps(next?: { after: number; factor: number }[]) {
  steps = next ?? STEPS
  lastActivity = Date.now()
}
