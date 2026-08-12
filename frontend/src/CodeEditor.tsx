import { useEffect, useRef } from 'react'
import { Compartment, EditorState, type Extension } from '@codemirror/state'
import {
  EditorView,
  drawSelection,
  dropCursor,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  rectangularSelection,
} from '@codemirror/view'
import {
  defaultKeymap,
  history,
  historyKeymap,
  indentWithTab,
  standardKeymap,
} from '@codemirror/commands'
import {
  bracketMatching,
  codeFolding,
  foldGutter,
  foldKeymap,
  indentOnInput,
  indentUnit,
} from '@codemirror/language'
import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete'
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search'
import { detectLanguage } from './editorLanguage'
import { editorTheme } from './editorTheme'
import { DEFAULTS, getPref, setPref, usePref } from './prefs'

// The code editor (§4.7-3).
//
// Line numbers, syntax highlighting and find/replace — the three things that
// separate "I can fix this config here" from "I will open Remote-SSH instead".
// Deliberately not an IDE: no completion sources, no diagnostics, no language
// server. Those need something installed on the server, which is the one thing
// this app promises never to do (원칙 1).
//
// The whole module, CodeMirror included, is loaded on demand — the editor is
// only reached by opening a file, and a session that never opens one should not
// pay for it. Same treatment as the terminal.

/** The one adjustable piece of the theme (§4.7-3). */
const fontTheme = (px: number) => EditorView.theme({ '&': { fontSize: `${px}px` } })

const zoom = (by: number) => () => {
  setPref('editorFontSize', getPref('editorFontSize') + by)
  return true
}

/** Extensions that never change. Built once; a fresh EditorState reuses it. */
function baseExtensions(
  onSave: () => void,
  language: Compartment,
  fontSize: Compartment,
): Extension[] {
  return [
    lineNumbers(),
    highlightActiveLineGutter(),
    highlightSpecialChars(),
    history(),
    codeFolding(),
    foldGutter(),
    drawSelection(),
    dropCursor(),
    EditorState.allowMultipleSelections.of(true),
    indentOnInput(),
    bracketMatching(),
    closeBrackets(),
    rectangularSelection(),
    highlightActiveLine(),
    highlightSelectionMatches(),
    search({ top: true }),
    // Tabs indent rather than moving focus. The usual accessibility argument
    // does not survive contact with a code editor, and Escape still steps out.
    keymap.of([
      {
        key: 'Mod-s',
        preventDefault: true,
        run: () => {
          onSave()
          return true
        },
      },
      // Zoom, on the keys every editor and browser uses. Before the default
      // keymap so Mod-0 is not taken by anything else, and preventDefault so
      // the webview does not zoom the whole window underneath us.
      { key: 'Mod-=', preventDefault: true, run: zoom(1) },
      { key: 'Mod-+', preventDefault: true, run: zoom(1) },
      { key: 'Mod--', preventDefault: true, run: zoom(-1) },
      {
        key: 'Mod-0',
        preventDefault: true,
        run: () => {
          setPref('editorFontSize', DEFAULTS.editorFontSize)
          return true
        },
      },
      ...closeBracketsKeymap,
      ...searchKeymap,
      ...historyKeymap,
      ...foldKeymap,
      ...defaultKeymap,
      indentWithTab,
    ]),
    indentUnit.of('  '),
    EditorView.lineWrapping,
    language.of([]),
    editorTheme,
    // Last, so this fontSize is the one that wins over anything the base theme
    // or a language extension might set.
    fontSize.of(fontTheme(getPref('editorFontSize'))),
  ]
}

export function CodeEditor({
  path,
  value,
  onChange,
  onSave,
  onCursor,
}: {
  path: string
  value: string
  onChange: (doc: string) => void
  onSave: () => void
  onCursor?: (line: number, col: number) => void
}) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const language = useRef(new Compartment())
  const fontSize = useRef(new Compartment())
  const size = usePref('editorFontSize')
  // One EditorState per open file. Recreating the view on every tab switch
  // would work, and would throw away the undo history and cursor position of
  // the tab being left — which is exactly what tabs are for.
  const states = useRef(new Map<string, EditorState>())
  const shown = useRef(path)
  // Builds a state carrying the full extension set. Held in a ref because the
  // tab-switch effect needs it and must not rebuild the update listener, whose
  // identity is what keeps the extension set stable across states.
  const factory = useRef<(doc: string) => EditorState>(null!)

  // Callbacks go through refs so a re-render does not tear down the editor.
  const cb = useRef({ onChange, onSave, onCursor })
  cb.current = { onChange, onSave, onCursor }

  useEffect(() => {
    const listen = EditorView.updateListener.of((u) => {
      if (u.docChanged) cb.current.onChange(u.state.doc.toString())
      if (u.selectionSet || u.docChanged) {
        const head = u.state.selection.main.head
        const line = u.state.doc.lineAt(head)
        cb.current.onCursor?.(line.number, head - line.from + 1)
      }
    })
    const make = (doc: string) =>
      EditorState.create({
        doc,
        extensions: [
          ...baseExtensions(
            () => cb.current.onSave(),
            language.current,
            fontSize.current,
          ),
          listen,
        ],
      })

    const state = make(value)
    states.current.set(shown.current, state)
    const v = new EditorView({ state, parent: host.current! })
    view.current = v
    v.focus()

    factory.current = make

    return () => {
      v.destroy()
      view.current = null
      states.current.clear()
    }
    // Mounted once for the life of the pane; `value` and `path` are the
    // starting document, and every later change is handled by the effects below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Tab switch: park the outgoing state, restore or build the incoming one.
  useEffect(() => {
    const v = view.current
    if (!v || shown.current === path) return
    states.current.set(shown.current, v.state)
    shown.current = path
    v.setState(states.current.get(path) ?? factory.current(value))
    v.focus()
    // `value` is read for a tab being opened for the first time; a change to it
    // alone is the sync effect's business, not this one's.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path])

  // The document can also change from outside — reverting, or rereading a file
  // somebody else edited. Anything the user typed already matches, so this is a
  // no-op in the common case rather than a fight with the editor.
  useEffect(() => {
    const v = view.current
    if (!v || shown.current !== path) return
    const current = v.state.doc.toString()
    if (current === value) return
    v.dispatch({
      changes: { from: 0, to: v.state.doc.length, insert: value },
      // A wholesale replacement must not leave the cursor pointing past the end.
      selection: { anchor: Math.min(v.state.selection.main.anchor, value.length) },
    })
  }, [value, path])

  // Zoom. Reconfiguring a compartment keeps the document, history and cursor,
  // and the dispatch is what makes CodeMirror remeasure the character grid —
  // without which the cursor lands a few columns off from the glyphs.
  useEffect(() => {
    const v = view.current
    if (!v) return
    v.dispatch({ effects: fontSize.current.reconfigure(fontTheme(size)) })
    v.requestMeasure()
  }, [size])

  // The grammar arrives after the file does: the editor opens as plain text and
  // gains highlighting a moment later, rather than the file waiting on a chunk
  // download. Reconfiguring a compartment leaves the document and history alone.
  useEffect(() => {
    let live = true
    const lang = detectLanguage(path)
    if (!lang) {
      view.current?.dispatch({ effects: language.current.reconfigure([]) })
      return
    }
    lang
      .load()
      .then((ext) => {
        if (live && shown.current === path) {
          view.current?.dispatch({ effects: language.current.reconfigure(ext) })
        }
      })
      // A grammar that fails to load costs highlighting, not the file. There is
      // nothing useful to say about it and nothing for the user to do.
      .catch(() => {})
    return () => {
      live = false
    }
  }, [path])

  return <div className="cm-host" ref={host} />
}
