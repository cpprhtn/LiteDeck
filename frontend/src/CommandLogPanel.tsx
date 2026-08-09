import { useEffect, useRef, useState } from 'react'
import { ClearCommandLog, GetCommandLog, on, type CommandEntry } from './ipc'

// The Command Log (§4.6) — every command the GUI ran, as it ran, copyable.
//
// This is the app's trust surface. A GUI operating on a production server is
// asking to be believed; showing exactly what it just executed is how it earns
// that, and it teaches the CLI on the way. Local only, never transmitted (§7.4).

export function CommandLogPanel({
  open,
  onToggle,
}: {
  open: boolean
  onToggle: () => void
}) {
  const [entries, setEntries] = useState<CommandEntry[]>([])
  const [copied, setCopied] = useState<number | null>(null)
  // Background reads are folded away by default. They are not hidden — the
  // toggle is right there — but a view refreshing every two seconds produces
  // hundreds of entries an hour, and the one restart the user performed would
  // otherwise be buried in them. The log exists to answer "what did the GUI do
  // on my behalf", and that answer has to stay legible (§4.6).
  const [showBackground, setShowBackground] = useState(false)
  const endRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    void GetCommandLog().then(setEntries).catch(() => {})

    const upsert = (e: CommandEntry) =>
      setEntries((prev) => {
        const i = prev.findIndex((x) => x.seq === e.seq)
        if (i < 0) return [...prev, e].slice(-500)
        const next = [...prev]
        next[i] = e
        return next
      })

    const offStart = on<CommandEntry>('cmd:started', upsert)
    const offEnd = on<CommandEntry>('cmd:finished', upsert)
    return () => {
      offStart()
      offEnd()
    }
  }, [])

  useEffect(() => {
    if (open) endRef.current?.scrollIntoView({ block: 'end' })
  }, [entries, open])

  const copy = async (e: CommandEntry) => {
    await navigator.clipboard.writeText(e.line)
    setCopied(e.seq)
    window.setTimeout(() => setCopied(null), 1200)
  }

  // Probes are excluded: `command -v podman` answering "no" is information,
  // and counting it would put a permanent "2 실패" on the panel that trains the
  // user to ignore the number entirely.
  const failures = entries.filter(
    (e) => e.status === 'failed' || e.status === 'error',
  ).length
  const background = entries.filter((e) => e.kind === 'poll' || e.kind === 'probe')
  const shown = showBackground ? entries : entries.filter((e) => !e.kind)

  return (
    <div className="cmdlog" data-open={open || undefined}>
      <button className="cmdlog-head" onClick={onToggle}>
        <span className="chevron">{open ? '▾' : '▸'}</span>
        <strong>Command Log</strong>
        <span className="muted small">{shown.length}건</span>
        {failures > 0 && <span className="badge danger">{failures} 실패</span>}
        <span className="spacer" />
        {open && background.length > 0 && (
          <span
            className="ghost small-btn"
            onClick={(ev) => {
              ev.stopPropagation()
              setShowBackground((v) => !v)
            }}
          >
            {showBackground ? '백그라운드 조회 접기' : `백그라운드 조회 ${background.length}건 보기`}
          </span>
        )}
        {open && (
          <span
            className="ghost small-btn"
            onClick={(ev) => {
              ev.stopPropagation()
              void ClearCommandLog()
              setEntries([])
            }}
          >
            지우기
          </span>
        )}
      </button>

      {open && (
        <div className="cmdlog-body">
          {shown.length === 0 && (
            <div className="placeholder small">
              {entries.length === 0
                ? 'GUI가 실행하는 모든 명령이 여기에 실시간으로 표시됩니다. 클릭하면 복사됩니다.'
                : '아직 직접 실행한 명령이 없습니다 — 위에서 백그라운드 조회를 펼쳐볼 수 있습니다.'}
            </div>
          )}
          {shown.map((e) => (
            <div
              key={e.seq}
              className="cmdlog-row"
              data-status={e.status}
              data-kind={e.kind || undefined}
              onClick={() => void copy(e)}
              title="클릭해서 복사"
            >
              <span className="cmdlog-marker">$</span>
              {/* Newlines shown as ⏎ rather than collapsed to whitespace by the
                  browser. A multi-line command such as the metrics script read as
                  `... | head -1 echo '#mem'` — a command that would be broken if
                  you typed it. On the one surface whose whole job is showing
                  exactly what ran, that is not a cosmetic issue. The clipboard
                  gets e.line untouched, so a paste still works. */}
              <span className="mono cmdlog-line">
                {e.line.split('\n').map((part, i) => (
                  <span key={i}>
                    {i > 0 && <span className="cmdlog-nl" title="줄바꿈">⏎ </span>}
                    {part}
                  </span>
                ))}
              </span>
              <span className="cmdlog-meta muted">
                {e.status === 'running' && '실행 중'}
                {e.status === 'ok' && `${e.durationMs}ms`}
                {e.status === 'probe' && `없음 (exit ${e.exitCode})`}
                {e.status === 'failed' && `exit ${e.exitCode}`}
                {e.status === 'error' && '오류'}
              </span>
              {copied === e.seq && <span className="badge">복사됨</span>}
              {e.stderr && e.status !== 'probe' && (
                <div className="cmdlog-stderr mono">{e.stderr}</div>
              )}
            </div>
          ))}
          <div ref={endRef} />
        </div>
      )}
    </div>
  )
}
