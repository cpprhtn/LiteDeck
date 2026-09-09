import { useEffect, useState } from 'react'
import { shortStamp } from './datetime'
import { HostLogins, type Login, type LoginsView } from './ipc'
import { AccessNotice } from './EventTimeline'
import { t } from './i18n'

// Who got in, and who kept trying (T-26).
//
// Sits under the live session list because it answers the other half of the
// same question: that table says who is here, this says who has been — and, on
// a server facing the internet, who is still knocking.
//
// The failures are a count, never a list. One measured server logged 3,011
// failed passwords in a day from 36 addresses against 737 different account
// names. A list of that is a list of one attack, cut off at the read limit; the
// shape of it fits in three numbers and two short columns.
//
// One read when the tab opens. The past does not change, and a poller here
// would re-scan a day of journal every few seconds.

function fmtWhen(iso: string): string {
  const d = new Date(iso)
  if (!Number.isFinite(d.getTime())) return '—'
  return shortStamp(d)
}

function lasted(l: Login): string {
  if (l.open) return t('아직 열려 있음')
  if (!l.until) return '—'
  const ms = new Date(l.until).getTime() - new Date(l.at).getTime()
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const mins = Math.round(ms / 60000)
  if (mins < 60) return t('{n}분', { n: mins })
  const hours = Math.floor(mins / 60)
  if (hours < 24) return t('{h}시간 {m}분', { h: hours, m: mins % 60 })
  return t('{d}일 {h}시간', { d: Math.floor(hours / 24), h: hours % 24 })
}

export function LoginHistory({ hostID }: { hostID: string }) {
  const [view, setView] = useState<LoginsView | null>(null)
  const [busy, setBusy] = useState(true)

  const load = (elevate: boolean) => {
    setBusy(true)
    HostLogins(hostID, elevate)
      .then(setView)
      // Quiet on purpose. This is a section under a table that already works;
      // a server that will not answer it should not take the page down.
      .catch(() => {})
      .finally(() => setBusy(false))
  }
  useEffect(() => {
    setView(null)
    load(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hostID])

  if (busy && !view) {
    return <p className="muted small login-hist-empty">{t('접속 이력을 읽는 중…')}</p>
  }
  if (!view) return null

  const auth = view.auth
  const readable = view.access === 'ok'

  return (
    <div className="login-hist">
      <h3 className="login-hist-title">{t('접속 이력')}</h3>

      {/* The failure half needs the journal; the list below does not. So the
          notice belongs here rather than over the whole section. */}
      {!readable && <AccessNotice view={view} busy={busy} onElevate={() => load(true)} />}

      {readable && (
        <div className="login-auth">
          <div className="login-auth-nums">
            <span className="login-auth-fail" data-hot={auth.failed > 0 || undefined}>
              {t('실패 {n}건', { n: auth.failed })}
            </span>
            <span className="muted">{t('성공 {n}건', { n: auth.accepted })}</span>
            <span className="muted small">{t('최근 24시간')}</span>
          </div>

          {auth.failed > 0 && (
            <div className="login-auth-tops">
              <div>
                <div className="login-auth-cap">
                  {t('출처 {shown}/{total}', {
                    shown: auth.sources?.length ?? 0,
                    total: auth.distinctSources,
                  })}
                </div>
                {auth.sources?.map((c) => (
                  <div key={c.name} className="login-auth-row">
                    <span className="mono ellipsis">{c.name}</span>
                    <span className="mono num muted">{c.count}</span>
                  </div>
                ))}
              </div>
              <div>
                <div className="login-auth-cap">
                  {t('시도된 계정 {shown}/{total}', {
                    shown: auth.users?.length ?? 0,
                    total: auth.distinctUsers,
                  })}
                </div>
                {auth.users?.map((c) => (
                  <div key={c.name} className="login-auth-row">
                    <span className="mono ellipsis">{c.name}</span>
                    <span className="mono num muted">{c.count}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      {view.logins.length === 0 ? (
        <p className="muted small login-hist-empty">{t('기록된 접속이 없습니다.')}</p>
      ) : (
        <div className="login-list">
          {view.logins.map((l, i) => (
            <div key={`${l.at}-${i}`} className="login-row" data-boot={l.boot || undefined}>
              <span className="mono login-who">
                {/* A boot is not a person. It is the most useful row in the
                    file and the least like the others, so it is named rather
                    than dressed up as a login.

                    t('재부팅'), which the event timeline already uses for the
                    same thing — not t('재시작'), which is the container tab's
                    button and means "restart it", an instruction rather than a
                    record. */}
                {l.boot ? t('재부팅') : l.user}
              </span>
              <span className="mono small muted login-tty">{l.tty || '—'}</span>
              <span className="mono small muted ellipsis login-from" title={l.from}>
                {l.from || '—'}
              </span>
              <span className="small muted login-at">{fmtWhen(l.at)}</span>
              <span className="small muted num login-for">{lasted(l)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
