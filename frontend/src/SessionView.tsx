import { useCallback, useEffect, useRef, useState } from 'react'
import { Scrim } from './Scrim'
import { usePoll } from './usePoll'
import { LoginHistory } from './LoginHistory'
import { EndSSHSession, ListSSHSessions, type ActionResult, type SSHSession } from './ipc'
import { t } from './i18n'

// Who is logged in to this server over SSH, and cutting them off.
//
// The one thing this view must never do is let someone end the connection they
// are using. The button is disabled for those rows, but that is only a courtesy —
// the binding refuses regardless, because a disabled button is a suggestion and a
// direct call walks past it.

const POLL_MS = 5000

function fmtElapsed(sec: number): string {
  if (sec <= 0) return t('방금')
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return t('{d}일 {h}시간', { d, h })
  if (h > 0) return t('{h}시간 {m}분', { h, m })
  if (m > 0) return t('{m}분', { m })
  return t('{n}초', { n: sec })
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
        onError(res.error ?? t('세션을 종료하지 못했습니다'))
        return
      }
      await refresh()
    } catch (e) {
      onError(String(e))
    } finally {
      setPending(null)
    }
  }

  // The history below is rendered whatever the live table says. "Nobody is
  // logged in" is exactly the moment somebody wants to know who was, and
  // returning early here hid it.
  if (loading) {
    return (
      <div className="view">
        <div className="placeholder">{t('세션을 읽는 중…')}</div>
        <LoginHistory hostID={hostID} />
      </div>
    )
  }
  if (sessions.length === 0) {
    return (
      <div className="view">
        <div className="placeholder">{t('SSH 세션이 없습니다.')}</div>
        <LoginHistory hostID={hostID} />
      </div>
    )
  }

  const others = sessions.filter((s) => !s.self).length

  // Three of these columns are filled from `w` and `who`, and a host can leave
  // every one of them empty: a container writes no utmp, and Windows has no
  // equivalent at all — there a terminal is not a pts device and there is no
  // idle time to read. Six columns of "—" look like a broken table rather than
  // an honest one, so a column nothing fills is not drawn.
  const has = (pick: (s: SSHSession) => string | undefined) => sessions.some((s) => !!pick(s))
  const showTTY = has((s) => s.tty)
  const showIdle = has((s) => s.idle)
  const showWhat = has((s) => s.what)
  const cols = [
    '1fr',
    showTTY && '90px',
    '1.3fr',
    '90px',
    showIdle && '90px',
    showWhat && '1fr',
    '90px',
  ]
    .filter(Boolean)
    .join(' ')

  return (
    <div className="view">
      <div className="view-toolbar">
        <span className="muted small">
          {t('세션 {n}개', { n: sessions.length })}
          {others > 0 && t(' · 내 접속 외 {n}개', { n: others })}
        </span>
        <span className="spacer" />
        <button className="ghost" onClick={() => void refresh()}>
          {t('새로고침')}
        </button>
      </div>

      {/* Sized to its content, not to the space. The history below is the long
          half of this tab, and a table with `flex: 1` claims the room first and
          then spills its rows over whatever follows when there is not enough. */}
      <div className="table session-table">
        <div className="thead" style={{ gridTemplateColumns: cols }}>
          <div>{t('사용자')}</div>
          {showTTY && <div>{t('단말')}</div>}
          <div>{t('접속 위치')}</div>
          <div className="num">{t('경과')}</div>
          {showIdle && <div className="num">{t('유휴')}</div>}
          {showWhat && <div>{t('실행 중')}</div>}
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
                  <span className="badge" title={t('LiteDeck이 지금 쓰고 있는 접속입니다')}>
                    {t('내 접속')}
                  </span>
                )}
              </div>
              {showTTY && <div className="mono small muted">{s.tty || '—'}</div>}
              <div className="ellipsis mono small muted" title={s.from}>
                {/* Blank when ss could not attach the process, which needs
                    privileges for other users' sockets. A missing column beats a
                    guessed one. */}
                {s.from || '—'}
              </div>
              <div className="num mono small">{fmtElapsed(s.elapsed)}</div>
              {showIdle && <div className="num mono small muted">{s.idle || '—'}</div>}
              {showWhat && <div className="ellipsis muted mono small">{s.what || '—'}</div>}
              <div>
                <button
                  className="ghost small-btn"
                  disabled={s.self || pending === s.pid}
                  title={
                    s.self
                      ? t('이 접속을 끊으면 LiteDeck과 서버의 연결이 끊깁니다')
                      : undefined
                  }
                  onClick={() => setConfirm(s)}
                >
                  {t('끊기')}
                </button>
              </div>
            </div>
          ))}
        </div>
      </div>

      <LoginHistory hostID={hostID} />

      {confirm && (
        <Scrim onClose={() => setConfirm(null)}>
          <div className="dialog">
            <h2>{t('이 세션을 끊으시겠습니까?')}</h2>
            <p className="muted">
              {t('해당 사용자의 셸이 즉시 종료됩니다. 저장하지 않은 작업은 사라지고, 실행 중이던 명령은 중단됩니다.')}
            </p>
            <dl className="keyinfo">
              <dt>{t('사용자')}</dt>
              <dd className="mono">{confirm.user}</dd>
              {confirm.tty && (
                <>
                  <dt>{t('단말')}</dt>
                  <dd className="mono">{confirm.tty}</dd>
                </>
              )}
              {confirm.from && (
                <>
                  <dt>{t('접속 위치')}</dt>
                  <dd className="mono selectable">{confirm.from}</dd>
                </>
              )}
              <dt>PID</dt>
              <dd className="mono">{confirm.pid}</dd>
            </dl>
            <div className="dialog-actions">
              <button onClick={() => setConfirm(null)}>{t('취소')}</button>
              <button className="danger" onClick={() => void end(confirm)}>
                {t('끊기')}
              </button>
            </div>
          </div>
        </Scrim>
      )}
    </div>
  )
}
