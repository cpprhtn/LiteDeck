import { useCallback, useEffect, useRef, useState } from 'react'
import { usePoll } from './usePoll'
import { ListTimers, type Timer } from './ipc'
import { t as tr } from './i18n'

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
      ? tr('{n}초', { n: Math.round(abs) })
      : abs < 3600
        ? tr('{n}분', { n: Math.round(abs / 60) })
        : abs < 86400
          ? tr('{n}시간', { n: Math.round(abs / 3600) })
          : tr('{n}일', { n: Math.round(abs / 86400) })

  return {
    text: diff >= 0 ? tr('{unit} 후', { unit }) : tr('{unit} 전', { unit }),
    overdue: false,
  }
}

function fmtAbs(unix: number): string {
  if (!unix) return tr('없음')
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

  usePoll(refresh, POLL_MS, visible)

  if (loading) return <div className="placeholder">{tr('타이머를 읽는 중…')}</div>
  if (timers.length === 0) {
    return <div className="placeholder">{tr('등록된 systemd 타이머가 없습니다.')}</div>
  }

  const neverRun = timers.filter((t) => !t.last).length

  return (
    <div className="view">
      <div className="view-toolbar">
        <span className="muted small">
          {tr('타이머 {n}개', { n: timers.length })}
          {neverRun > 0 && tr(' · 아직 실행된 적 없음 {n}개', { n: neverRun })}
        </span>
        <span className="spacer" />
        <button className="ghost" onClick={() => void refresh()}>
          {tr('새로고침')}
        </button>
      </div>

      <div className="table">
        <div className="thead" style={{ gridTemplateColumns: '1fr 120px 120px 1.4fr' }}>
          <div>TIMER</div>
          <div>{tr('다음 실행')}</div>
          <div>{tr('마지막 실행')}</div>
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
                    <span className="muted">{tr('한 번도 없음')}</span>
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
