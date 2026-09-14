import { useSyncExternalStore } from 'react'
import { k } from './i18n'

// Light and dark (§8).
//
// # Three positions, not two
//
// "Follow the OS" is the default and has to stay reachable, because it is the
// only setting that keeps being right — somebody whose desktop switches at
// sunset wants the app to switch with it. A two-position switch cannot express
// it: once you have clicked either half you are pinned until you find the
// setting again. So the stored value has three states and the empty one is the
// default, exactly as the language preference does it.
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

/** What the user chose. Empty is the default: follow the OS. */
export type Theme = '' | 'light' | 'dark'

/** What that resolves to. What the attribute is actually set to. */
export type ResolvedTheme = 'light' | 'dark'

// k(), not t(): this table is built once at module load and the picker
// translates each label as it renders, so a language change redraws it without
// the table having to be rebuilt.
export const THEMES: { id: Theme; label: string; title: string }[] = [
  { id: '', label: k('시스템'), title: k('운영체제 설정을 따릅니다') },
  { id: 'light', label: k('라이트'), title: k('항상 밝게') },
  { id: 'dark', label: k('다크'), title: k('항상 어둡게') },
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

export function getTheme(): Theme {
  return current
}

/** Puts the resolved answer on the root element, which is all the CSS reads. */
function stamp() {
  document.documentElement.dataset.theme = resolvedTheme()
  emit()
}

export function setTheme(theme: Theme) {
  current = theme
  try {
    // Written even for the default, so a machine that was pinned to dark and is
    // then set back to "follow the OS" does not open dark once more before the
    // bootstrap arrives.
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

  // Only matters while following the OS, but subscribing unconditionally is
  // simpler than subscribing and unsubscribing as the choice changes, and
  // `stamp` is a no-op when the answer has not moved.
  window
    .matchMedia?.('(prefers-color-scheme: dark)')
    .addEventListener('change', () => {
      if (!current) stamp()
    })
}

/**
 * The current choice, as React state.
 *
 * The picker has to read it through this and not through getTheme(). Its
 * <select> is controlled, and React restores a controlled input to its last
 * rendered value when the change handler produces no re-render — the choice
 * lives in a module variable, which React cannot see. Picking a theme changed
 * the colours and then snapped the control back to where it was, visibly or
 * not depending on whether something else happened to re-render the rail in
 * the same tick. It looked fine with the sidebar idle and wrong with a
 * terminal open.
 *
 * The choice and not the resolved value, which is a distinction with a case
 * behind it: moving from "light" back to "follow the OS" on a light desktop
 * leaves the resolved value where it was, so a component watching that one
 * never re-renders and the picker stays on "light".
 */
export function useTheme(): Theme {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    getTheme,
    () => '',
  )
}

/** Subscribes outside React, for the pieces that redraw imperatively. */
export function onThemeChange(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}
