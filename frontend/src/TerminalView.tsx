import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Terminal, type ILink } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import {
  CloseTerminal,
  ListTerminals,
  HostShells,
  OpenTerminal,
  ReadClipboard,
  ReadTextFile,
  ResizeTerminal,
  RevealFromTerminal,
  TerminalCwd,
  TypedEntered,
  WriteTerminal,
  on,
  type Shell,
  type TerminalInfo,
} from './ipc'
import { CommandHistory } from './CommandHistory'
import { ResizeHandle } from './ResizeHandle'
import { getPref, setPref, usePref } from './prefs'
import { LineWatcher, isTerminalReply } from './lineWatcher'
import { isOpen, openFile, requestReveal } from './openFiles'
import { getPlatform } from './platform'
import { t } from './i18n'
import { onThemeChange } from './theme'
import { isWebMode } from './webTransport'
import { TERM_THEMES, termTheme } from './termThemes'
import { onTermAction, type TermAction } from './termCommands'
import { historyLoaded, loadHistory, rememberLatest, suggestions } from './termSuggest'
import {
  countPanes,
  fill,
  geometry,
  loadShapes,
  neighbour,
  panes,
  prune,
  removePane,
  saveShapes,
  setRatio,
  splitPane,
  type Dir,
  type TermTab,
  type Way,
} from './termLayout'

// The built-in terminal (§4.6).
//
// Secondary to the GUI by design — it is here for the things a GUI cannot
// express, and for the moment someone wants to check the app's work by hand.
// The Command Log is the bridge in the other direction.
//
// Output crosses the boundary as base64: terminal bytes are not text, and JSON
// string encoding would turn partial UTF-8 at a chunk boundary into replacement
// characters that never repair themselves.
//
// # Splits
//
// A tab can be split into panes, side by side or one above the other. Each pane
// is one more shell channel on the SSH connection that is already open — never a
// second login — so they share the host's budget of long-lived channels with
// log follows, and run out together. A pane's shell ends when the pane closes or
// the connection drops: nothing is left running on the server, which is the
// difference between this and tmux. What survives a restart is the arrangement,
// filled with fresh shells.

const enc = new TextEncoder()

function b64encode(data: string): string {
  const bytes = enc.encode(data)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin)
}

/** One decoder per pane.
 *
 *  `stream: true` holds the tail of a chunk that ended mid-character until the
 *  next call completes it — which is exactly right for one stream and wrong for
 *  several sharing a decoder. Two tabs used to hand their bytes to the same
 *  one, so half a Hangul syllable from a `tail -f` came back glued to the front
 *  of the next line of a build in the other tab. Both tabs showed a broken
 *  character that never repaired itself. */
function b64decode(dec: TextDecoder, data: string): string {
  const bin = atob(data)
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  return dec.decode(bytes, { stream: true })
}

/** Read the clipboard, whichever side can actually do it.
 *
 *  On the desktop the read detours through Go: WKWebView refuses
 *  `navigator.clipboard.readText()` outright, so on macOS the paste would
 *  silently do nothing. Writing is allowed in the same webview, which is why
 *  only this half detours.
 *
 *  In server mode there is no webview behind that call and Go answers "server
 *  mode cannot read the clipboard", so all three paste paths failed every time.
 *  A browser over HTTPS or localhost can read its own clipboard, which is the
 *  right answer there — the clipboard that matters is the one at the keyboard,
 *  not the one on the server. */
function readClipboard(): Promise<string> {
  if (isWebMode()) return navigator.clipboard.readText()
  return ReadClipboard()
}

/** Opens a link in the system browser. Wails' own runtime call on the desktop —
 *  a window.open inside the webview would open a second, chromeless app window
 *  — and an ordinary new tab in server mode. */
function openExternal(url: string) {
  const rt = (window as unknown as { runtime?: { BrowserOpenURL?: (u: string) => void } }).runtime
  if (!isWebMode() && rt?.BrowserOpenURL) rt.BrowserOpenURL(url)
  else window.open(url, '_blank', 'noopener')
}

// What a click can follow in terminal output. URLs, and paths that are
// unambiguous without context: absolute, or from home. A bare `foo.txt` is a
// word as often as a file, and underlining every word on the screen is noise.
const URL_RE = /\bhttps?:\/\/[^\s"'<>`)\]]+[^\s"'<>`)\].,;:!?]/g
const PATH_RE = /(?<![\w/~])(?:~|\/)[\w.@+\-/]*[\w@+\-/]/g

/** The modifier a link click needs — ⌘ on macOS, Ctrl elsewhere. A plain click
 *  is how somebody focuses a pane or starts a selection, and following a path
 *  every time they did that would be the terminal fighting its user. */
function linkModifier(e: MouseEvent): boolean {
  return getPlatform().isMac ? e.metaKey : e.ctrlKey
}

/** The keyboard shortcuts, by platform convention: iTerm2's on macOS, Windows
 *  Terminal's everywhere else. */
// The editor `vi` opens in place. Lazy: CodeMirror is most of a megabyte, and
// a terminal nobody types `vi` into should not carry it.
const EditorPane = lazy(() => import('./EditorPane').then((m) => ({ default: m.EditorPane })))

/**
 * Reads back the line the shell drew, from where typing began to the end of the
 * line the cursor is on.
 *
 * Only certain answers. Every row after the first has to be a soft wrap of the
 * one above: a row that is not means the shell printed something of its own —
 * a list of Tab candidates, a redrawn prompt — and the line can no longer be
 * found by position. Then this says null, and the line goes to the shell as it
 * always did.
 *
 * Read to the end of the row rather than to the cursor, because an arrow key
 * can leave the cursor in the middle of the line and the shell runs all of it.
 * What sits to the right of the cursor past a run of spaces is a right-hand
 * prompt (zsh's RPROMPT), not part of the command.
 */
function readBack(term: Terminal, start: { x: number; y: number } | null): string | null {
  if (!start) return null
  const b = term.buffer.active
  const cursorY = b.baseY + b.cursorY
  if (cursorY < start.y || cursorY - start.y > 40) return null
  let text = ''
  let cursorAt = 0
  for (let y = start.y; y <= cursorY; y++) {
    const line = b.getLine(y)
    if (!line) return null
    if (y > start.y && !line.isWrapped) return null
    const from = y === start.y ? start.x : 0
    if (y === cursorY) cursorAt = text.length + Math.max(0, b.cursorX - from)
    text += line.translateToString(false, from)
  }
  // Rows that continue onto the next wrapped row past the cursor.
  for (let y = cursorY + 1; ; y++) {
    const line = b.getLine(y)
    if (!line || !line.isWrapped) break
    text += line.translateToString(false)
  }
  const tail = text.slice(cursorAt)
  const gap = tail.search(/\s{2,}\S/)
  const line = (gap >= 0 ? text.slice(0, cursorAt + gap) : text).replace(/\s+$/, '')
  return line || null
}

function shortcut(e: KeyboardEvent): TermAction | null {
  if (e.type !== 'keydown') return null
  if (getPlatform().isMac) {
    if (!e.metaKey || e.ctrlKey) return null
    if (e.altKey) {
      if (e.key === 'ArrowLeft') return 'focus-left'
      if (e.key === 'ArrowRight') return 'focus-right'
      if (e.key === 'ArrowUp') return 'focus-up'
      if (e.key === 'ArrowDown') return 'focus-down'
      return null
    }
    const key = e.key.toLowerCase()
    if (key === 'd') return e.shiftKey ? 'split-down' : 'split-right'
    if (key === 'w' && !e.shiftKey) return 'close-pane'
    if (key === 't' && !e.shiftKey) return 'new-tab'
    if (e.code === 'Equal') return 'font-bigger'
    if (e.code === 'Minus') return 'font-smaller'
    if (e.code === 'Digit0') return 'font-reset'
    return null
  }
  if (e.altKey && !e.ctrlKey && !e.metaKey) {
    if (e.shiftKey && e.code === 'Equal') return 'split-right'
    if (e.shiftKey && e.code === 'Minus') return 'split-down'
    if (!e.shiftKey) {
      if (e.key === 'ArrowLeft') return 'focus-left'
      if (e.key === 'ArrowRight') return 'focus-right'
      if (e.key === 'ArrowUp') return 'focus-up'
      if (e.key === 'ArrowDown') return 'focus-down'
    }
    return null
  }
  if (e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey) {
    const key = e.key.toLowerCase()
    if (key === 'w') return 'close-pane'
    if (key === 't') return 'new-tab'
  }
  return null
}

/** The shortcut for an action, for tooltips. */
export function termKeyLabel(a: TermAction): string {
  const mac = getPlatform().isMac
  const m: Partial<Record<TermAction, [string, string]>> = {
    'split-right': ['⌘D', 'Alt+Shift+='],
    'split-down': ['⌘⇧D', 'Alt+Shift+-'],
    'close-pane': ['⌘W', 'Ctrl+Shift+W'],
    'new-tab': ['⌘T', 'Ctrl+Shift+T'],
    'focus-left': ['⌘⌥←', 'Alt+←'],
    'focus-right': ['⌘⌥→', 'Alt+→'],
  }
  const pair = m[a]
  return pair ? (mac ? pair[0] : pair[1]) : ''
}

function TerminalPane({
  info,
  focused,
  onClosed,
  onError,
  onCommand,
  onAction,
  onFocused,
  posix,
}: {
  info: TerminalInfo
  /** The shell edits lines the POSIX way (readline, zle, fish). cmd.exe and
   *  PowerShell do not, and a blind line is never read back for them — the
   *  keys that clear a line there are not the same keys. */
  posix: boolean
  /** This pane is the one the keyboard goes to, on the tab that is on screen. */
  focused: boolean
  onClosed: () => void
  onError: (msg: string) => void
  onCommand: (command: string, arg: string, clear?: string) => void
  /** A split, close or move shortcut pressed while this pane had the keyboard. */
  onAction: (a: TermAction) => void
  /** The pane took the keyboard — a click, or the focus moved here. */
  onFocused: () => void
}) {
  const posixRef = useRef(posix)
  posixRef.current = posix
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  // Through refs: the terminal is built once, and rebuilding it whenever the
  // parent re-renders would throw away the session's scrollback.
  //
  // All the callbacks and not just onCommand — a tab switch is a re-render
  // now that the view stays mounted, and every render of the parent hands this
  // pane a fresh arrow function. Leaving one of them in the dependency list
  // below was enough to dispose the terminal four times per round trip and
  // bring it back empty.
  const command = useRef(onCommand)
  command.current = onCommand
  const closed = useRef(onClosed)
  closed.current = onClosed
  const failed = useRef(onError)
  failed.current = onError
  const acted = useRef(onAction)
  acted.current = onAction
  const focusedRef = useRef(onFocused)
  focusedRef.current = onFocused

  const fontSize = usePref('termFontSize')
  const lineHeight = usePref('termLineHeight')
  const fontFamily = usePref('termFontFamily')
  const ligatures = usePref('termLigatures')
  const copyOnSelect = usePref('termCopyOnSelect')
  const themeId = usePref('termTheme')
  const copyRef = useRef(copyOnSelect)
  copyRef.current = copyOnSelect
  const suggestHistory = usePref('termSuggestHistory')
  const suggestCommands = usePref('termSuggestCommands')
  const suggestRef = useRef({ history: suggestHistory, commands: suggestCommands })
  suggestRef.current = { history: suggestHistory, commands: suggestCommands }
  /** Works the suggestion out again — set by the effect below, called when a
   *  setting changes so switching a source off takes its dim text with it. */
  const rehint = useRef<() => void>(() => {})

  useEffect(() => {
    if (!hostRef.current) return

    const appMono = getComputedStyle(document.documentElement).getPropertyValue('--font-mono').trim()
    const term = new Terminal({
      fontFamily: getPref('termFontFamily') || appMono,
      fontSize: getPref('termFontSize'),
      lineHeight: getPref('termLineHeight') / 100,
      cursorBlink: true,
      scrollback: 5000,
      theme: termTheme(getPref('termTheme')),
      // macOS convention: ⌥ composes characters rather than sending Meta.
      macOptionIsMeta: false,
    })
    // The palette is read once at construction, so a terminal opened in
    // daylight stayed light after the theme switched to dark while CodeMirror
    // and the rest of the UI changed underneath it. The tokens are CSS
    // variables; when the theme flips they resolve to new values and the
    // terminal has to be told.
    //
    // Through theme.ts rather than matchMedia, because the OS is no longer the
    // only thing that can change the answer — the picker in the rail footer can
    // too, and a media query never hears about that.
    // The pane's padding is painted too. With "app" the terminal's background
    // is the window's and nobody would notice, but a fixed palette left a frame
    // of the app's own colour around every terminal.
    const paint = (theme = termTheme(getPref('termTheme'))) => {
      term.options.theme = theme
      if (hostRef.current) hostRef.current.style.background = theme.background ?? ''
    }
    const offTheme = onThemeChange(() => paint())

    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(hostRef.current)
    paint()
    fit.fit()

    termRef.current = term
    fitRef.current = fit

    // The suggestions: likely whole lines, listed just above the prompt.
    //
    // An element over the terminal rather than text written into it. Writing
    // it would put characters on the screen the shell does not know about, and
    // the next thing the shell drew would land on top of them or behind them.
    // The list is only ever a picture; what → sends is the rest of the
    // picked line, typed, and the shell echoes it like anything else.
    const list = document.createElement('div')
    list.className = 'term-suggest'
    list.hidden = true
    hostRef.current.appendChild(list)
    /** The lines on offer, the line they were worked out for, and the pick. */
    let hints: string[] = []
    let hintFor = ''
    let picked = 0
    /** Esc put the list away for this line; typing anything brings it back. */
    let dismissedFor: string | null = null
    /** On screen right now — the only time a key may take it. */
    let shown = false

    // Copy, paste, and the pane shortcuts, which the terminal has to bind for
    // itself.
    //
    // Everywhere else in the window the OS provides copy and paste — the macOS
    // Edit menu (menu_darwin.go), or WebView2 and WebKitGTK's own bindings. Here
    // it cannot: Ctrl+C is SIGINT and must stay SIGINT, and xterm claims every
    // keystroke before the browser sees it.
    //
    // So the platform's terminal convention rather than its text-field one:
    // ⌘C/⌘V on macOS, where ⌘ collides with nothing, and Ctrl+Shift+C/V
    // everywhere else — what GNOME Terminal, Konsole and Windows Terminal all
    // use, and for the same reason.
    term.attachCustomKeyEventHandler((e) => {
      const action = shortcut(e)
      if (action) {
        e.preventDefault()
        acted.current(action)
        return false
      }
      if (e.type !== 'keydown') return true
      // The list's keys while it is showing, and only then: with no list on
      // the screen they are the shell's keys as always. ↑ ↓ pick only when
      // there is something to pick between — with one line on offer, ↑ is
      // still the shell's history.
      //
      // Not Tab. Tab is the shell's completion, and with history on there is a
      // list up for nearly every prefix: taking it there turned `cd /us` into
      // a whole remembered line instead of `cd /usr/`. → ends the line in every
      // shell, so taking it costs nothing the shell would have done.
      if (shown && hints.length && !e.ctrlKey && !e.altKey && !e.metaKey && !e.shiftKey && !e.isComposing) {
        if (e.key === 'ArrowRight') {
          e.preventDefault()
          take(picked)
          return false
        }
        if ((e.key === 'ArrowUp' || e.key === 'ArrowDown') && hints.length > 1) {
          e.preventDefault()
          const step = e.key === 'ArrowUp' ? -1 : 1
          picked = (picked + step + hints.length) % hints.length
          draw()
          return false
        }
        if (e.key === 'Escape') {
          e.preventDefault()
          dismissedFor = hintFor
          hints = []
          place()
          return false
        }
      }
      const mac = getPlatform().isMac
      const combo = mac
        ? e.metaKey && !e.ctrlKey && !e.altKey
        : e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey
      if (!combo) return true

      const key = e.key.toLowerCase()
      // The command palette's own key. It is caught at the window before this
      // ever runs; returning false here only keeps xterm from also sending it.
      if (key === 'p' && (mac ? e.shiftKey : true)) return false
      if (key !== 'c' && key !== 'v') return true
      // preventDefault stops the macOS Edit menu firing the same action a
      // second time: WKWebView reports the key equivalent as handled when the
      // page consumes it, which is what lets a web app override ⌘C at all.
      e.preventDefault()

      if (key === 'c') {
        const sel = term.getSelection()
        if (sel) void navigator.clipboard.writeText(sel).catch(() => {})
        return false
      }
      readClipboard()
        // term.paste rather than WriteTerminal: it wraps the text in the
        // bracketed-paste markers when the far side asked for them, which is
        // what stops a pasted block from being run a line at a time, and
        // normalises CRLF to CR. It reaches the server through onData below,
        // so the Command Log and the `code`/`vi` watcher still see it.
        .then((text) => text && term.paste(text))
        .catch(() => failed.current(t('클립보드를 읽지 못했습니다.')))
      return false
    })

    // Copy on select, for the people whose hands learned X11: the selection is
    // the clipboard. Off by default, because everywhere else a selection is
    // just a selection, and silently replacing the clipboard is a surprise.
    const offSelect = term.onSelectionChange(() => {
      if (!copyRef.current) return
      const sel = term.getSelection()
      if (sel) void navigator.clipboard.writeText(sel).catch(() => {})
    })

    // Links: URLs to the browser, paths to the editor. Both behind the platform
    // modifier, as in every other terminal people use.
    const links = term.registerLinkProvider({
      provideLinks(y, callback) {
        const line = term.buffer.active.getLine(y - 1)
        if (!line) return callback(undefined)
        const text = line.translateToString(true)
        const found: ILink[] = []
        const add = (re: RegExp, act: (s: string) => void) => {
          re.lastIndex = 0
          for (let m = re.exec(text); m; m = re.exec(text)) {
            const s = m[0]
            const x = m.index + 1
            // A path inside a URL is part of the URL.
            if (found.some((l) => l.range.start.x <= x && x <= l.range.end.x)) continue
            found.push({
              text: s,
              range: { start: { x, y }, end: { x: x + s.length - 1, y } },
              decorations: { underline: true, pointerCursor: true },
              activate: (e) => linkModifier(e) && act(s),
            })
          }
        }
        add(URL_RE, openExternal)
        add(PATH_RE, (p) => command.current('open', p))
        callback(found.length ? found : undefined)
      },
    })

    const dec = new TextDecoder()
    const offData = on<string>(`term:data:${info.id}`, (chunk) =>
      term.write(b64decode(dec, chunk)),
    )
    const offExit = on<string>(`term:exit:${info.id}`, (msg) => {
      // Flush whatever the decoder was holding. Without this a session that
      // ends mid-character drops the last one silently.
      const tail = dec.decode()
      if (tail) term.write(tail)
      term.write(`\r\n\x1b[2m— ${t('세션 종료')}${msg ? `: ${msg}` : ''} —\x1b[0m\r\n`)
      closed.current()
    })

    // `code` and `vi` are handled here and never sent (§4.6a).
    //
    // Catching them on this side rather than in the shell is what makes them
    // work at all on a server with no VS Code, no vi, and cmd.exe for a shell.
    // Nothing runs remotely, so nothing has to be installed, exported or
    // shadowed, and there is no version of this that opens an editor on the
    // server by accident.
    const typed = new LineWatcher()
    // Every line the user enters goes to the local history (T-22), including
    // the ones this side could not read — those carry no text and only cost the
    // tracked directory its confidence. Failures are swallowed: a history that
    // cannot be written is not a reason to break the terminal.
    typed.onEnter = ({ line, blind }) => {
      const entered = TypedEntered(info.hostId, info.id, line, blind).catch(() => {})
      // The line as Go hands it back, not as typed: Go masks what looks like a
      // password, and the suggestions must never offer one.
      if (!blind && line) void entered.then(() => rememberLatest(info.hostId))
    }
    // Keystrokes go to the server one write at a time, in order.
    //
    // Each keystroke used to be its own call, fired and forgotten. The calls
    // are independent requests, and nothing made the second wait for the
    // first: with four panes open and the screen busy, `cd /srv/api` reached
    // the shell as `cd /svr/api` and `pwd` as `pdw`. Now a write waits for the
    // one before it, and whatever is typed meanwhile rides along in the next —
    // which is also fewer calls for somebody typing fast.
    //
    // The promise each send returns settles once that input is on the wire, so
    // a caller can order something after it.
    let chain: Promise<void> = Promise.resolve()
    let queued = ''
    let scheduled = false
    const send = (data: string): Promise<void> => {
      queued += data
      if (!scheduled) {
        scheduled = true
        chain = chain.then(() => {
          scheduled = false
          const chunk = queued
          queued = ''
          return WriteTerminal(info.id, b64encode(chunk)).catch((e) => failed.current(String(e)))
        })
      }
      return chain
    }

    // Where on the screen the line being typed began, for reading it back.
    let lineStart: { x: number; y: number } | null = null

    /** Fills the list in, the picked line marked. */
    const draw = () => {
      list.replaceChildren(
        ...hints.map((line, n) => {
          const item = document.createElement('div')
          item.className = n === picked ? 'term-suggest-item picked' : 'term-suggest-item'
          const head = document.createElement('span')
          head.className = 'term-suggest-typed'
          head.textContent = hintFor
          const rest = document.createElement('span')
          rest.textContent = line.slice(hintFor.length)
          item.append(head, rest)
          // Down, not click: a click would take the focus off the terminal
          // first, and the keys after it would go nowhere.
          item.addEventListener('mousedown', (e) => {
            e.preventDefault()
            take(n)
          })
          return item
        }),
      )
    }
    /** Types the rest of the n-th line. As typing, so the watcher, the Command
     *  Log and the history see exactly what the shell does. */
    const take = (n: number) => {
      const line = hints[n]
      if (line === undefined) return
      const rest = line.slice(hintFor.length)
      hints = []
      place()
      if (rest) term.input(rest, true)
      term.focus()
    }

    /** Puts the list above the line being typed, or takes it away.
     *
     *  Shown only when the screen agrees with the keystrokes: the shell has
     *  echoed exactly what was typed. Until the echo of a far server arrives,
     *  while a password is typed blind, inside `less` or `vim`, or while the
     *  view is scrolled back, it stays away. */
    const place = () => {
      const b = term.buffer.active
      const screen = hostRef.current?.querySelector<HTMLElement>('.xterm-screen')
      const host = hostRef.current
      const ok =
        hints.length > 0 &&
        !!screen &&
        !!host &&
        !!lineStart &&
        b.type === 'normal' &&
        b.viewportY === b.baseY &&
        readBack(term, lineStart) === hintFor.trimEnd()
      if (!ok || !screen || !host || !lineStart) {
        if (shown) list.hidden = true
        shown = false
        return
      }
      const sr = screen.getBoundingClientRect()
      const hr = host.getBoundingClientRect()
      const cw = sr.width / term.cols
      const ch = sr.height / term.rows
      const s = list.style
      s.fontFamily = term.options.fontFamily ?? ''
      s.fontSize = `${term.options.fontSize}px`
      s.lineHeight = `${Math.round(ch)}px`
      if (!shown) draw()
      list.hidden = false
      shown = true
      // Under where the typing began when that is on this row; a line that
      // has wrapped starts the list at the left edge.
      const row = b.cursorY
      const startRow = lineStart.y - b.baseY
      const col = startRow === row ? lineStart.x : 0
      const top = sr.top - hr.top + row * ch
      const h = list.offsetHeight
      const w = list.offsetWidth
      // Above the line, where it covers output already read. Below when the
      // prompt sits too near the top for that.
      s.top = `${top - h >= 0 ? top - h - 2 : top + ch + 2}px`
      s.left = `${Math.max(0, Math.min(sr.left - hr.left + col * cw, hr.width - w - 4))}px`
    }
    /** Works out the suggestions for the line as it now stands. */
    const refresh = () => {
      const line = typed.line
      const want = suggestRef.current
      if (want.history && !historyLoaded(info.hostId)) {
        // The first keystroke on a host starts the read. When it lands, the
        // line still being typed gets the history it was missing.
        void loadHistory(info.hostId).then(() => {
          if (typed.line === hintFor && hintFor) refresh()
        })
      }
      if (line !== hintFor) dismissedFor = null
      hintFor = line ?? ''
      hints =
        line && line !== dismissedFor
          ? suggestions(info.hostId, line, {
              history: want.history,
              // The list is Linux's. cmd.exe and PowerShell have their own words.
              commands: want.commands && posixRef.current,
            })
          : []
      picked = 0
      // Drawn again on every change: same length is not same lines.
      if (shown) draw()
      place()
    }
    rehint.current = refresh
    // Every repaint, because that is when the echo lands and the cursor moves.
    const offRender = term.onRender(place)
    const disposeInput = term.onData((data) => {
      const buf = term.buffer.active
      const enter = data === '\r' || data === '\n'
      if (typed.fresh && !enter && !isTerminalReply(data)) {
        lineStart = { x: buf.cursorX, y: buf.baseY + buf.cursorY }
      }
      const caught = typed.feed(
        data,
        buf.type === 'normal',
        posixRef.current ? () => readBack(term, lineStart) : undefined,
      )
      const startedAt = lineStart
      if (enter) lineStart = null
      if (caught) {
        // The shell's line is emptied and the shell asked where it stands, in
        // one write from Go with the screen diverted — see CurrentDirAfter.
        // What empties it depends on how the line was made: typed keys are
        // undone with as many backspaces, which every shell understands
        // (cmd.exe takes Ctrl-U as a literal ^U); a line Tab completed holds
        // more than was typed, so it takes end-of-line and kill-line, the same
        // two keys in readline, zle and fish, and never sent to cmd.exe or
        // PowerShell — `posix` sees to that.
        //
        // The line itself stays on screen and the cursor goes to the next one,
        // as if the command had run; the shell's fresh prompt lands there when
        // its answer comes back. Counted in code points: an emoji is two
        // UTF-16 units and one backspace.
        const clear = caught.fromScreen ? '\x05\x15' : '\b'.repeat([...caught.line].length)
        const c = caught
        const start = startedAt
        // After every keystroke before it has reached the shell, and after the
        // shell has drawn the line back: on a server far away the echo of the
        // last few keys is still on its way, and once the screen is diverted
        // it would never arrive — the line looked empty instead of showing
        // what was run. Half a second at most; a line that never shows up is
        // not a reason to keep anybody waiting.
        hints = []
        place()
        void chain.then(async () => {
          const want = c.line.trim()
          for (let i = 0; i < 25; i++) {
            const seen = readBack(term, start)
            if (seen !== null && seen.trim() === want) break
            await new Promise((r) => setTimeout(r, 20))
          }
          term.write('\r\n')
          command.current(c.command, c.arg, clear)
        })
        return
      }
      void send(data)
      refresh()
    })

    // The remote side needs to know the size or full-screen programs draw at
    // 80x24 regardless of the window.
    //
    // Never measured from a pane that is not laid out, though. FitAddon reads
    // the parent's computed style, and a display:none element answers with the
    // stylesheet's "100%" instead of a pixel count — 100px, which worked out to
    // a 10x6 terminal. That size went to the real PTY, the shell redrew its
    // prompt for a 10-column screen, and the screenful of output that was there
    // did not survive the trip back. Hiding a tab is not a resize.
    //
    // Fit locally on every observation, tell the server once the dragging
    // stops — 100ms after the last change. A window drag fires the observer per
    // frame, and each one was an RPC and a real SIGWINCH on the far side — a
    // shell redrawing its prompt sixty times a second while somebody resizes the
    // window, or drags the divider between two panes.
    let resizeTimer = 0
    let sent = ''
    const resize = () => {
      const el = hostRef.current
      if (!el || el.clientWidth === 0 || el.clientHeight === 0) return
      try {
        fit.fit()
      } catch {
        /* the pane is not visible yet */
        return
      }
      const cols = term.cols
      const rows = term.rows
      if (resizeTimer) clearTimeout(resizeTimer)
      resizeTimer = window.setTimeout(() => {
        resizeTimer = 0
        // Once per size, not once per settle: a fit that lands on the size the
        // server already has is not worth a SIGWINCH.
        const key = `${cols}x${rows}`
        if (key === sent) return
        sent = key
        void ResizeTerminal(info.id, cols, rows).catch(() => {})
      }, 100)
    }
    const observer = new ResizeObserver(resize)
    observer.observe(hostRef.current)
    resize()
    term.focus()

    const host = hostRef.current
    const took = () => focusedRef.current()
    host.addEventListener('focusin', took)

    return () => {
      host.removeEventListener('focusin', took)
      observer.disconnect()
      if (resizeTimer) clearTimeout(resizeTimer)
      disposeInput.dispose()
      offRender.dispose()
      list.remove()
      rehint.current = () => {}
      offSelect.dispose()
      links.dispose()
      offTheme()
      offData()
      offExit()
      term.dispose()
      termRef.current = null
    }
    // The session id alone: see the refs above.
  }, [info.id])

  // Font, theme and line height follow the preferences while the pane is open.
  // Changing them refits, because the cell size moved and with it how many
  // columns fit — which the server has to hear about, or `ls` wraps at the old
  // width.
  useEffect(() => {
    const term = termRef.current
    if (!term) return
    const appMono = getComputedStyle(document.documentElement).getPropertyValue('--font-mono').trim()
    term.options.fontSize = fontSize
    term.options.lineHeight = lineHeight / 100
    term.options.fontFamily = fontFamily || appMono
    const theme = termTheme(themeId)
    term.options.theme = theme
    if (hostRef.current) hostRef.current.style.background = theme.background ?? ''
    try {
      fitRef.current?.fit()
    } catch {
      /* not laid out yet; the observer will fit it when it is */
    }
  }, [fontSize, lineHeight, fontFamily, themeId])

  useEffect(() => {
    rehint.current()
  }, [suggestHistory, suggestCommands])

  // The keyboard follows the pane. Mounting used to do this once, at the end of
  // the effect above, which was enough back when coming back to this tab meant
  // mounting again. Now that the pane survives, nothing re-aimed the keyboard —
  // the terminal looked ready, kept its scrollback, and swallowed the first
  // thing typed at it because the focus was still on the tab button.
  useEffect(() => {
    if (focused) termRef.current?.focus()
  }, [focused])

  return (
    <div
      className="term-pane"
      data-ligatures={ligatures || undefined}
      ref={hostRef}
      onContextMenu={(e) => {
        // The convention two Windows users reported missing. PuTTY, PowerShell
        // and Windows Terminal all do this: a right click copies when there is
        // a selection and pastes when there is not, so one button covers both
        // without a menu.
        //
        // Not on macOS. Right click is the context menu there, and taking it
        // over would break the platform's own habit to fix somebody else's.
        if (getPlatform().isMac) return
        const term = termRef.current
        if (!term) return
        e.preventDefault()
        const sel = term.getSelection()
        if (sel) {
          void navigator.clipboard.writeText(sel).catch(() => {})
          term.clearSelection()
          return
        }
        // Same source as the keyboard paste — see readClipboard.
        readClipboard()
          .then((text) => text && term.paste(text))
          .catch(() => failed.current(t('클립보드를 읽지 못했습니다.')))
      }}
    />
  )
}

/** Live layouts by host, for as long as the app runs.
 *
 *  The view unmounts when somebody switches hosts and mounts again when they
 *  come back, and Go still holds the sessions. Without this, the sessions came
 *  back as one tab each and the arrangement was lost every time. */
const live = new Map<string, { tabs: TermTab[]; active: string | null }>()

/** The shell each pane was opened with, so a split of a PowerShell pane is
 *  another PowerShell and not whatever sshd hands out by default. Module-level
 *  for the same reason as `live`: the sessions outlive the view. */
const shellOfTerm = new Map<string, string | undefined>()

let tabSeq = 0
const newKey = () => `tab${++tabSeq}`

/** Turns a split's divider drag into a ratio, from the pointer's position in
 *  the split's own box. */
function DividerHandle({
  dir,
  style,
  onRatio,
  boxOf,
}: {
  dir: Dir
  style: React.CSSProperties
  onRatio: (r: number) => void
  /** The split's box in pixels, measured when the drag starts. */
  boxOf: () => DOMRect | null
}) {
  return (
    <div
      className="term-divider"
      data-dir={dir}
      style={style}
      role="separator"
      aria-orientation={dir === 'row' ? 'vertical' : 'horizontal'}
      onPointerDown={(e) => {
        const box = boxOf()
        if (!box) return
        e.preventDefault()
        const el = e.currentTarget
        el.setPointerCapture(e.pointerId)
        const move = (ev: PointerEvent) =>
          onRatio(dir === 'row' ? (ev.clientX - box.left) / box.width : (ev.clientY - box.top) / box.height)
        const up = () => {
          el.removeEventListener('pointermove', move)
          el.removeEventListener('pointerup', up)
        }
        el.addEventListener('pointermove', move)
        el.addEventListener('pointerup', up)
      }}
    />
  )
}

function TerminalSettings({ onClose }: { onClose: () => void }) {
  const fontSize = usePref('termFontSize')
  const lineHeight = usePref('termLineHeight')
  const fontFamily = usePref('termFontFamily')
  const ligatures = usePref('termLigatures')
  const copyOnSelect = usePref('termCopyOnSelect')
  const theme = usePref('termTheme')
  const suggestHistory = usePref('termSuggestHistory')
  const suggestCommands = usePref('termSuggestCommands')
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const away = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) onClose()
    }
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    document.addEventListener('mousedown', away)
    document.addEventListener('keydown', esc)
    return () => {
      document.removeEventListener('mousedown', away)
      document.removeEventListener('keydown', esc)
    }
  }, [onClose])

  return (
    <div className="term-settings" ref={ref} role="dialog" aria-label={t('터미널 설정')}>
      <div className="form-grid">
        <label>{t('색 테마')}</label>
        <select value={theme} onChange={(e) => setPref('termTheme', e.target.value)}>
          {TERM_THEMES.map((th) => (
            <option key={th.id} value={th.id}>
              {t(th.label)}
            </option>
          ))}
        </select>

        <label>{t('글꼴')}</label>
        <input
          value={fontFamily}
          placeholder={t('앱 기본 고정폭 글꼴')}
          spellCheck={false}
          onChange={(e) => setPref('termFontFamily', e.target.value)}
        />

        <label>{t('크기')}</label>
        <input
          type="number"
          min={9}
          max={28}
          value={fontSize}
          onChange={(e) => setPref('termFontSize', Number(e.target.value))}
        />

        <label>{t('줄 간격')}</label>
        <select value={lineHeight} onChange={(e) => setPref('termLineHeight', Number(e.target.value))}>
          {[100, 110, 120, 130, 140, 150].map((v) => (
            <option key={v} value={v}>
              {(v / 100).toFixed(1)}
            </option>
          ))}
        </select>
      </div>
      <label className="checkbox">
        <input
          type="checkbox"
          checked={ligatures}
          onChange={(e) => setPref('termLigatures', e.target.checked)}
        />
        <span>{t('리거처 (글꼴이 지원할 때)')}</span>
      </label>
      <label className="checkbox">
        <input
          type="checkbox"
          checked={copyOnSelect}
          onChange={(e) => setPref('termCopyOnSelect', e.target.checked)}
        />
        <span>{t('선택하면 바로 복사')}</span>
      </label>
      <div className="term-settings-group">
        <span className="small">{t('입력 중 추천')}</span>
        <span className="muted small">{t('↑ ↓ 로 고르고 → 로 채웁니다')}</span>
      </div>
      <label className="checkbox">
        <input
          type="checkbox"
          checked={suggestHistory}
          onChange={(e) => setPref('termSuggestHistory', e.target.checked)}
        />
        <span>{t('이 서버의 히스토리')}</span>
      </label>
      <label className="checkbox">
        <input
          type="checkbox"
          checked={suggestCommands}
          onChange={(e) => setPref('termSuggestCommands', e.target.checked)}
        />
        <span>{t('자주 쓰는 리눅스 명령어')}</span>
      </label>
      <p className="muted small">
        {t(
          '분할한 칸의 셸은 칸을 닫거나 연결이 끊기면 끝납니다. tmux 처럼 서버에 남지 않습니다 — 앱을 다시 열면 같은 배치에 새 셸이 열립니다.',
        )}
      </p>
    </div>
  )
}

export function TerminalView({
  hostID,
  visible,
  onError,
  onReveal,
  windows,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
  /** A caught `code .` wants the file tab brought forward. */
  onReveal: () => void
  /** The server is Windows: its default shells do not edit lines the POSIX
   *  way, so a Tab-completed line is not read back there (WSL still is). */
  windows?: boolean
}) {
  const remembered = live.get(hostID)
  const [tabs, setTabs] = useState<TermTab[]>(remembered?.tabs ?? [])
  const [active, setActive] = useState<string | null>(remembered?.active ?? null)
  const [infos, setInfos] = useState<Record<string, TerminalInfo>>({})
  const [dead, setDead] = useState<Set<string>>(new Set())
  // Panes `vi` turned into an editor, by terminal. The shell stays alive under
  // the editor and gets the keyboard back when it closes.
  const [editing, setEditing] = useState<Record<string, { path: string; keep: boolean }>>({})
  const [settingsOpen, setSettingsOpen] = useState(false)
  // Open by default would take a quarter of the terminal from somebody who came
  // here to type. It is one click away and it remembers nothing on purpose —
  // the panel is for looking something up, not for living in.
  const [histOpen, setHistOpen] = useState(false)
  const histWidth = usePref('historyWidth')

  const activeTab = tabs.find((x) => x.key === active) ?? null
  const focusedTerm = activeTab?.focus ?? null

  // The arrangement is remembered twice: in memory for this run, against the
  // live session ids, and on disk as a shape for the next launch.
  //
  // Not until the view has settled, though. On mount the state is empty while
  // the sessions are still being asked for, and saving that would wipe the
  // stored arrangement — and mark this host as known-and-empty — a moment
  // before the code that reads them gets to run. Which is exactly what it did,
  // every launch and every reload, until the end-to-end run caught it.
  const settledRef = useRef(remembered !== undefined)
  useEffect(() => {
    if (!settledRef.current) return
    live.set(hostID, { tabs, active })
    saveShapes(hostID, tabs)
  }, [hostID, tabs, active])

  // Where the focused terminal is standing, so the history panel can open
  // there. Asked when the panel opens and when the focus changes — not polled:
  // it moves on a `cd`, and a `cd` is a keystroke this side already saw.
  const [cwd, setCwd] = useState<string | undefined>()
  useEffect(() => {
    if (!histOpen || !focusedTerm) return
    let gone = false
    void TerminalCwd(focusedTerm)
      .then(([p]) => !gone && setCwd(p || undefined))
      .catch(() => {})
    return () => {
      gone = true
    }
  }, [histOpen, focusedTerm])
  const opening = useRef(false)
  // What this host can give a prompt in. One entry on Linux — the account's
  // login shell, which nobody chose here — and several on Windows, where sshd
  // hands out cmd.exe and the person at the keyboard probably wanted
  // PowerShell. The menu only appears when there is something to choose.
  const [shells, setShells] = useState<Shell[]>([])
  const [shellMenu, setShellMenu] = useState(false)
  // The same choice for a split: on Windows a tab can hold cmd beside
  // PowerShell beside WSL. The shortcuts still split into the pane's own shell;
  // the buttons ask, because that is where a choice can be shown.
  const [splitMenu, setSplitMenu] = useState<Dir | null>(null)
  const splitRef = useRef<HTMLDivElement>(null)
  // What is being opened right now, if anything. A WSL distribution whose
  // virtual machine has idled out takes ten to twenty seconds to come back, and
  // Go spends that waiting for it in the mode that is safe to wait in. Without
  // this the window simply sits there, which is indistinguishable from broken —
  // and what people do about that is press the button again.
  const [opening_, setOpening] = useState<string | null>(null)
  const shellRef = useRef<HTMLDivElement>(null)
  const stackRef = useRef<HTMLDivElement>(null)
  /** The host whose sessions this view has already taken over. */
  const adopted = useRef<string | null>(null)

  useEffect(() => {
    let alive = true
    void HostShells(hostID)
      .then((list) => alive && setShells(list))
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [hostID])

  useEffect(() => {
    if (!splitMenu) return
    const away = (e: MouseEvent) => {
      if (!splitRef.current?.contains(e.target as Node)) setSplitMenu(null)
    }
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && setSplitMenu(null)
    document.addEventListener('mousedown', away)
    document.addEventListener('keydown', esc)
    return () => {
      document.removeEventListener('mousedown', away)
      document.removeEventListener('keydown', esc)
    }
  }, [splitMenu])

  useEffect(() => {
    if (!shellMenu) return
    const away = (e: MouseEvent) => {
      if (!shellRef.current?.contains(e.target as Node)) setShellMenu(false)
    }
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && setShellMenu(false)
    document.addEventListener('mousedown', away)
    document.addEventListener('keydown', esc)
    return () => {
      document.removeEventListener('mousedown', away)
      document.removeEventListener('keydown', esc)
    }
  }, [shellMenu])

  /** Opens one shell channel. Null when the server or the budget said no — the
   *  reason has already gone to onError. */
  const openShell = useCallback(
    async (shellId?: string, label?: string): Promise<TerminalInfo | null> => {
      setOpening(label ?? t('새 터미널'))
      try {
        const info = await OpenTerminal(hostID, { cols: 80, rows: 24, shellId })
        shellOfTerm.set(info.id, shellId)
        setInfos((m) => ({ ...m, [info.id]: info }))
        return info
      } catch (e) {
        onError(String(e))
        return null
      } finally {
        setOpening(null)
      }
    },
    [hostID, onError],
  )

  const openTab = useCallback(
    async (shellId?: string, label?: string) => {
      if (opening.current) return
      opening.current = true
      try {
        const info = await openShell(shellId, label)
        if (!info) return
        const tab: TermTab = { key: newKey(), root: { kind: 'pane', term: info.id }, focus: info.id }
        setTabs((list) => [...list, tab])
        setActive(tab.key)
      } finally {
        opening.current = false
      }
    },
    [openShell],
  )

  /** Splits the focused pane of the active tab. */
  const split = useCallback(
    async (dir: Dir, shellId?: string, label?: string) => {
      if (opening.current || !activeTab) return
      opening.current = true
      try {
        const from = activeTab.focus
        // The pane's own shell unless another was picked from the menu.
        const info = await openShell(shellId ?? shellOfTerm.get(from), label)
        if (!info) return
        setTabs((list) =>
          list.map((x) =>
            x.key === activeTab.key
              ? { ...x, root: splitPane(x.root, from, info.id, dir), focus: info.id }
              : x,
          ),
        )
      } finally {
        opening.current = false
      }
    },
    [activeTab, openShell],
  )

  /** The pane `vi` turned into an editor goes back to being the shell. */
  const leaveEditor = useCallback((termID: string) => {
    setEditing((m) => {
      if (!(termID in m)) return m
      const next = { ...m }
      delete next[termID]
      return next
    })
  }, [])

  /** Closes one pane. The last pane of a tab closes the tab. */
  const closePane = useCallback(
    async (term: string) => {
      try {
        await CloseTerminal(term)
      } catch (e) {
        onError(String(e))
      }
      shellOfTerm.delete(term)
      // The file it was editing stays open in the file tab's list; only the
      // pane is gone.
      leaveEditor(term)
      setTabs((list) => {
        const next: TermTab[] = []
        for (const x of list) {
          if (!panes(x.root).includes(term)) {
            next.push(x)
            continue
          }
          const root = removePane(x.root, term)
          if (!root) continue
          const rest = panes(root)
          next.push({ ...x, root, focus: rest.includes(x.focus) ? x.focus : rest[rest.length - 1] })
        }
        setActive((cur) =>
          next.some((x) => x.key === cur) ? cur : (next[next.length - 1]?.key ?? null),
        )
        return next
      })
    },
    [onError, leaveEditor],
  )

  const closeTab = useCallback(
    async (key: string) => {
      const tab = tabs.find((x) => x.key === key)
      if (!tab) return
      for (const term of panes(tab.root)) await closePane(term)
    },
    [tabs, closePane],
  )

  const focusPane = useCallback((key: string, term: string) => {
    setTabs((list) => list.map((x) => (x.key === key && x.focus !== term ? { ...x, focus: term } : x)))
  }, [])

  const move = useCallback(
    (way: Way) => {
      if (!activeTab) return
      const next = neighbour(activeTab.focus, way, geometry(activeTab.root).panes)
      if (next) focusPane(activeTab.key, next)
    },
    [activeTab, focusPane],
  )

  const act = useCallback(
    (a: TermAction) => {
      switch (a) {
        case 'new-tab':
          void openTab()
          break
        case 'split-right':
          void split('row')
          break
        case 'split-down':
          void split('col')
          break
        case 'close-pane':
          if (activeTab) void closePane(activeTab.focus)
          break
        case 'focus-left':
          move('left')
          break
        case 'focus-right':
          move('right')
          break
        case 'focus-up':
          move('up')
          break
        case 'focus-down':
          move('down')
          break
        case 'font-bigger':
          setPref('termFontSize', getPref('termFontSize') + 1)
          break
        case 'font-smaller':
          setPref('termFontSize', getPref('termFontSize') - 1)
          break
        case 'font-reset':
          setPref('termFontSize', 13)
          break
      }
    },
    [openTab, split, closePane, move, activeTab],
  )

  // The palette's actions reach the terminal that is on screen, and only it.
  const actRef = useRef(act)
  actRef.current = act
  const visibleRef = useRef(visible)
  visibleRef.current = visible
  useEffect(
    () =>
      onTermAction((a) => {
        if (!visibleRef.current) return false
        actRef.current(a)
        return true
      }),
    [],
  )

  // Adopt whatever is already running, and only open a new terminal if there is
  // nothing to adopt.
  //
  // Go holds the sessions, not this view. That was the whole point back when
  // looking at another tab unmounted the view: trusting the local list meant
  // coming back from the file tree with an empty one, opening a second
  // terminal, and leaving the first holding a channel no one could name any
  // more. Four round trips and the host was out of slots (§4.6).
  //
  // The view now survives a tab switch, which is what keeps the scrollback —
  // the PTY always survived, but a fresh xterm adopting it came up blank, so
  // everything typed before the switch was gone from the screen while still
  // being gone-from-the-screen only. Adoption still matters: a reconnect, a
  // restarted app, or a second window all arrive with sessions already running.
  //
  // With splits, adoption also has to put each session back in its pane. The
  // in-memory layout says where; a running session it does not mention gets a
  // tab of its own, and a pane whose session is gone is pruned.
  useEffect(() => {
    // Claimed synchronously, before any await: React runs mount effects twice in
    // development, and two passes that both find an empty list would both open a
    // terminal — the very leak this effect exists to close.
    if (adopted.current === hostID) return
    adopted.current = hostID

    let cancelled = false
    // Whether this pass reached an answer — adopted the running sessions, or
    // opened the first one. Only an unsettled pass releases the claim below.
    let settled = false
    ;(async () => {
      try {
        const existing = await ListTerminals(hostID)
        if (cancelled) return
        if (existing.length > 0) {
          settled = true
          setInfos(Object.fromEntries(existing.map((x) => [x.id, x])))
          const alive = new Set(existing.map((x) => x.id))
          const kept: TermTab[] = []
          const known = live.get(hostID)
          if (known) {
            for (const x of known.tabs) {
              const root = prune(x.root, alive)
              if (!root) continue
              const rest = panes(root)
              kept.push({ ...x, root, focus: rest.includes(x.focus) ? x.focus : rest[0] })
            }
          } else {
            // Sessions running and no layout in memory: the page itself was
            // reloaded — a browser in server mode — while Go kept the shells.
            // The saved shapes say how they were arranged; they are put back in
            // the order they were opened, which is the order the shapes were
            // filled in.
            const order = [...existing].sort((a, b) => a.seq - b.seq).map((x) => x.id)
            let at = 0
            for (const shape of loadShapes(hostID)) {
              const n = countPanes(shape)
              if (at + n > order.length) break
              const root = fill(shape, order.slice(at, at + n))
              at += n
              if (root) kept.push({ key: newKey(), root, focus: panes(root)[0] })
            }
          }
          const placed = new Set(kept.flatMap((x) => panes(x.root)))
          for (const x of existing) {
            if (placed.has(x.id)) continue
            kept.push({ key: newKey(), root: { kind: 'pane', term: x.id }, focus: x.id })
          }
          settledRef.current = true
          setTabs(kept)
          setActive((cur) =>
            cur && kept.some((x) => x.key === cur) ? cur : (kept[kept.length - 1]?.key ?? null),
          )
          return
        }
        // A terminal costs a channel from the long-lived budget, so someone who
        // never opens this tab should not pay for one. Left unsettled when it
        // is not visible, so the next visit asks again rather than inheriting
        // an answer that was only "not yet".
        if (!visible) return
        settled = true

        // The arrangement from last time, with fresh shells in it. Opened one
        // at a time and stopped at the first refusal: the budget is per host,
        // and asking for the rest after the server said no would only repeat
        // the same error once per pane.
        const shapes = loadShapes(hostID)
        if (shapes.length === 0) {
          settledRef.current = true
          void openTab()
          return
        }
        const restored: TermTab[] = []
        let refused = false
        for (const shape of shapes) {
          const ids: (string | null)[] = []
          for (let i = 0; i < countPanes(shape); i++) {
            const info = refused ? null : await openShell()
            if (!info) refused = true
            ids.push(info?.id ?? null)
          }
          const root = fill(shape, ids)
          if (root) {
            const all = panes(root)
            restored.push({ key: newKey(), root, focus: all[0] })
          }
        }
        if (cancelled) return
        settledRef.current = true
        if (restored.length === 0) {
          void openTab()
          return
        }
        setTabs(restored)
        setActive(restored[0].key)
      } catch (e) {
        if (!cancelled) onError(String(e))
      }
    })()
    return () => {
      cancelled = true
      // Release the claim on the way out, or the pair of guards cancels the
      // feature rather than the duplicate: React's development double-invoke
      // cancels the first pass here and the second finds the host already
      // claimed, so nothing ever opens. Releasing keeps the single-open
      // guarantee — the cancelled pass returns before it can open anything —
      // while letting the pass that survives do its job.
      //
      // Only an unsettled pass, though. `visible` is a dependency, so a tab
      // switch re-runs this effect; releasing unconditionally meant asking Go
      // for the session list twice per round trip and answering a question
      // already answered.
      if (!settled && adopted.current === hostID) adopted.current = null
    }
  }, [hostID, visible, openTab, openShell, onError])

  // Resolving the path may mean asking that shell where it is standing, which
  // only Go can do — it is the side holding the session.
  //
  // `code` opens the project: the file tab, with the tree. `vi` edits one file
  // where it was typed — the pane becomes the editor and becomes the shell
  // again when it closes, the way the real vi takes the screen and gives it
  // back. With four terminals open, the edit stays with the one it came from.
  const reveal = async (termID: string, command: string, arg: string, clear = '') => {
    try {
      const req = await RevealFromTerminal(termID, arg, clear)
      if (req.error) {
        onError(req.error)
        return
      }
      // `vi .` and `vi somedir` mean browsing, which is the file tab's job.
      if (command !== 'vi' || req.isDir) {
        requestReveal(req.hostId, req.path, req.isDir, req.new)
        onReveal()
        return
      }
      const keep = isOpen(req.hostId, req.path)
      if (!keep) {
        if (req.new) {
          // Not there yet: an empty buffer, and the first save creates it.
          openFile(req.hostId, {
            path: req.path,
            content: '',
            size: 0,
            perm: 0o644,
            modTime: 0,
            tooLarge: false,
            binary: false,
          })
        } else {
          const file = await ReadTextFile(req.hostId, req.path)
          if (file.tooLarge || file.binary) {
            onError(
              file.binary
                ? t('{path}: 바이너리 파일이라 열 수 없습니다', { path: req.path })
                : t('{path}: 편집기 한도(2MB)를 넘어 열 수 없습니다', { path: req.path }),
            )
            return
          }
          openFile(req.hostId, file)
        }
      }
      setEditing((m) => ({ ...m, [termID]: { path: req.path, keep } }))
    } catch (e) {
      onError(String(e))
    }
  }


  // Sends the shell somewhere the history says work happened. Typed into the
  // terminal rather than run behind it: what lands is a `cd` the user can see,
  // edit or Ctrl-C, and it shows up in that shell's own history like any other
  // line. The path is single-quoted because it came out of a history file —
  // spaces and `$` in a directory name are ordinary, and neither should reach
  // the shell as syntax.
  const goTo = (termID: string, dir: string) => {
    const quoted = "'" + dir.replaceAll("'", `'\\''`) + "'"
    void WriteTerminal(termID, b64encode(`cd ${quoted}\n`)).catch((e) => onError(String(e)))
  }

  // A-2: the keys nobody found. Two Windows users reported that copy and paste
  // did not work when both were bound all along — the combination is simply not
  // discoverable, and a line of prose in the toolbar was removed once already
  // for being noise. The tab's tooltip is where somebody hunting for it looks.
  const mod = getPlatform().isMac ? '⌘' : 'Ctrl+Shift+'
  // Right-click pastes everywhere except macOS, where it opens the system
  // context menu instead and the app never sees the event. Saying so there was
  // a hint that did not work on the platform reading it.
  const copyHint = getPlatform().isMac
    ? t('복사 {c} · 붙여넣기 {v}', { c: `${mod}C`, v: `${mod}V` })
    : t('복사 {c} · 붙여넣기 {v} · 우클릭으로도 붙여넣기', { c: `${mod}C`, v: `${mod}V` })

  const tabTitle = (x: TermTab) => {
    const info = infos[x.focus]
    const n = panes(x.root).length
    return { title: info?.title ?? x.focus, n }
  }

  // Geometry of every tab, recomputed when a layout changes and not on every
  // keystroke render.
  const geo = useMemo(
    () => new Map(tabs.map((x) => [x.key, geometry(x.root)] as const)),
    [tabs],
  )

  return (
    <div className="view term-view">
      <div className="view-toolbar">
        <div className="term-tabs">
          {tabs.map((x) => {
            const { title, n } = tabTitle(x)
            const allDead = panes(x.root).every((p) => dead.has(p))
            return (
              <button
                key={x.key}
                className="term-tab"
                data-on={x.key === active || undefined}
                data-dead={allDead || undefined}
                title={copyHint}
                onClick={() => setActive(x.key)}
                onAuxClick={(e) => {
                  // Middle click closes, as it does on the file tabs and in every
                  // other terminal people have used.
                  if (e.button === 1) void closeTab(x.key)
                }}
              >
                <span className="ellipsis">{title}</span>
                {n > 1 && <span className="term-tab-count">{n}</span>}
                <span
                  className="term-tab-close"
                  onClick={(e) => {
                    e.stopPropagation()
                    void closeTab(x.key)
                  }}
                >
                  ×
                </span>
              </button>
            )
          })}
        </div>
        <div className="term-new" ref={shellRef}>
          <button
            className="ghost"
            onClick={() => (shells.length > 1 ? setShellMenu((v) => !v) : void openTab())}
            aria-haspopup={shells.length > 1 ? 'menu' : undefined}
            aria-expanded={shells.length > 1 ? shellMenu : undefined}
            title={
              (shells.length > 1 ? t('새 터미널 — 셸 고르기') : t('새 터미널')) +
              ` (${termKeyLabel('new-tab')})`
            }
          >
            +
          </button>
          {shellMenu && (
            <div className="term-shell-menu" role="menu">
              {shells.map((sh) => (
                <button
                  key={sh.id}
                  role="menuitem"
                  className="rail-pop-item"
                  onClick={() => {
                    setShellMenu(false)
                    // The label as well as the id. WSL is the one that takes
                    // seconds to come up — cold, six to twelve of them — and it
                    // was the one whose name the indicator did not show.
                    void openTab(sh.id, sh.label)
                  }}
                >
                  {sh.label}
                </button>
              ))}
            </div>
          )}
        </div>
        {activeTab && (
          <div className="term-split-anchor" ref={splitRef}>
            {(['row', 'col'] as const).map((dir) => (
              <button
                key={dir}
                className="ghost small-btn term-split-btn"
                data-on={splitMenu === dir || undefined}
                onClick={() =>
                  shells.length > 1 ? setSplitMenu((m) => (m === dir ? null : dir)) : void split(dir)
                }
                aria-haspopup={shells.length > 1 ? 'menu' : undefined}
                aria-expanded={shells.length > 1 ? splitMenu === dir : undefined}
                title={
                  dir === 'row'
                    ? t('좌우로 나누기 ({key})', { key: termKeyLabel('split-right') })
                    : t('위아래로 나누기 ({key})', { key: termKeyLabel('split-down') })
                }
                aria-label={dir === 'row' ? t('좌우로 나누기') : t('위아래로 나누기')}
              >
                {dir === 'row' ? '◫' : '⊟'}
              </button>
            ))}
            {splitMenu && (
              <div className="term-shell-menu" role="menu" aria-label={t('새 칸의 셸')}>
                {shells.map((sh) => {
                  const here = (shellOfTerm.get(activeTab.focus) ?? shells[0]?.id) === sh.id
                  return (
                    <button
                      key={sh.id}
                      role="menuitem"
                      className="rail-pop-item"
                      onClick={() => {
                        const dir = splitMenu
                        setSplitMenu(null)
                        void split(dir, sh.id, sh.label)
                      }}
                    >
                      <span className="ellipsis">{sh.label}</span>
                      {here && <span className="muted small term-shell-here">{t('지금 칸')}</span>}
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        )}
        {opening_ && (
          <span className="term-opening muted small">
            <span className="spin" aria-hidden="true" />
            {t('{shell} 여는 중…', { shell: opening_ })}
          </span>
        )}
        <span className="spacer" />
        <div className="term-settings-anchor">
          <button
            className="ghost small-btn"
            data-on={settingsOpen || undefined}
            onClick={() => setSettingsOpen((v) => !v)}
            title={t('터미널 설정 — 테마·글꼴')}
            aria-label={t('터미널 설정')}
          >
            Aa
          </button>
          {settingsOpen && <TerminalSettings onClose={() => setSettingsOpen(false)} />}
        </div>
        <button
          className="ghost small-btn"
          data-on={histOpen || undefined}
          onClick={() => setHistOpen((v) => !v)}
          title={t('이 서버에서 지난번에 무엇을 했는지')}
        >
          {t('히스토리')}
        </button>
      </div>

      {/* The history sits beside the terminal because that is where it is used:
          what people do with a command they ran before is run it again, and a
          list of them one tab away is a list nobody walks to. */}
      <div className="term-split" data-history={histOpen || undefined}>
        <div className="term-stack" ref={stackRef}>
          {tabs.length === 0 && <div className="placeholder">{t('터미널을 여는 중…')}</div>}
          {tabs.map((x) => {
            const g = geo.get(x.key)
            if (!g) return null
            const many = g.panes.size > 1
            return (
              <div key={x.key} className="term-slot" data-active={x.key === active || undefined}>
                {[...g.panes].map(([term, box]) => {
                  const info = infos[term]
                  if (!info) return null
                  return (
                    <div
                      key={term}
                      className="term-cell"
                      data-focus={(many && x.focus === term) || undefined}
                      data-dead={dead.has(term) || undefined}
                      style={{
                        left: `${box.x * 100}%`,
                        top: `${box.y * 100}%`,
                        width: `${box.w * 100}%`,
                        height: `${box.h * 100}%`,
                      }}
                      onMouseDown={() => focusPane(x.key, term)}
                    >
                      <TerminalPane
                        info={info}
                        posix={!windows || (shellOfTerm.get(term) ?? '').startsWith('wsl')}
                        focused={visible && x.key === active && x.focus === term && !editing[term]}
                        onClosed={() => setDead((d) => new Set(d).add(term))}
                        onError={onError}
                        onCommand={(cmd, arg, clear) => void reveal(term, cmd, arg, clear)}
                        onAction={act}
                        onFocused={() => focusPane(x.key, term)}
                      />
                      {editing[term] && (
                        <div className="term-edit">
                          <Suspense fallback={<div className="placeholder">{t('편집기를 불러오는 중…')}</div>}>
                            <EditorPane
                              hostID={hostID}
                              visible={visible && x.key === active}
                              only={editing[term].path}
                              keepOpen={editing[term].keep}
                              onLeave={() => leaveEditor(term)}
                              onError={onError}
                              onSaved={() => {}}
                              onDownload={() => {}}
                            />
                          </Suspense>
                        </div>
                      )}
                      {many && !editing[term] && (
                        <button
                          className="term-cell-close ghost small-btn"
                          onClick={() => void closePane(term)}
                          title={t('이 칸 닫기 ({key})', { key: termKeyLabel('close-pane') })}
                          aria-label={t('이 칸 닫기')}
                        >
                          ×
                        </button>
                      )}
                    </div>
                  )
                })}
                {g.dividers.map((d) => (
                  <DividerHandle
                    key={d.path || 'root'}
                    dir={d.dir}
                    style={{
                      left: `${d.at.x * 100}%`,
                      top: `${d.at.y * 100}%`,
                      width: d.dir === 'row' ? undefined : `${d.at.w * 100}%`,
                      height: d.dir === 'row' ? `${d.at.h * 100}%` : undefined,
                    }}
                    boxOf={() => {
                      const r = stackRef.current?.getBoundingClientRect()
                      if (!r) return null
                      return new DOMRect(
                        r.left + d.span.x * r.width,
                        r.top + d.span.y * r.height,
                        d.span.w * r.width,
                        d.span.h * r.height,
                      )
                    }}
                    onRatio={(ratio) =>
                      setTabs((list) =>
                        list.map((y) => (y.key === x.key ? { ...y, root: setRatio(y.root, d.path, ratio) } : y)),
                      )
                    }
                  />
                ))}
              </div>
            )
          })}
        </div>

        {histOpen && (
          <aside className="term-history" style={{ width: histWidth }}>
            <ResizeHandle pref="historyWidth" label={t('히스토리 너비')} axis="x" />
            <CommandHistory
              hostID={hostID}
              cwd={cwd}
              onError={onError}
              onGoTo={focusedTerm ? (dir) => goTo(focusedTerm, dir) : undefined}
            />
          </aside>
        )}
      </div>
    </div>
  )
}
