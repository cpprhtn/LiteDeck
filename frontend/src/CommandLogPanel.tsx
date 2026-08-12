import { useEffect, useRef, useState } from 'react'
import { ClearCommandLog, GetCommandLog, on, type CommandEntry } from './ipc'
import { ResizeHandle } from './ResizeHandle'
import { usePref } from './prefs'
import { t } from './i18n'

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
  const height = usePref('commandLogHeight')
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
    <div
      className="cmdlog"
      data-open={open || undefined}
      style={open ? { gridTemplateRows: `auto ${height}px` } : undefined}
    >
      {open && <ResizeHandle pref="commandLogHeight" label={t('Command Log 높이')} />}

      <button className="cmdlog-head" onClick={onToggle}>
        <span className="chevron">{open ? '▾' : '▸'}</span>
        <strong>Command Log</strong>
        <span className="muted small">{t('{n}건', { n: shown.length })}</span>
        {failures > 0 && <span className="badge danger">{t('{n} 실패', { n: failures })}</span>}
        <span className="spacer" />
        {open && background.length > 0 && (
          <span
            className="ghost small-btn"
            onClick={(ev) => {
              ev.stopPropagation()
              setShowBackground((v) => !v)
            }}
          >
            {showBackground
              ? t('백그라운드 조회 접기')
              : t('백그라운드 조회 {n}건 보기', { n: background.length })}
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
            {t('지우기')}
          </span>
        )}
      </button>

      {open && (
        <div className="cmdlog-body">
          {shown.length === 0 && (
            <div className="placeholder small">
              {entries.length === 0
                ? t('GUI가 실행하는 모든 명령이 여기에 실시간으로 표시됩니다. 클릭하면 복사됩니다.')
                : t('아직 직접 실행한 명령이 없습니다 — 위에서 백그라운드 조회를 펼쳐볼 수 있습니다.')}
            </div>
          )}
          {shown.map((e) => (
            <div
              key={e.seq}
              className="cmdlog-row"
              data-status={e.status}
              data-kind={e.kind || undefined}
              data-origin={e.origin || undefined}
              onClick={() => void copy(e)}
              title={t('클릭해서 복사')}
            >
              {/* A line an MCP client caused has to be attributable at a
                  glance. The marker replaces the prompt rather than sitting
                  beside it: this entry is a tool call, not a shell command.
                  Labelled MCP rather than AI because that is the word the
                  person set up in their client. */}
              <span className="cmdlog-marker">{e.origin === 'ai' ? 'MCP' : '$'}</span>
              {/* Newlines shown as ⏎ rather than collapsed to whitespace by the
                  browser. A multi-line command such as the metrics script read as
                  `... | head -1 echo '#mem'` — a command that would be broken if
                  you typed it. On the one surface whose whole job is showing
                  exactly what ran, that is not a cosmetic issue. The clipboard
                  gets e.line untouched, so a paste still works. */}
              <span className="mono cmdlog-line">
                {e.line.split('\n').map((part, i) => (
                  <span key={i}>
                    {i > 0 && <span className="cmdlog-nl" title={t('줄바꿈')}>⏎ </span>}
                    {part}
                  </span>
                ))}
              </span>
              <span className="cmdlog-meta muted">
                {e.status === 'running' && t('실행 중')}
                {e.status === 'ok' && `${e.durationMs}ms`}
                {e.status === 'probe' && t('없음 (exit {code})', { code: e.exitCode })}
                {e.status === 'failed' && `exit ${e.exitCode}`}
                {e.status === 'error' && t('오류')}
              
                {/* A command that sat in a queue and one that was genuinely
                    slow read identically without this. */}
                {e.queuedMs ? (
                  <span className="cmdlog-queued" title={t('세션 슬롯을 기다린 시간')}>
                    {t('대기 {n}ms', { n: e.queuedMs })}
                  </span>
                ) : null}
              </span>
              {copied === e.seq && <span className="badge">{t('복사됨')}</span>}
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
