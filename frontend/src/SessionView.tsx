import { useCallback, useEffect, useRef, useState } from 'react'
import { usePoll } from './usePoll'
import { EndSSHSession, ListSSHSessions, type ActionResult, type SSHSession } from './ipc'

// Who is logged in to this server over SSH, and cutting them off.
//
// The one thing this view must never do is let someone end the connection they
// are using. The button is disabled for those rows, but that is only a courtesy —
// the binding refuses regardless, because a disabled button is a suggestion and a
// direct call walks past it.

const POLL_MS = 5000

function fmtElapsed(sec: number): string {
  if (sec <= 0) return '방금'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}일 ${h}시간`
  if (h > 0) return `${h}시간 ${m}분`
  if (m > 0) return `${m}분`
  return `${sec}초`
}

export function SessionView({
  hostID,
  visible,
  onError,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [sessions, setSessions] = useState<SSHSession[]>([])
  const [loading, setLoading] = useState(true)
  const [confirm, setConfirm] = useState<SSHSession | null>(null)
  const [pending, setPending] = useState<number | null>(null)
  const inFlight = useRef(false)

  const refresh = useCallback(async () => {
    if (inFlight.current) return
    inFlight.current = true
    try {
      setSessions((await ListSSHSessions(hostID)) ?? [])
    } catch (e) {
      onError(String(e))
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [hostID, onError])

  usePoll(refresh, POLL_MS, visible)

  const end = async (s: SSHSession) => {
    setConfirm(null)
    setPending(s.pid)
    try {
      const res: ActionResult = await EndSSHSession(hostID, s.pid)
      if (!res.ok) {
        onError(res.error ?? '세션을 종료하지 못했습니다')
        return
      }
      await refresh()
    } catch (e) {
      onError(String(e))
    } finally {
      setPending(null)
    }
  }

  if (loading) return <div className="placeholder">세션을 읽는 중…</div>
  if (sessions.length === 0) {
    return <div className="placeholder">SSH 세션이 없습니다.</div>
  }

  const others = sessions.filter((s) => !s.self).length
  const cols = '1fr 90px 1.3fr 90px 90px 1fr 90px'

  return (
    <div className="view">
      <div className="view-toolbar">
        <span className="muted small">
          세션 {sessions.length}개
          {others > 0 && ` · 내 접속 외 ${others}개`}
        </span>
        <span className="spacer" />
        <button className="ghost" onClick={() => void refresh()}>
          새로고침
        </button>
      </div>

      <div className="table">
        <div className="thead" style={{ gridTemplateColumns: cols }}>
          <div>사용자</div>
          <div>단말</div>
          <div>접속 위치</div>
          <div className="num">경과</div>
          <div className="num">유휴</div>
          <div>실행 중</div>
          <div />
        </div>
        <div className="tbody" style={{ overflowY: 'auto' }}>
          {sessions.map((s) => (
            <div
              key={s.pid}
              className="trow net-row"
              style={{
                position: 'static',
                transform: 'none',
                gridTemplateColumns: cols,
                height: 'auto',
                paddingTop: 4,
                paddingBottom: 4,
              }}
            >
              <div className="ellipsis">
                <span className="mono">{s.user}</span>
                {s.self && (
                  <span className="badge" title="LiteDeck이 지금 쓰고 있는 접속입니다">
                    내 접속
                  </span>
                )}
              </div>
              <div className="mono small muted">{s.tty || '—'}</div>
              <div className="ellipsis mono small muted" title={s.from}>
                {/* Blank when ss could not attach the process, which needs
                    privileges for other users' sockets. A missing column beats a
                    guessed one. */}
                {s.from || '—'}
              </div>
              <div className="num mono small">{fmtElapsed(s.elapsed)}</div>
              <div className="num mono small muted">{s.idle || '—'}</div>
              <div className="ellipsis muted mono small">{s.what || '—'}</div>
              <div>
                <button
                  className="ghost small-btn"
                  disabled={s.self || pending === s.pid}
                  title={
                    s.self
                      ? '이 접속을 끊으면 LiteDeck과 서버의 연결이 끊깁니다'
                      : undefined
                  }
                  onClick={() => setConfirm(s)}
                >
                  끊기
                </button>
              </div>
            </div>
          ))}
        </div>
      </div>

      {confirm && (
        <div className="scrim">
          <div className="dialog">
            <h2>이 세션을 끊으시겠습니까?</h2>
            <p className="muted">
              해당 사용자의 셸이 즉시 종료됩니다. 저장하지 않은 작업은 사라지고,
              실행 중이던 명령은 중단됩니다.
            </p>
            <dl className="keyinfo">
              <dt>사용자</dt>
              <dd className="mono">{confirm.user}</dd>
              <dt>단말</dt>
              <dd className="mono">{confirm.tty || '(터미널 없음 — 명령 또는 전송)'}</dd>
              {confirm.from && (
                <>
                  <dt>접속 위치</dt>
                  <dd className="mono selectable">{confirm.from}</dd>
                </>
              )}
              <dt>PID</dt>
              <dd className="mono">{confirm.pid}</dd>
            </dl>
            <div className="dialog-actions">
              <button onClick={() => setConfirm(null)}>취소</button>
              <button className="danger" onClick={() => void end(confirm)}>
                끊기
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
