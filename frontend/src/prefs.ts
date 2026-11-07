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
  /** Width of the history panel beside the terminal, in px. */
  historyWidth: number
  /** Whether the host list inside the rail is unfolded. */
  sidebarOpen: boolean
  /** Whether the rail is showing at all. Folded away, the content takes the
   *  whole window — which is the only thing somebody reading code on a server
   *  wants from this app's chrome. */
  railOpen: boolean

  // The terminal (§5.3 of the next spec). Local for the same reason as the rest:
  // a font that suits one screen is wrong on the next.

  /** Terminal font size in px. */
  termFontSize: number
  /** Line height as a percentage — 100 is the font's own. A whole number
   *  because every numeric preference here is one, and 1.15 rounds badly. */
  termLineHeight: number
  /** Font family. Empty means the app's monospace stack. */
  termFontFamily: string
  /** Programming ligatures (`->` drawn as an arrow) where the font has them. */
  termLigatures: boolean
  /** Copy a selection as soon as it is made, the way X11 terminals do. */
  termCopyOnSelect: boolean
  /** Colour scheme; see termThemes.ts. */
  termTheme: string
  /** Suggest the rest of a line from what was run on this host before. */
  termSuggestHistory: boolean
  /** Suggest the rest of a line from a list of common Linux commands. */
  termSuggestCommands: boolean
  /** Width of the detail panel on the AI work screen, in px. A diff wants
   *  room; a long session wants the graph to have it instead. */
  aiDetailWidth: number
}

/** The preferences that are a size. The rest are switches, and clamping a
 *  switch between two numbers is not a thing. */
export type NumericPref = { [K in keyof Prefs]: Prefs[K] extends number ? K : never }[keyof Prefs]

/** The ones that are text — a font name, a theme id. */
export type StringPref = { [K in keyof Prefs]: Prefs[K] extends string ? K : never }[keyof Prefs]

export const DEFAULTS: Prefs = {
  commandLogHeight: 200,
  liveLogHeight: 260,
  editorFontSize: 13,
  historyWidth: 320,
  sidebarOpen: true,
  railOpen: true,
  termFontSize: 13,
  termLineHeight: 100,
  termFontFamily: '',
  termLigatures: false,
  termCopyOnSelect: false,
  termTheme: 'app',
  termSuggestHistory: true,
  termSuggestCommands: true,
  aiDetailWidth: 400,
}

const LIMITS: Record<NumericPref, [number, number]> = {
  commandLogHeight: [80, 700],
  liveLogHeight: [100, 900],
  editorFontSize: [9, 32],
  // Wide enough for a docker command without wrapping; capped so the terminal
  // stays the thing on this tab.
  historyWidth: [220, 720],
  termFontSize: [9, 28],
  termLineHeight: [100, 160],
  aiDetailWidth: [280, 960],
}

function clamp<K extends keyof Prefs>(key: K, value: unknown): Prefs[K] {
  const fallback = DEFAULTS[key]
  if (typeof fallback === 'boolean') {
    // A stored non-boolean is a corrupted entry, not a truthiness question.
    return (typeof value === 'boolean' ? value : fallback) as Prefs[K]
  }
  if (typeof fallback === 'string') {
    // Bounded, because it ends up in a CSS font-family and a hand-edited store
    // should not be able to put a kilobyte there.
    return (typeof value === 'string' ? value.slice(0, 120) : fallback) as Prefs[K]
  }
  const [lo, hi] = LIMITS[key as NumericPref]
  const n = typeof value === 'number' && Number.isFinite(value) ? value : (fallback as number)
  return Math.round(Math.max(lo, Math.min(hi, n))) as Prefs[K]
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
    // The generic is what lets the assignment typecheck: iterating `keyof Prefs`
    // widens the value to a union, and no union member is assignable to every
    // slot. Pinning one key per call keeps the pair together.
    const take = <K extends keyof Prefs>(key: K) => {
      out[key] = clamp(key, got[key])
    }
    for (const key of Object.keys(DEFAULTS) as (keyof Prefs)[]) take(key)
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

export function setPref<K extends keyof Prefs>(key: K, value: Prefs[K]) {
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
