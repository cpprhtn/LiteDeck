import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorView } from '@codemirror/view'
import { tags as t } from '@lezer/highlight'
import type { Extension } from '@codemirror/state'

// The editor's appearance, expressed in the app's design tokens (§4.7-1).
//
// Every colour is a `var(--…)` rather than a resolved value, which means the
// editor follows the OS switching between light and dark without anyone
// listening for it — the same rule the rest of the UI already follows. It also
// means a future theme is a change to tokens.css and nothing else.

const theme = EditorView.theme({
  '&': {
    height: '100%',
    color: 'var(--fg)',
    backgroundColor: 'var(--bg)',
    // No fontSize here: it is adjustable, so it lives in its own compartment in
    // CodeEditor.tsx. Setting it in both places makes whichever theme
    // CodeMirror injects last the winner, which is not a thing to leave to luck.
  },
  '.cm-scroller': {
    fontFamily: 'var(--font-mono)',
    lineHeight: '1.55',
    overflow: 'auto',
  },
  '.cm-content': { caretColor: 'var(--accent)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--accent)' },
  // CodeMirror paints the selection itself when the editor has focus, and hands
  // it back to the browser when it does not — both need saying, or the
  // selection vanishes the moment the user clicks the save button.
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection':
    { backgroundColor: 'var(--bg-selected)' },
  '.cm-gutters': {
    backgroundColor: 'var(--bg-sunken)',
    color: 'var(--fg-faint)',
    borderRight: '1px solid var(--border)',
  },
  '.cm-activeLineGutter': { backgroundColor: 'var(--bg-hover)', color: 'var(--fg-muted)' },
  '.cm-activeLine': { backgroundColor: 'var(--bg-hover)' },
  '.cm-foldPlaceholder': {
    backgroundColor: 'var(--bg-sunken)',
    border: '1px solid var(--border-strong)',
    color: 'var(--fg-muted)',
  },
  '.cm-matchingBracket, .cm-nonmatchingBracket': {
    backgroundColor: 'var(--bg-selected)',
    outline: '1px solid var(--border-strong)',
  },
  '.cm-searchMatch': {
    backgroundColor: 'color-mix(in srgb, var(--warn) 30%, transparent)',
  },
  '.cm-searchMatch.cm-searchMatch-selected': {
    backgroundColor: 'color-mix(in srgb, var(--accent) 45%, transparent)',
  },
  // The find/replace bar. Left to itself it arrives with browser-default inputs
  // and buttons in the middle of a themed window.
  '.cm-panels': {
    backgroundColor: 'var(--bg-raised)',
    color: 'var(--fg)',
    fontFamily: 'var(--font-ui)',
    fontSize: 'var(--text-sm)',
  },
  '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--border-strong)' },
  '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--border-strong)' },
  '.cm-panel.cm-search': { padding: 'var(--sp-2)' },
  '.cm-panel.cm-search input, .cm-panel.cm-search button': {
    fontFamily: 'inherit',
    fontSize: 'inherit',
    color: 'var(--fg)',
    backgroundColor: 'var(--bg)',
    border: '1px solid var(--border-strong)',
    borderRadius: 'var(--radius)',
    padding: '2px 6px',
  },
  '.cm-panel.cm-search label': { color: 'var(--fg-muted)' },
  '.cm-panel.cm-search input[type=checkbox]': { padding: 0 },
  '.cm-tooltip': {
    backgroundColor: 'var(--bg-raised)',
    border: '1px solid var(--border-strong)',
    borderRadius: 'var(--radius)',
    color: 'var(--fg)',
  },
})

const highlight = HighlightStyle.define([
  { tag: [t.comment, t.lineComment, t.blockComment, t.docComment], color: 'var(--syn-comment)', fontStyle: 'italic' },
  { tag: [t.keyword, t.controlKeyword, t.moduleKeyword, t.modifier, t.self, t.null], color: 'var(--syn-keyword)' },
  { tag: [t.operator, t.operatorKeyword, t.derefOperator], color: 'var(--syn-operator)' },
  { tag: [t.string, t.special(t.string), t.regexp, t.escape], color: 'var(--syn-string)' },
  { tag: [t.number, t.bool, t.atom, t.literal, t.unit], color: 'var(--syn-number)' },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.macroName], color: 'var(--syn-function)' },
  { tag: [t.definition(t.variableName), t.definition(t.propertyName)], color: 'var(--syn-def)' },
  { tag: [t.typeName, t.className, t.namespace, t.standard(t.typeName)], color: 'var(--syn-type)' },
  // Property names carry INI, YAML, JSON and systemd units — the four formats
  // most likely to be open — so they get their own colour rather than the
  // default foreground.
  { tag: [t.propertyName, t.attributeName, t.labelName], color: 'var(--syn-property)' },
  { tag: [t.variableName, t.tagName], color: 'var(--syn-variable)' },
  { tag: [t.meta, t.processingInstruction, t.annotation, t.documentMeta], color: 'var(--syn-meta)' },
  { tag: [t.heading, t.strong], color: 'var(--syn-heading)', fontWeight: '600' },
  { tag: t.emphasis, fontStyle: 'italic' },
  { tag: [t.link, t.url], color: 'var(--syn-link)', textDecoration: 'underline' },
  { tag: t.strikethrough, textDecoration: 'line-through' },
  { tag: [t.punctuation, t.separator, t.bracket], color: 'var(--syn-punct)' },
  { tag: [t.inserted], color: 'var(--ok)' },
  { tag: [t.deleted], color: 'var(--danger)' },
  { tag: t.invalid, color: 'var(--danger)' },
])

export const editorTheme: Extension = [theme, syntaxHighlighting(highlight)]
