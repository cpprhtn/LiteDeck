import { useCallback, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import {
  CloseTerminal,
  ListTerminals,
  OpenTerminal,
  ReadClipboard,
  ResizeTerminal,
  RevealFromTerminal,
  WriteTerminal,
  on,
  type TerminalInfo,
} from './ipc'
import { LineWatcher } from './lineWatcher'
import { requestReveal } from './openFiles'
import { getPlatform } from './platform'
import { t } from './i18n'

// The built-in terminal (§4.6).
//
// Secondary to the GUI by design — it is here for the things a GUI cannot
// express, and for the moment someone wants to check the app's work by hand.
// The Command Log is the bridge in the other direction.
//
// Output crosses the boundary as base64: terminal bytes are not text, and JSON
// string encoding would turn partial UTF-8 at a chunk boundary into replacement
// characters that never repair themselves.

const enc = new TextEncoder()
const dec = new TextDecoder()

function b64encode(data: string): string {
  const bytes = enc.encode(data)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin)
}

function b64decode(data: string): string {
  const bin = atob(data)
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  // stream: true keeps a multi-byte character split across two chunks intact.
  return dec.decode(bytes, { stream: true })
}

/** Reads the app's design tokens so the terminal matches the rest of the UI. */
function themeFromTokens() {
  const s = getComputedStyle(document.documentElement)
  const v = (name: string, fallback: string) =>
    s.getPropertyValue(name).trim() || fallback
  return {
    background: v('--bg', '#1c1c1e'),
    foreground: v('--fg', '#f5f5f7'),
    cursor: v('--accent', '#0a84ff'),
    selectionBackground: v('--bg-selected', '#0a84ff40'),
  }
}

function TerminalPane({
  info,
  focused,
  onClosed,
  onError,
  onCommand,
}: {
  info: TerminalInfo
  /** This pane is the selected terminal, on the tab that is on screen. */
  focused: boolean
  onClosed: () => void
  onError: (msg: string) => void
  onCommand: (command: string, arg: string) => void
}) {
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  // Through refs: the terminal is built once, and rebuilding it whenever the
  // parent re-renders would throw away the session's scrollback.
  //
  // All three callbacks and not just onCommand — a tab switch is a re-render
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

  useEffect(() => {
    if (!hostRef.current) return

    const term = new Terminal({
      fontFamily: getComputedStyle(document.documentElement)
        .getPropertyValue('--font-mono')
        .trim(),
      fontSize: 13,
      cursorBlink: true,
      scrollback: 5000,
      theme: themeFromTokens(),
      // macOS convention: ⌥ composes characters rather than sending Meta.
      macOptionIsMeta: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(hostRef.current)
    fit.fit()

    termRef.current = term
    fitRef.current = fit

    // Copy and paste, which the terminal has to bind for itself.
    //
    // Everywhere else in the window the OS provides them — the macOS Edit menu
    // (menu_darwin.go), or WebView2 and WebKitGTK's own bindings. Here it
    // cannot: Ctrl+C is SIGINT and must stay SIGINT, and xterm claims every
    // keystroke before the browser sees it.
    //
    // So the platform's terminal convention rather than its text-field one:
    // ⌘C/⌘V on macOS, where ⌘ collides with nothing, and Ctrl+Shift+C/V
    // everywhere else — what GNOME Terminal, Konsole and Windows Terminal all
    // use, and for the same reason.
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== 'keydown') return true
      const mac = getPlatform().isMac
      const combo = mac
        ? e.metaKey && !e.ctrlKey && !e.altKey
        : e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey
      if (!combo) return true

      const key = e.key.toLowerCase()
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
      // Through Go, not navigator.clipboard.readText(): WebKit refuses that
      // call outright, so on macOS the paste would silently do nothing. Writing
      // is allowed in the same webview, which is why only this half detours.
      ReadClipboard()
        // term.paste rather than WriteTerminal: it wraps the text in the
        // bracketed-paste markers when the far side asked for them, which is
        // what stops a pasted block from being run a line at a time, and
        // normalises CRLF to CR. It reaches the server through onData below,
        // so the Command Log and the `code`/`vi` watcher still see it.
        .then((text) => text && term.paste(text))
        .catch(() => failed.current(t('클립보드를 읽지 못했습니다.')))
      return false
    })

    const offData = on<string>(`term:data:${info.id}`, (chunk) =>
      term.write(b64decode(chunk)),
    )
    const offExit = on<string>(`term:exit:${info.id}`, (msg) => {
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
    const disposeInput = term.onData((data) => {
      const caught = typed.feed(data, term.buffer.active.type === 'normal')
      if (caught) {
        // Erase the line a character at a time instead of sending Enter, so the
        // command neither runs nor reaches history. The user sees their prompt
        // come back, which is what "the app handled it" should look like.
        //
        // Backspace rather than Ctrl-U: readline understands both, but cmd.exe
        // takes Ctrl-U as a literal ^U, leaves the line intact, and runs it —
        // which turned `code .` on a Windows server into `code .^U` plus
        // whatever was sent next. Verified against a real cmd.exe and a real
        // POSIX shell; backspace is the only thing both agree on.
        //
        // Counted in code points: an emoji is two UTF-16 units and one
        // backspace.
        const erase = '\b'.repeat([...caught.line].length)
        void WriteTerminal(info.id, b64encode(erase)).catch(() => {})
        command.current(caught.command, caught.arg)
        return
      }
      void WriteTerminal(info.id, b64encode(data)).catch((e) => failed.current(String(e)))
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
    const resize = () => {
      const el = hostRef.current
      if (!el || el.clientWidth === 0 || el.clientHeight === 0) return
      try {
        fit.fit()
        void ResizeTerminal(info.id, term.cols, term.rows).catch(() => {})
      } catch {
        /* the pane is not visible yet */
      }
    }
    const observer = new ResizeObserver(resize)
    observer.observe(hostRef.current)
    resize()
    term.focus()

    return () => {
      observer.disconnect()
      disposeInput.dispose()
      offData()
      offExit()
      term.dispose()
      termRef.current = null
    }
    // The session id alone: see the refs above.
  }, [info.id])

  // The keyboard follows the tab. Mounting used to do this once, at the end of
  // the effect above, which was enough back when coming back to this tab meant
  // mounting again. Now that the pane survives, nothing re-aimed the keyboard —
  // the terminal looked ready, kept its scrollback, and swallowed the first
  // thing typed at it because the focus was still on the tab button.
  useEffect(() => {
    if (focused) termRef.current?.focus()
  }, [focused])

  return <div className="term-pane" ref={hostRef} />
}

export function TerminalView({
  hostID,
  visible,
  onError,
  onReveal,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
  /** A caught `code`/`vi` wants the file tab brought forward. */
  onReveal: () => void
}) {
  const [tabs, setTabs] = useState<TerminalInfo[]>([])
  const [active, setActive] = useState<string | null>(null)
  const [dead, setDead] = useState<Set<string>>(new Set())
  const opening = useRef(false)
  /** The host whose sessions this view has already taken over. */
  const adopted = useRef<string | null>(null)

  const openTab = useCallback(async () => {
    if (opening.current) return
    opening.current = true
    try {
      const info = await OpenTerminal(hostID, { cols: 80, rows: 24 })
      setTabs((t) => [...t, info])
      setActive(info.id)
    } catch (e) {
      onError(String(e))
    } finally {
      opening.current = false
    }
  }, [hostID, onError])

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
          setTabs(existing)
          setActive((cur) =>
            cur && existing.some((t) => t.id === cur) ? cur : existing[existing.length - 1].id,
          )
          return
        }
        // A terminal costs a channel from the long-lived budget, so someone who
        // never opens this tab should not pay for one. Left unsettled when it
        // is not visible, so the next visit asks again rather than inheriting
        // an answer that was only "not yet".
        if (visible) {
          settled = true
          void openTab()
        }
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
  }, [hostID, visible, openTab, onError])

  // Resolving the path may mean asking that shell where it is standing, which
  // only Go can do — it is the side holding the session.
  const reveal = async (termID: string, arg: string) => {
    try {
      const req = await RevealFromTerminal(termID, arg)
      if (req.error) {
        onError(req.error)
        return
      }
      requestReveal(req.hostId, req.path, req.isDir, req.new)
      onReveal()
    } catch (e) {
      onError(String(e))
    }
  }

  const close = async (id: string) => {
    try {
      await CloseTerminal(id)
    } catch (e) {
      onError(String(e))
    }
    setTabs((t) => {
      const next = t.filter((x) => x.id !== id)
      setActive((cur) => (cur === id ? (next[next.length - 1]?.id ?? null) : cur))
      return next
    })
  }

  return (
    <div className="view term-view">
      <div className="view-toolbar">
        <div className="term-tabs">
          {tabs.map((t) => (
            <button
              key={t.id}
              className="term-tab"
              data-on={t.id === active || undefined}
              data-dead={dead.has(t.id) || undefined}
              onClick={() => setActive(t.id)}
            >
              <span className="ellipsis">{t.title}</span>
              <span
                className="term-tab-close"
                onClick={(e) => {
                  e.stopPropagation()
                  void close(t.id)
                }}
              >
                ×
              </span>
            </button>
          ))}
          <button className="ghost" onClick={() => void openTab()} title={t('새 터미널')}>
            +
          </button>
        </div>
        <span className="spacer" />
        <span className="muted small">
          {t('GUI가 표현하지 못하는 일을 위한 탭입니다 — 실행한 명령은 아래 Command Log에 남습니다')}
        </span>
      </div>

      <div className="term-stack">
        {tabs.length === 0 && <div className="placeholder">{t('터미널을 여는 중…')}</div>}
        {tabs.map((t) => (
          <div key={t.id} className="term-slot" data-active={t.id === active || undefined}>
            <TerminalPane
              info={t}
              focused={visible && t.id === active}
              onClosed={() => setDead((d) => new Set(d).add(t.id))}
              onError={onError}
              onCommand={(_cmd, arg) => void reveal(t.id, arg)}
            />
          </div>
        ))}
      </div>
    </div>
  )
}
