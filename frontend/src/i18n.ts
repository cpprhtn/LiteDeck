import { useSyncExternalStore } from 'react'
import { en } from './locale-en'

// UI text (§8).
//
// # Why the Korean text is the key
//
// `t('새 폴더')` rather than `t('files.newFolder')`. The alternative means
// inventing four hundred identifiers and reading the catalogue to know what a
// button says. Keying by the source text keeps every call site legible, makes
// adding a language a matter of one file, and makes a missing translation fall
// back to Korean — the right answer for this project's primary audience, and a
// far better one than a bare key on screen.
//
// The cost is that editing the Korean silently orphans its translation. The
// coverage test closes that: it reads every t() call out of the source and
// fails on one the English catalogue does not know.
//
// # Why not a library
//
// react-i18next brings plurals for languages that need them, lazy namespaces
// and a backend loader. Korean has no plural agreement, both catalogues are
// small enough to ship whole, and there is no backend. What is left is a
// dictionary lookup and a substitution — which is this file.

export type Language = 'ko' | 'en'

export const LANGUAGES: { id: Language; label: string; title: string }[] = [
  // Two letters, because this sits beside the version string in a narrow
  // sidebar and anything longer crowds it. The full name is in the tooltip,
  // each written in its own language — somebody who cannot read the current UI
  // is exactly the person looking for this control.
  { id: 'ko', label: 'KO', title: '한국어' },
  { id: 'en', label: 'EN', title: 'English' },
]

/** Values substituted into a message. */
export type Vars = Record<string, string | number>

let current: Language = 'ko'
const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

export function setLanguage(lang: Language) {
  if (lang === current) return
  current = lang
  // The whole UI re-renders. Language is not a per-component concern and
  // threading it through props would put it in every signature in the app.
  emit()
}

export function getLanguage(): Language {
  return current
}

/**
 * Applies a stored preference, resolving "follow the OS" if there is none.
 *
 * `navigator.language` first: it is the UI language the user actually sees in
 * this window. On macOS that answer is only honest because the bundle declares
 * CFBundleLocalizations — without it the system hands an unlocalised app its
 * development region, and a Korean machine would report English.
 *
 * `system` is what Go read out of the environment (LANG and friends), used
 * where the webview has nothing to say. Anything unrecognised is Korean: this
 * project's source language, and the safer default for a tag nobody expected.
 */
export function initLanguage(stored: string, system = '') {
  setLanguage(resolve(stored || navigator.language || system || 'ko'))
}

/** Only the primary subtag matters: en-US, en_GB and en are one language. */
export function resolve(tag: string): Language {
  return tag.toLowerCase().split(/[-_.]/)[0] === 'en' ? 'en' : 'ko'
}

/**
 * Subscribes a component to language changes.
 *
 * Components call this rather than importing `t` alone, because a plain
 * function call gives React nothing to re-render on.
 */
export function useT(): typeof t {
  useSyncExternalStore(
    (fn) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    () => current,
  )
  return t
}

/**
 * Translates, then substitutes `{name}` placeholders.
 *
 * Named rather than positional so a translation can reorder them, which English
 * and Korean routinely need to do — Korean puts the counted noun after the
 * number and English does not.
 */
export function t(text: string, vars?: Vars): string {
  const template = current === 'en' ? (en[text] ?? text) : text
  if (!vars) return template
  return template
    .replace(PLURAL, (whole: string, name: string, one: string, many: string) =>
      name in vars ? (Number(vars[name]) === 1 ? one : many) : whole,
    )
    .replace(/\{(\w+)\}/g, (whole: string, name: string) =>
      name in vars ? String(vars[name]) : whole,
    )
}

/**
 * `{n#file|files}` — the singular when n is 1, the plural otherwise.
 *
 * Korean does not agree in number and English does, so a template written once
 * cannot serve both: "1 files have unsaved changes" is wrong in a way a user
 * notices immediately. This is the whole of the plural machinery — no CLDR
 * categories, because the two languages shipped need exactly two forms, and a
 * language that needs more (Russian has three) can have the rule then.
 */
const PLURAL = /\{(\w+)#([^|{}]*)\|([^{}]*)\}/g

/**
 * Marks a Korean string that is translated later, where it is shown.
 *
 * Label tables — tab names, connection states, transfer statuses — are module
 * constants, evaluated once at import. Calling `t()` there would freeze the
 * text at whatever the language was on startup, so the table stores Korean and
 * the render site calls `t()`. That leaves a bare Korean literal in the source,
 * which is indistinguishable from one somebody forgot to wrap. This says which
 * it is: a no-op at runtime, and the marker both coverage tests look for.
 */
export function k(text: string): string {
  return text
}

/** Keys the English catalogue does not cover. Used by the coverage test. */
export function missingFrom(keys: string[]): string[] {
  return keys.filter((k) => !(k in en))
}
