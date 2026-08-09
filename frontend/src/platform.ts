// Platform conventions, in one place.
//
// §8 requires shortcuts that match the user's OS: Enter renames on macOS but
// opens on Windows, Delete removes on Windows but does nothing on macOS. Getting
// this wrong breaks the "these are my own files" feel (§1.1) far more
// thoroughly than any wrong icon would.
//
// Every component asks this module instead of hard-coding a key string, so the
// platform-native work in §4.7-1 stays a one-file change rather than a sweep
// through the whole UI.

import { GetPlatform } from './ipc'

export interface Platform {
  os: string
  arch: string
  isMac: boolean
  modKey: string
  modLabel: string
}

/** Actions the UI can bind. Names, not keys — keys are per platform. */
export type Action =
  | 'rename'
  | 'delete'
  | 'refresh'
  | 'find'
  | 'open'
  | 'parentDir'
  | 'newFolder'
  | 'copyPath'

interface Binding {
  /** KeyboardEvent.key */
  key: string
  mod?: boolean
  shift?: boolean
  alt?: boolean
  /** What to render in menus. */
  label: string
}

const MAC: Record<Action, Binding> = {
  rename: { key: 'Enter', label: '↩' },
  delete: { key: 'Backspace', mod: true, label: '⌘⌫' },
  refresh: { key: 'r', mod: true, label: '⌘R' },
  find: { key: 'f', mod: true, label: '⌘F' },
  open: { key: 'ArrowDown', mod: true, label: '⌘↓' },
  parentDir: { key: 'ArrowUp', mod: true, label: '⌘↑' },
  newFolder: { key: 'n', mod: true, shift: true, label: '⇧⌘N' },
  copyPath: { key: 'c', mod: true, alt: true, label: '⌥⌘C' },
}

const PC: Record<Action, Binding> = {
  rename: { key: 'F2', label: 'F2' },
  delete: { key: 'Delete', label: 'Del' },
  refresh: { key: 'F5', label: 'F5' },
  find: { key: 'f', mod: true, label: 'Ctrl+F' },
  open: { key: 'Enter', label: 'Enter' },
  parentDir: { key: 'ArrowUp', alt: true, label: 'Alt+↑' },
  newFolder: { key: 'n', mod: true, shift: true, label: 'Ctrl+Shift+N' },
  copyPath: { key: 'c', mod: true, shift: true, label: 'Ctrl+Shift+C' },
}

let platform: Platform = {
  os: 'unknown',
  arch: '',
  isMac: false,
  modKey: 'Control',
  modLabel: 'Ctrl',
}
let bindings = PC

/** Loads the real platform from Go. Call once at startup. */
export async function initPlatform(): Promise<Platform> {
  platform = await GetPlatform()
  bindings = platform.isMac ? MAC : PC
  return platform
}

export function getPlatform(): Platform {
  return platform
}

/** The label to show beside a menu item, e.g. "⌘R". */
export function shortcutLabel(action: Action): string {
  return bindings[action].label
}

/**
 * Whether the event came from somewhere the user is typing.
 *
 * Window-level shortcuts have to ask: on macOS `rename` is bare Enter and on
 * Windows `delete` is bare Delete, so an editor or a text field that does not
 * exclude itself turns ordinary typing into file operations. CodeMirror's
 * document is a contenteditable, which is why the tag list alone is not enough.
 */
export function isTyping(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null
  if (!t) return false
  if (t.isContentEditable) return true
  return ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName)
}

/** Whether a keyboard event should trigger an action on this platform. */
export function matches(e: KeyboardEvent, action: Action): boolean {
  const b = bindings[action]
  const mod = platform.isMac ? e.metaKey : e.ctrlKey
  return (
    e.key.toLowerCase() === b.key.toLowerCase() &&
    mod === !!b.mod &&
    e.shiftKey === !!b.shift &&
    e.altKey === !!b.alt
  )
}
