import { useCallback, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import {
  CloseTerminal,
  OpenTerminal,
  ResizeTerminal,
  WriteTerminal,
  on,
  type TerminalInfo,
} from './ipc'

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
}: {
  info: TerminalInfo
  onClosed: () => void
  onError: (msg: string) => void
}) {
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)

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
      term.write(`\r\n\x1b[2m— 세션 종료${msg ? `: ${msg}` : ''} —\x1b[0m\r\n`)
      onClosed()
    })

    const disposeInput = term.onData((data) => {
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
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [tabs, setTabs] = useState<TerminalInfo[]>([])
  const [active, setActive] = useState<string | null>(null)
  const [dead, setDead] = useState<Set<string>>(new Set())
  const opening = useRef(false)

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

  // Open one on first view, not on mount: a terminal costs a channel from the
  // long-lived budget, and someone who never opens the tab should not pay it.
  useEffect(() => {
    if (visible && tabs.length === 0) void openTab()
  }, [visible, tabs.length, openTab])

  // Terminals belong to a connection; closing them all when the tab unmounts
  // would kill sessions the user is coming back to, so they persist until the
  // host disconnects or the tab is closed explicitly.
  useEffect(() => {
    return () => {
      // Nothing here on purpose — see above.
    }
  }, [])

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
          <button className="ghost" onClick={() => void openTab()} title="새 터미널">
            +
          </button>
        </div>
        <span className="spacer" />
        <span className="muted small">
          GUI가 표현하지 못하는 일을 위한 탭입니다 — 실행한 명령은 아래 Command Log에
          남습니다
        </span>
      </div>

      <div className="term-stack">
        {tabs.length === 0 && <div className="placeholder">터미널을 여는 중…</div>}
        {tabs.map((t) => (
          <div key={t.id} className="term-slot" data-active={t.id === active || undefined}>
            <TerminalPane
              info={t}
              onClosed={() => setDead((d) => new Set(d).add(t.id))}
              onError={onError}
            />
          </div>
        ))}
      </div>
    </div>
  )
}
