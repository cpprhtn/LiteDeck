import { useEffect, useRef, useState } from 'react'
import { StopLogStream, on, type LogLine, type LogStream } from './ipc'
import { ResizeHandle } from './ResizeHandle'
import { usePref } from './prefs'
import { t } from './i18n'

// Live log output (§4.3, §4.5).
//
// Follows rather than polls: re-reading the last 500 lines every few seconds
// re-transfers what the client already has and still shows it late.

const MAX_LINES = 5000

export function LogPanel({
  stream,
  onClose,
}: {
  stream: LogStream
  onClose: () => void
}) {
  const [lines, setLines] = useState<LogLine[]>([])
  const [ended, setEnded] = useState<string | null>(null)
  const [follow, setFollow] = useState(true)
  const [filter, setFilter] = useState('')
  // Shared across services and containers: it is one slot in the window, and a
  // height set while reading a unit's log should hold when the next one opens.
  const height = usePref('liveLogHeight')
  const bodyRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const offData = on<LogLine>(`log:data:${stream.id}`, (line) =>
      // Bounded: a chatty unit would otherwise grow the DOM without limit and
      // the window would slow to a crawl over a long session.
      setLines((prev) => (prev.length >= MAX_LINES ? [...prev.slice(1), line] : [...prev, line])),
    )
    const offExit = on<string>(`log:exit:${stream.id}`, (msg) => setEnded(msg || t('스트림 종료')))
    return () => {
      offData()
      offExit()
      void StopLogStream(stream.id).catch(() => {})
    }
  }, [stream.id])

  // Auto-scroll, but only while the user is already at the bottom. Yanking the
  // view back down while someone is reading history is the single most
  // irritating thing a log viewer can do.
  useEffect(() => {
    if (!follow || !bodyRef.current) return
    bodyRef.current.scrollTop = bodyRef.current.scrollHeight
  }, [lines, follow])

  const onScroll = () => {
    const el = bodyRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
    setFollow(atBottom)
  }

  const needle = filter.trim().toLowerCase()
  const shown = needle
    ? lines.filter((l) => l.text.toLowerCase().includes(needle))
    : lines

  return (
    <div className="logpanel" style={{ height }}>
      <ResizeHandle pref="liveLogHeight" label={t('로그 창 높이')} />
      <div className="logpanel-head">
        <strong className="mono ellipsis">{stream.title}</strong>
        <span className="muted small">{t('{n}줄', { n: lines.length })}</span>
        {ended && <span className="badge warn">{ended}</span>}
        <input
          className="search"
          placeholder={t('필터')}
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
        {!follow && (
          <button
            className="ghost small-btn"
            onClick={() => {
              setFollow(true)
              if (bodyRef.current) {
                bodyRef.current.scrollTop = bodyRef.current.scrollHeight
              }
            }}
          >
            {t('↓ 최신으로')}
          </button>
        )}
        <button
          className="ghost small-btn"
          onClick={() => {
            void navigator.clipboard.writeText(shown.map((l) => l.text).join('\n'))
          }}
        >
          {t('복사')}
        </button>
        <button className="ghost small-btn" onClick={onClose}>
          {t('닫기')}
        </button>
      </div>

      <div className="logpanel-body mono" ref={bodyRef} onScroll={onScroll}>
        {shown.length === 0 && (
          <div className="placeholder small">
            {lines.length === 0 ? t('로그를 기다리는 중…') : t('필터에 맞는 줄이 없습니다.')}
          </div>
        )}
        {shown.map((l, i) => (
          <div key={i} className="logline" data-stderr={l.stderr || undefined}>
            {l.text}
          </div>
        ))}
      </div>
    </div>
  )
}
