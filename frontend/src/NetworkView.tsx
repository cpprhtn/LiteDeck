import { useCallback, useEffect, useRef, useState } from 'react'
import { usePoll } from './usePoll'
import {
  HostNetwork,
  SSHDConfig,
  type NetworkView as NetView,
  type SSHDNote,
  type SSHDReport,
} from './ipc'
import { t } from './i18n'

// The network view (v1.x). Answers the two questions people actually open a
// network tab for — "what address is this box on" and "what is listening on
// that port" — and stops there.

const POLL_MS = 5000

export function NetworkView({
  hostID,
  visible,
  onError,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [net, setNet] = useState<NetView | null>(null)
  const [loading, setLoading] = useState(true)
  const [onlyExposed, setOnlyExposed] = useState(false)
  const inFlight = useRef(false)

  const refresh = useCallback(async () => {
    if (inFlight.current) return
    inFlight.current = true
    try {
      setNet(await HostNetwork(hostID))
    } catch (e) {
      onError(String(e))
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [hostID, onError])

  usePoll(refresh, POLL_MS, visible)

  if (loading && !net) {
    return <div className="placeholder">{t('네트워크 상태를 읽는 중…')}</div>
  }

  const interfaces = net?.interfaces ?? []
  const listeners = (net?.listeners ?? []).filter((l) => !onlyExposed || l.exposed)
  const exposedCount = (net?.listeners ?? []).filter((l) => l.exposed).length

  return (
    <div className="view net-view">
      <div className="view-toolbar">
        <div className="segmented">
          <button data-on={!onlyExposed || undefined} onClick={() => setOnlyExposed(false)}>
            {t('전체 {n}', { n: net?.listeners.length ?? 0 })}
          </button>
          <button
            data-on={onlyExposed || undefined}
            data-danger={exposedCount > 0 || undefined}
            onClick={() => setOnlyExposed(true)}
          >
            {t('외부 노출 {n}', { n: exposedCount })}
          </button>
        </div>
        <span className="spacer" />
        <button className="ghost" onClick={() => void refresh()}>
          {t('새로고침')}
        </button>
      </div>

      {(net?.warnings.length ?? 0) > 0 && (
        <div className="net-warnings">
          {net!.warnings.map((w, i) => (
            <div key={i} className="muted small">
              ⚠ {w}
            </div>
          ))}
        </div>
      )}

      <div className="net-body">
        <section>
          <h3 className="net-heading">{t('인터페이스')}</h3>
          {interfaces.length === 0 && (
            <div className="placeholder small">{t('인터페이스를 읽지 못했습니다.')}</div>
          )}
          <div className="net-cards">
            {interfaces.map((i) => {
              const up = i.state === 'UP' || (i.loopback && i.state === 'UNKNOWN')
              return (
                <div key={i.name} className="card" data-running={up || undefined}>
                  <div className="card-head">
                    <span className="dot" data-status={up ? 'active' : undefined} />
                    <strong className="mono">{i.name}</strong>
                    <span className="muted small">
                      {i.state}
                      {i.loopback && ' · loopback'}
                      {i.mtu > 0 && ` · MTU ${i.mtu}`}
                    </span>
                  </div>
                  <div className="net-addrs mono small">
                    {i.addresses.length === 0 && <span className="muted">{t('주소 없음')}</span>}
                    {i.addresses.map((a) => (
                      <div key={`${a.family}-${a.address}`}>
                        <span className="muted">{a.family === 'inet6' ? 'v6' : 'v4'}</span>{' '}
                        {a.address}/{a.prefix}
                      </div>
                    ))}
                  </div>
                  {i.mac && <div className="muted small mono">{i.mac}</div>}
                </div>
              )
            })}
          </div>
        </section>

        <section>
          <h3 className="net-heading">
            {t('열린 포트')}
            <span className="muted small">
              {' '}
              — <strong>{t('외부 노출')}</strong>{t('은 0.0.0.0/[::]에 바인딩되어 다른 기기에서 닿을 수 있다는 뜻입니다')}
            </span>
          </h3>
          {listeners.length === 0 && (
            <div className="placeholder small">
              {onlyExposed ? t('외부에 노출된 포트가 없습니다.') : t('열린 포트가 없습니다.')}
            </div>
          )}
          {listeners.length > 0 && (
            <div className="table net-table">
              <div className="thead" style={{ gridTemplateColumns: '64px 80px 1fr 1fr 90px' }}>
                <div>PROTO</div>
                <div className="num">PORT</div>
                <div>BIND</div>
                <div>PROCESS</div>
                <div>{t('노출')}</div>
              </div>
              {listeners.map((l, i) => (
                <div
                  key={`${l.protocol}-${l.address}-${l.port}-${i}`}
                  className="trow net-row"
                  style={{ gridTemplateColumns: '64px 80px 1fr 1fr 90px' }}
                >
                  <div className="mono">{l.protocol}</div>
                  <div className="num mono">{l.port}</div>
                  <div className="mono ellipsis muted">
                    {l.address}
                    {l.ipv6 && <span className="badge">v6</span>}
                  </div>
                  <div className="ellipsis">
                    {l.process ? (
                      <>
                        {l.process}
                        {l.pid ? <span className="muted mono"> ({l.pid})</span> : null}
                      </>
                    ) : (
                      <span className="muted">—</span>
                    )}
                  </div>
                  <div>
                    {l.exposed ? (
                      <span className="badge warn">{t('외부')}</span>
                    ) : (
                      <span className="muted small">{t('로컬만')}</span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>

        <SSHDSection hostID={hostID} visible={visible} />
      </div>
    </div>
  )
}

/**
 * What the server's sshd configuration declares (§4.4).
 *
 * Read once when the tab is first opened, not on the poll the rest of this view
 * runs on: a config file does not change while you watch it, and every poll is
 * something the server pays for (§3.2d).
 */
function SSHDSection({ hostID, visible }: { hostID: string; visible: boolean }) {
  const [report, setReport] = useState<SSHDReport | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [showAll, setShowAll] = useState(false)

  const load = useCallback(async () => {
    setBusy(true)
    try {
      setReport(await SSHDConfig(hostID))
      setError(null)
    } catch (e) {
      // Its own error line rather than the view's: a server whose sshd_config
      // cannot be read still has interfaces and ports worth showing.
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }, [hostID])

  const asked = useRef('')
  useEffect(() => {
    if (!visible || asked.current === hostID) return
    asked.current = hostID
    void load()
  }, [visible, hostID, load])

  const notes = report?.notes ?? []
  const warns = notes.filter((n) => n.level === 'warn')

  return (
    <section>
      <h3 className="net-heading">
        {t('SSH 설정')}
        {warns.length > 0 && <span className="badge warn">{t('{n}건', { n: warns.length })}</span>}
        <span className="spacer" />
        <button className="ghost small-btn" disabled={busy} onClick={() => void load()}>
          {t('새로고침')}
        </button>
      </h3>

      {error && <div className="placeholder small">{error}</div>}
      {!error && !report && (
        <div className="placeholder small">{t('sshd 설정을 읽는 중…')}</div>
      )}

      {report && (
        <>
          <p className="muted small">
            {t('설정 파일에 적힌 값만 읽습니다 — 파일이 정하지 않은 항목은 sshd 기본값이 적용되고, 그 값은 배포판마다 달라 여기서 추측하지 않습니다.')}
          </p>

          {notes.length === 0 && (
            <div className="placeholder small">{t('짚을 만한 설정이 없습니다.')}</div>
          )}
          <div className="sshd-notes">
            {notes.map((n, i) => (
              <div key={`${n.code}-${i}`} className="sshd-note" data-level={n.level}>
                <span className={n.level === 'warn' ? 'badge warn' : 'badge'}>
                  {n.level === 'warn' ? t('확인 필요') : t('참고')}
                </span>
                <span>{noteText(n)}</span>
                <span className="muted small mono ellipsis">
                  {n.file}:{n.line}
                </span>
              </div>
            ))}
          </div>

          {(report.matches?.length ?? 0) > 0 && (
            <p className="muted small">
              {t('Match 블록 {n}개가 있습니다 — 그 안의 값은 해당 블록이 지목한 사용자·주소에만 적용되므로 위 목록에 넣지 않았습니다.', {
                n: report.matches!.length,
              })}
              {' '}
              <span className="mono">{report.matches!.map((m) => m.value).join(' · ')}</span>
            </p>
          )}

          {(report.unreadable?.length ?? 0) > 0 && (
            <p className="warn-text small">
              {t('sshd 가 읽는 파일 중 이 계정이 열 수 없는 것이 있습니다 — 아래 내용은 완전하지 않습니다: {files}', {
                files: report.unreadable!.join(', '),
              })}
            </p>
          )}

          <button className="ghost small-btn" onClick={() => setShowAll((v) => !v)}>
            {showAll
              ? t('설정값 접기')
              : t('설정값 {n}개 보기', { n: report.declared.length })}
          </button>
          {showAll && (
            <div className="table net-table">
              <div className="thead" style={{ gridTemplateColumns: '1fr 1fr 220px' }}>
                <div>KEYWORD</div>
                <div>VALUE</div>
                <div>FILE</div>
              </div>
              {report.declared.map((d) => (
                <div
                  key={`${d.file}:${d.line}`}
                  className="trow net-row"
                  style={{ gridTemplateColumns: '1fr 1fr 220px' }}
                >
                  <div className="mono ellipsis">{d.keyword}</div>
                  <div className="mono ellipsis">{d.value}</div>
                  <div className="mono ellipsis muted" title={d.file}>
                    {d.file}:{d.line}
                  </div>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </section>
  )
}

/** The sentence for one finding. Codes come from Go; the wording lives here. */
function noteText(n: SSHDNote): string {
  switch (n.code) {
    case 'permit-root-login':
      return t('root 로 직접 로그인할 수 있습니다 (PermitRootLogin yes).')
    case 'permit-empty-passwords':
      return t('빈 비밀번호로 로그인할 수 있습니다 (PermitEmptyPasswords yes).')
    case 'max-sessions-low':
      // The one finding that is about this app: LiteDeck opens one SFTP
      // channel, up to five for terminals and log tails, and three for
      // commands. Below ten, tabs start failing to open for a reason that
      // looks like a bug in the app.
      return t('MaxSessions 가 {value} 입니다 — LiteDeck 은 한 서버에 채널을 최대 9개 씁니다. 터미널이나 로그 창이 열리지 않는다면 이것이 원인입니다.', {
        value: n.value,
      })
    case 'password-authentication':
      return t('비밀번호 인증이 켜져 있습니다 (PasswordAuthentication yes).')
    case 'port':
      return t('sshd 가 {value} 번 포트에서 듣습니다.', { value: n.value })
    case 'x11-forwarding':
      return t('X11 전달이 켜져 있습니다 (X11Forwarding yes).')
    case 'access-list':
      return t('접속 허용·차단 목록이 있습니다: {value}', { value: n.value })
    default:
      return n.code
  }
}
