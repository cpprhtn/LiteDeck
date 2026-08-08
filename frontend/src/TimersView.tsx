import { useCallback, useEffect, useRef, useState } from 'react'
import { ListTimers, type Timer } from './ipc'

// Scheduled jobs (v1.x): systemd timers, read-only.
//
// Answers the question people actually have — "was this supposed to run, and
// did it?" Editing a schedule means writing a unit file and reloading the
// daemon, and half-doing that leaves a server with a job that silently never
// runs, so it is deliberately out of scope for now.

const POLL_MS = 10000

function fmtWhen(unix: number): { text: string; overdue: boolean } {
  if (!unix) return { text: '—', overdue: false }
  const now = Date.now() / 1000
  const diff = unix - now
  const abs = Math.abs(diff)

  const unit =
    abs < 60
      ? `${Math.round(abs)}초`
      : abs < 3600
        ? `${Math.round(abs / 60)}분`
        : abs < 86400
          ? `${Math.round(abs / 3600)}시간`
          : `${Math.round(abs / 86400)}일`

  return { text: diff >= 0 ? `${unit} 후` : `${unit} 전`, overdue: false }
}

function fmtAbs(unix: number): string {
  if (!unix) return '없음'
  const d = new Date(unix * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function TimersView({
  hostID,
  visible,
  onError,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [timers, setTimers] = useState<Timer[]>([])
  const [loading, setLoading] = useState(true)
  const inFlight = useRef(false)

  const refresh = useCallback(async () => {
    if (inFlight.current) return
    inFlight.current = true
    try {
      setTimers((await ListTimers(hostID)) ?? [])
    } catch (e) {
      onError(String(e))
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [hostID, onError])

  useEffect(() => {
    if (!visible) return
    void refresh()
    const id = window.setInterval(() => void refresh(), POLL_MS)
    return () => window.clearInterval(id)
  }, [visible, refresh])

  if (loading) return <div className="placeholder">타이머를 읽는 중…</div>
  if (timers.length === 0) {
    return <div className="placeholder">등록된 systemd 타이머가 없습니다.</div>
  }

  const neverRun = timers.filter((t) => !t.last).length

  return (
    <div className="view">
      <div className="view-toolbar">
        <span className="muted small">
          타이머 {timers.length}개
          {neverRun > 0 && ` · 아직 실행된 적 없음 ${neverRun}개`}
        </span>
        <span className="spacer" />
        <button className="ghost" onClick={() => void refresh()}>
          새로고침
        </button>
      </div>

      <div className="table">
        <div className="thead" style={{ gridTemplateColumns: '1fr 120px 120px 1.4fr' }}>
          <div>TIMER</div>
          <div>다음 실행</div>
          <div>마지막 실행</div>
          <div>ACTIVATES</div>
        </div>
        <div className="tbody" style={{ overflowY: 'auto' }}>
          {timers.map((t) => {
            const next = fmtWhen(t.next)
            const last = fmtWhen(t.last)
            return (
              <div
                key={t.unit}
                className="trow net-row"
                style={{
                  position: 'static',
                  transform: 'none',
                  gridTemplateColumns: '1fr 120px 120px 1.4fr',
                  height: 'auto',
                  paddingTop: 4,
                  paddingBottom: 4,
                }}
              >
                <div className="ellipsis">
                  <span className="mono">{t.unit}</span>
                  {t.description && (
                    <div className="muted small ellipsis">{t.description}</div>
                  )}
                </div>
                <div className="mono small" title={fmtAbs(t.next)}>
                  {next.text}
                </div>
                <div className="mono small" title={fmtAbs(t.last)}>
                  {t.last ? (
                    last.text
                  ) : (
                    // Never having run is not a fault — a daily timer installed
                    // an hour ago is simply waiting — but it is the first thing
                    // to check when a job "did not happen".
                    <span className="muted">한 번도 없음</span>
                  )}
                </div>
                <div className="ellipsis muted mono">{t.activates}</div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
