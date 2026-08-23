import { useSyncExternalStore } from 'react'
import type { MetricsView } from './ipc'

// The latest metrics sample per host, published once and read by everyone.
//
// The summary bar is mounted above every tab, so it is already polling for
// these numbers on every host the user is looking at. Anything else that wants
// them — the resource detail, and the history behind the sparklines — reads
// from here rather than asking the server again. A second poller for the same
// reading would double a round trip that a whole release went into shrinking.
//
// Lives outside React for the same reason openFiles.ts does: the resource view
// unmounts when the user visits another tab, and the reading should not have to
// be fetched again on the way back.

type Listener = () => void

const NO_SAMPLES: Sample[] = []

const latest = new Map<string, MetricsView>()
const history = new Map<string, Sample[]>()
const listeners = new Set<Listener>()

/**
 * One reading, stamped.
 *
 * The stamp is the point. Idle backoff stretches the gap between polls from two
 * seconds to thirty (A-45), so an evenly spaced array draws half an hour of
 * quiet as if it were two minutes of it — the line keeps its shape while the
 * time underneath it silently changes scale. Every value here is positioned by
 * when it was taken, not by where it landed in the array.
 */
export interface Sample {
  /** Date.now() at the moment the reading arrived. */
  t: number
  /** -1 where the reading was not available, never a real zero. */
  cpu: number
  mem: number
  /** Utilisation per GPU, in nvidia-smi's card order. */
  gpu: number[]
}

/** How long a sample is kept. An hour is roughly 1,800 readings at the active
 *  poll rate — small enough to hold, long enough to cover "it started about an
 *  hour ago". Older than this belongs to sar (arch/07 §5), not to memory. */
const HISTORY_MS = 60 * 60 * 1000

/**
 * The gap that counts as the app having been away rather than merely idle.
 *
 * Idle backoff tops out around thirty seconds, so anything past this is the
 * window having been closed, the laptop asleep, or the host disconnected — and
 * that is a hole in the record, not a flat stretch. Drawing through it would be
 * the one thing this feature must not do: inventing a reading nobody took.
 */
export const GAP_MS = 90_000

/** Called by whoever owns the poll — today the summary bar. */
export function publishMetrics(hostID: string, m: MetricsView) {
  latest.set(hostID, m)

  const now = Date.now()
  const rows = history.get(hostID) ?? NO_SAMPLES

  // Trim by age rather than by count: a count would hold six times longer at
  // the idle rate than at the active one, so the window would quietly change
  // length depending on whether anyone was using the app.
  const cutoff = now - HISTORY_MS
  let drop = 0
  while (drop < rows.length && rows[drop].t < cutoff) drop++

  // A new array every time, never a push into the old one. useSyncExternalStore
  // compares snapshots by reference, so a list mutated in place is a list React
  // is entitled to believe did not change — and the chart would sit still while
  // the readings kept arriving.
  const next = rows.slice(drop)
  next.push({
    t: now,
    cpu: m.cpu,
    mem: m.memPercent,
    gpu: m.gpus.map((g) => g.utilization),
  })
  history.set(hostID, next)

  listeners.forEach((fn) => fn())
}

/** Called when a host disconnects. A stale reading outliving its connection
 *  would show the numbers from before the server went away, and the history
 *  would be spliced onto whatever the next connection reports. */
export function forgetMetrics(hostID: string) {
  const had = latest.delete(hostID)
  history.delete(hostID)
  if (had) listeners.forEach((fn) => fn())
}

/** Every retained sample for a host, oldest first. */
export function useMetricsHistory(hostID: string): Sample[] {
  return useSyncExternalStore(
    subscribe,
    () => history.get(hostID) ?? NO_SAMPLES,
    () => NO_SAMPLES,
  )
}

function subscribe(fn: Listener): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

/** The most recent sample for a host, or null before the first one arrives. */
export function useMetrics(hostID: string): MetricsView | null {
  return useSyncExternalStore(
    subscribe,
    () => latest.get(hostID) ?? null,
    () => null,
  )
}
