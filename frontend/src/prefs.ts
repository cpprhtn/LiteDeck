import { useSyncExternalStore } from 'react'

// How the user likes their window arranged (§4.7-1).
//
// localStorage rather than the Go config: these are worth nothing on another
// machine, they change several times a minute while someone is settling in, and
// a bad write here must not be able to damage the host list. The config file is
// for things that would hurt to lose.
//
// Every value is clamped on read as well as on write. A hand-edited or
// half-written entry should give a usable window, not a 4px editor.

const KEY = 'litedeck.prefs'

export interface Prefs {
  /** Height of the Command Log body when open, in px. */
  commandLogHeight: number
  /** Height of the live log panel — service and container output. */
  liveLogHeight: number
  /** Editor font size in px. */
  editorFontSize: number
}

export const DEFAULTS: Prefs = {
  commandLogHeight: 200,
  liveLogHeight: 260,
  editorFontSize: 13,
}

const LIMITS: Record<keyof Prefs, [number, number]> = {
  commandLogHeight: [80, 700],
  liveLogHeight: [100, 900],
  editorFontSize: [9, 32],
}

function clamp(key: keyof Prefs, value: unknown): number {
  const [lo, hi] = LIMITS[key]
  const n = typeof value === 'number' && Number.isFinite(value) ? value : DEFAULTS[key]
  return Math.round(Math.max(lo, Math.min(hi, n)))
}

function load(): Prefs {
  try {
    const raw = window.localStorage.getItem(KEY)
    if (!raw) return { ...DEFAULTS }
    const got = JSON.parse(raw) as Partial<Prefs>
    // Driven off DEFAULTS rather than listed again: a new preference should be
    // one line in one place, and a key missing from the stored object — which
    // is every key, the first time a new one ships — falls back through clamp.
    const out = { ...DEFAULTS }
    for (const key of Object.keys(DEFAULTS) as (keyof Prefs)[]) {
      out[key] = clamp(key, got[key])
    }
    return out
  } catch {
    // Unparseable, or storage disabled entirely. Defaults are a fine answer and
    // there is nothing here worth telling the user about.
    return { ...DEFAULTS }
  }
}

let prefs = load()
const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

function subscribe(fn: () => void) {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

export function usePref<K extends keyof Prefs>(key: K): Prefs[K] {
  return useSyncExternalStore(subscribe, () => prefs[key])
}

export function getPref<K extends keyof Prefs>(key: K): Prefs[K] {
  return prefs[key]
}

export function setPref<K extends keyof Prefs>(key: K, value: number) {
  const next = clamp(key, value)
  if (next === prefs[key]) return
  prefs = { ...prefs, [key]: next }
  emit()
  try {
    window.localStorage.setItem(KEY, JSON.stringify(prefs))
  } catch {
    // A full or blocked store costs the setting at next launch, not this one.
  }
}

export function resetPref(key: keyof Prefs) {
  setPref(key, DEFAULTS[key])
}
