import { useSyncExternalStore } from 'react'
import type { TextFile } from './ipc'

// Which files the editor has open, per host (§4.7-3).
//
// This lives outside React because the file explorer unmounts whenever the user
// looks at another tab, and an editor whose unsaved work disappears the moment
// somebody glances at the terminal is worse than no editor. The store is the
// answer to "what would be lost right now", which is also what a close guard
// needs to ask.

export type OpenFile = {
  path: string
  name: string
  /** What is in the editor right now. */
  doc: string
  /** What the server had when this was opened or last saved. Also the diff base. */
  base: string
  baseModTime: number
  baseSize: number
  perm: number
}

export type HostFiles = {
  files: OpenFile[]
  active: string | null
}

const EMPTY: HostFiles = { files: [], active: null }

const byHost = new Map<string, HostFiles>()
const listeners = new Set<() => void>()

// Width of the tree beside the editor. Not per host — it is how the user likes
// the window arranged, and it should not reset because they clicked elsewhere.
let treeWidth = 360

function emit() {
  for (const l of listeners) l()
}

function subscribe(fn: () => void) {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

function snapshot(hostID: string): HostFiles {
  return byHost.get(hostID) ?? EMPTY
}

function update(hostID: string, fn: (h: HostFiles) => HostFiles) {
  byHost.set(hostID, fn(snapshot(hostID)))
  emit()
}

function replace(h: HostFiles, path: string, fn: (f: OpenFile) => OpenFile): HostFiles {
  return { ...h, files: h.files.map((f) => (f.path === path ? fn(f) : f)) }
}

export function useOpenFiles(hostID: string): HostFiles {
  return useSyncExternalStore(subscribe, () => snapshot(hostID))
}

export function useTreeWidth(): number {
  return useSyncExternalStore(subscribe, () => treeWidth)
}

export function setTreeWidth(px: number) {
  treeWidth = Math.max(220, Math.min(px, 900))
  emit()
}

export function isDirty(f: OpenFile): boolean {
  return f.doc !== f.base
}

/** Opens a file, or brings it forward if it is already open. */
export function openFile(hostID: string, file: TextFile) {
  update(hostID, (h) => {
    if (h.files.some((f) => f.path === file.path)) return { ...h, active: file.path }
    const opened: OpenFile = {
      path: file.path,
      name: file.path.split('/').pop() || file.path,
      doc: file.content,
      base: file.content,
      baseModTime: file.modTime,
      baseSize: file.size,
      perm: file.perm,
    }
    return { files: [...h.files, opened], active: file.path }
  })
}

export function closeFile(hostID: string, path: string) {
  update(hostID, (h) => {
    const i = h.files.findIndex((f) => f.path === path)
    if (i < 0) return h
    const files = h.files.filter((f) => f.path !== path)
    // Land on the neighbour rather than on nothing: closing one of five tabs
    // should not empty the editor.
    const active =
      h.active !== path ? h.active : (files[i] ?? files[i - 1])?.path ?? null
    return { files, active }
  })
}

export function setActive(hostID: string, path: string) {
  update(hostID, (h) => ({ ...h, active: path }))
}

export function setDoc(hostID: string, path: string, doc: string) {
  update(hostID, (h) => replace(h, path, (f) => (f.doc === doc ? f : { ...f, doc })))
}

/** After a save: the editor's text is now what the server has. */
export function markSaved(hostID: string, path: string, modTime: number, size: number) {
  update(hostID, (h) =>
    replace(h, path, (f) => ({ ...f, base: f.doc, baseModTime: modTime, baseSize: size })),
  )
}

/** After rereading a file the server changed underneath us. */
export function resetTo(hostID: string, path: string, file: TextFile) {
  update(hostID, (h) =>
    replace(h, path, (f) => ({
      ...f,
      doc: file.content,
      base: file.content,
      baseModTime: file.modTime,
      baseSize: file.size,
      perm: file.perm,
    })),
  )
}

/** Keeps the edits, but rebases them on what the server now has, so the next
 *  save is compared against the right file instead of conflicting forever. */
export function rebase(hostID: string, path: string, file: TextFile) {
  update(hostID, (h) =>
    replace(h, path, (f) => ({
      ...f,
      base: file.content,
      baseModTime: file.modTime,
      baseSize: file.size,
    })),
  )
}

/** Does this path name the file, or the directory it lives under? */
function covers(target: string, path: string): boolean {
  return path === target || path.startsWith(target + '/')
}

/** Deleting in the tree has to reach the tabs holding what was deleted —
 *  including everything under a directory. */
export function forgetPaths(hostID: string, paths: string[]) {
  for (const f of snapshot(hostID).files) {
    if (paths.some((p) => covers(p, f.path))) closeFile(hostID, f.path)
  }
}

/** A rename moves the tab rather than closing it: the document is the same one,
 *  and closing it would drop unsaved edits without asking. */
export function renameOpen(hostID: string, from: string, to: string) {
  update(hostID, (h) => ({
    active: h.active && covers(from, h.active) ? h.active.replace(from, to) : h.active,
    files: h.files.map((f) => {
      if (!covers(from, f.path)) return f
      const path = f.path.replace(from, to)
      return { ...f, path, name: path.split('/').pop() || path }
    }),
  }))
}

/** Open files with unsaved changes that these paths would take with them. */
export function unsavedUnder(hostID: string, paths: string[]): OpenFile[] {
  return snapshot(hostID).files.filter(
    (f) => isDirty(f) && paths.some((p) => covers(p, f.path)),
  )
}

/** Disconnecting drops the tabs: their host is gone and so is any way to save. */
export function closeHost(hostID: string) {
  if (!byHost.has(hostID)) return
  byHost.delete(hostID)
  pendingReveal.delete(hostID)
  emit()
}

// A path the terminal asked to open (§4.6a).
//
// It is parked here rather than passed as a prop because `code .` arrives while
// the file view is still unmounted — the shell integration is what causes the
// app to switch tabs in the first place. The explorer collects it when it
// mounts, which also makes the ordering between the tab switch and the event
// stop mattering.
export type Reveal = { path: string; isDir: boolean; isNew: boolean; nonce: number }

const pendingReveal = new Map<string, Reveal>()
let revealSeq = 0

export function requestReveal(
  hostID: string,
  path: string,
  isDir: boolean,
  isNew = false,
) {
  revealSeq++
  pendingReveal.set(hostID, { path, isDir, isNew, nonce: revealSeq })
  emit()
}

export function useReveal(hostID: string): Reveal | undefined {
  return useSyncExternalStore(subscribe, () => pendingReveal.get(hostID))
}

/** Consumed once. Asking for the same path twice must still act twice, which is
 *  what the nonce is for; clearing it is what stops it acting on every render. */
export function clearReveal(hostID: string, nonce: number) {
  if (pendingReveal.get(hostID)?.nonce !== nonce) return
  pendingReveal.delete(hostID)
  emit()
}
