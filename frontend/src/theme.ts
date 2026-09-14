import { useSyncExternalStore } from 'react'
import { k } from './i18n'

// Light and dark (§8).
//
// # Two positions, and a default that is not one of them
//
// The picker offers light and dark and nothing else. Underneath, the stored
// value has a third state — empty, meaning nobody has chosen yet — which is
// simply what settings.json says on a fresh install. It is resolved against
// the OS and never shown: the control displays the resolved answer, so a new
// user finds it already sitting on whatever their desktop is set to.
//
// Until the first pick the app does follow the OS, and after it the choice is
// pinned. That is the whole of the difference, and it is deliberately not a
// third position on the control: "follow the OS" is a state people end up in
// rather than one they go looking for.
//
// # Where the choice lives
//
// settings.json, written by Go, is the durable record. localStorage holds a
// copy for one reason: the inline script in index.html has to decide the theme
// before the first paint, and at that moment nothing else is readable
// synchronously. Go is the authority — `init` reconciles them on the first
// bootstrap and the stored setting wins.
//
// # What the resolved answer is for
//
// CSS reads `data-theme` on the root element and nothing else; there is no
// `prefers-color-scheme` block left in tokens.css. That keeps one copy of the
// dark palette instead of two, at the cost of this module having to resolve
// "follow the OS" itself and re-resolve it when the OS changes underneath.

/** What is stored. Empty means nobody has picked yet. */
export type Theme = '' | 'light' | 'dark'

/** What that resolves to, and what the attribute is set to. */
export type ResolvedTheme = 'light' | 'dark'

// k(), not t(): this table is built once at module load and the picker
// translates each label as it renders, so a language change redraws it without
// the table having to be rebuilt.
export const THEMES: { id: ResolvedTheme; label: string }[] = [
  { id: 'light', label: k('라이트') },
  { id: 'dark', label: k('다크') },
]

const KEY = 'litedeck.theme'

let current: Theme = ''
const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

function systemIsDark(): boolean {
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false
}

/** The theme in effect right now, with "follow the OS" resolved. */
export function resolvedTheme(): ResolvedTheme {
  if (current === 'light' || current === 'dark') return current
  return systemIsDark() ? 'dark' : 'light'
}

/**
 * Puts the resolved answer on the root element, which is all the CSS reads.
 *
 * Silent when the answer has not moved. The terminal rebuilds its palette on
 * every notification, and an OS event that resolves to the colours already on
 * screen is not worth that.
 */
function stamp() {
  const next = resolvedTheme()
  if (document.documentElement.dataset.theme === next) return
  document.documentElement.dataset.theme = next
  emit()
}

export function setTheme(theme: Theme) {
  current = theme
  try {
    // The empty case is cleared rather than left alone: it arrives from the
    // bootstrap when Go has no stored theme, and a stale copy here would paint
    // the next launch from a choice that is no longer recorded anywhere.
    if (theme) localStorage.setItem(KEY, theme)
    else localStorage.removeItem(KEY)
  } catch {
    // Site data blocked. The choice still applies for this run; it just will
    // not survive a restart ahead of the bootstrap, which then supplies it.
  }
  stamp()
}

/**
 * Applies the stored choice and starts following the OS.
 *
 * `stored` comes from Go. It is the authority: the inline script may have
 * painted from a localStorage copy that is stale — another window changed it,
 * or somebody edited settings.json — and this is where that is settled.
 */
export function initTheme(stored: string) {
  setTheme(stored === 'light' || stored === 'dark' ? stored : '')

  // Only matters before the first pick, but subscribing unconditionally is
  // simpler than subscribing and unsubscribing as the choice changes.
  window
    .matchMedia?.('(prefers-color-scheme: dark)')
    .addEventListener('change', () => {
      if (!current) stamp()
    })
}

/**
 * The theme in effect, as React state.
 *
 * The picker has to read it through this rather than by calling
 * resolvedTheme(). Its <select> is controlled, and React restores a controlled
 * input to its last rendered value when the change handler produces no
 * re-render. The value lives in a module variable, which React cannot see, so
 * picking a theme changed the colours and then snapped the control back to
 * where it was — visibly or not depending on whether something else happened to
 * re-render the rail in the same tick. It looked right with the sidebar idle
 * and wrong with a terminal open.
 *
 * The resolved value, not the stored one: the stored one has an empty state
 * that the control has no position for.
 */
export function useTheme(): ResolvedTheme {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    resolvedTheme,
    () => 'light',
  )
}

/** Subscribes outside React, for the pieces that redraw imperatively. */
export function onThemeChange(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}
