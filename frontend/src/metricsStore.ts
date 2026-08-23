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

const latest = new Map<string, MetricsView>()
const listeners = new Set<Listener>()

/** Called by whoever owns the poll — today the summary bar. */
export function publishMetrics(hostID: string, m: MetricsView) {
  latest.set(hostID, m)
  listeners.forEach((fn) => fn())
}

/** Called when a host disconnects. A stale reading outliving its connection
 *  would show the numbers from before the server went away. */
export function forgetMetrics(hostID: string) {
  if (latest.delete(hostID)) listeners.forEach((fn) => fn())
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
