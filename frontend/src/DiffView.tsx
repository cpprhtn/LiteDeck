import { useEffect, useRef, useState } from 'react'
import { MergeView } from '@codemirror/merge'
import { Compartment, EditorState, type Extension } from '@codemirror/state'
import { EditorView, lineNumbers } from '@codemirror/view'
import { detectLanguage } from './editorLanguage'
import { editorTheme } from './editorTheme'

// What this save is about to change (§4.7-3).
//
// The same idea as the Command Log: show the work before doing it. Editing a
// config on a running server is the case where "I thought I only changed the
// port" and "I also deleted a block by accident" look identical until something
// stops answering.
//
// Loaded on demand — the diff machinery is only reached by pressing save, and
// most saves in a session go through it once.

export function DiffView({
  path,
  before,
  after,
}: {
  path: string
  before: string
  after: string
}) {
  const host = useRef<HTMLDivElement>(null)
  const [changes, setChanges] = useState<number | null>(null)

  useEffect(() => {
    const language = new Compartment()
    const base: Extension[] = [
      lineNumbers(),
      EditorView.editable.of(false),
      EditorState.readOnly.of(true),
      EditorView.lineWrapping,
      language.of([]),
      editorTheme,
    ]
    const view = new MergeView({
      a: { doc: before, extensions: base },
      b: { doc: after, extensions: base },
      parent: host.current!,
      // A one-line change in a 400-line config should show that one line, not
      // 400 lines with a mark somewhere in the middle.
      collapseUnchanged: { margin: 3, minSize: 6 },
      highlightChanges: true,
      gutter: true,
    })
    setChanges(view.chunks.length)

    // The grammar is optional here — a diff is readable without it, so it is
    // applied if it arrives and skipped if it does not. A compartment, not a
    // full reconfigure: MergeView installs extensions of its own on both sides,
    // and replacing the state wholesale would take the diff with them.
    let live = true
    detectLanguage(path)
      ?.load()
      .then((lang) => {
        if (!live) return
        for (const side of [view.a, view.b]) {
          side.dispatch({ effects: language.reconfigure(lang) })
        }
      })
      .catch(() => {})

    return () => {
      live = false
      view.destroy()
    }
  }, [path, before, after])

  return (
    <>
      <div className="diff-head muted small">
        {changes === 0
          ? '바뀐 내용이 없습니다.'
          : changes === null
            ? ''
            : `${changes}곳이 바뀝니다 — 왼쪽이 서버, 오른쪽이 저장할 내용입니다.`}
      </div>
      <div className="diff-host" ref={host} />
    </>
  )
}
