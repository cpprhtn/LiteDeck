import { useCallback, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import {
  CloseTerminal,
  ListTerminals,
  OpenTerminal,
  ResizeTerminal,
  RevealFromTerminal,
  WriteTerminal,
  on,
  type TerminalInfo,
} from './ipc'
import { LineWatcher } from './lineWatcher'
import { requestReveal } from './openFiles'
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
  onClosed,
  onError,
  onCommand,
}: {
  info: TerminalInfo
  onClosed: () => void
  onError: (msg: string) => void
  onCommand: (command: string, arg: string) => void
}) {
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  // Through a ref: the terminal is built once, and rebuilding it whenever the
  // parent re-renders would throw away the session's scrollback.
  const command = useRef(onCommand)
  command.current = onCommand

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

    const offData = on<string>(`term:data:${info.id}`, (chunk) =>
      term.write(b64decode(chunk)),
    )
    const offExit = on<string>(`term:exit:${info.id}`, (msg) => {
      term.write(`\r\n\x1b[2m— ${t('세션 종료')}${msg ? `: ${msg}` : ''} —\x1b[0m\r\n`)
      onClosed()
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
      void WriteTerminal(info.id, b64encode(data)).catch((e) => onError(String(e)))
    })

    // The remote side needs to know the size or full-screen programs draw at
    // 80x24 regardless of the window.
    const resize = () => {
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
  }, [info.id, onClosed, onError])

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
  // This view unmounts every time the user looks at another tab, so its own
  // state is not a record of anything — Go is. Trusting the local list meant
  // coming back from the file tab with an empty one, opening a second terminal,
  // and leaving the first holding a channel no one could name any more. Four
  // round trips and the host was out of slots (§4.6).
  //
  // Sessions deliberately survive the unmount: a running build or an htop should
  // not die because someone glanced at the file tree.
  useEffect(() => {
    // Claimed synchronously, before any await: React runs mount effects twice in
    // development, and two passes that both find an empty list would both open a
    // terminal — the very leak this effect exists to close.
    if (adopted.current === hostID) return
    adopted.current = hostID

    let cancelled = false
    ;(async () => {
      try {
        const existing = await ListTerminals(hostID)
        if (cancelled) return
        if (existing.length > 0) {
          setTabs(existing)
          setActive((cur) =>
            cur && existing.some((t) => t.id === cur) ? cur : existing[existing.length - 1].id,
          )
          return
        }
        // A terminal costs a channel from the long-lived budget, so someone who
        // never opens this tab should not pay for one.
        if (visible) void openTab()
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
      if (adopted.current === hostID) adopted.current = null
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
