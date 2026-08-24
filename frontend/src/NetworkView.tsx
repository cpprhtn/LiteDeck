import { useCallback, useEffect, useRef, useState } from 'react'
import { usePoll } from './usePoll'
import {
  CloseKumaTunnel,
  DetectKuma,
  HostNetwork,
  KumaConfig,
  OpenKumaTunnel,
  SetKumaConfig,
  SSHDConfig,
  on,
  type KumaCandidate,
  type KumaView,
  type NetworkView as NetView,
  type SSHDNote,
  type SSHDReport,
  type TunnelView,
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
          <button data-on={onlyExposed || undefined} onClick={() => setOnlyExposed(true)}>
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
          {/* No explanation, and no warning colour. Binding to 0.0.0.0 is a
              thing people do on purpose, and a GUI that editorialises about it
              is talking down to the person who typed it. The amber badge is
              enough: it makes the wildcard binds scannable, and the BIND column
              beside it already says which address that is. */}
          <h3 className="net-heading">{t('열린 포트')}</h3>
          {listeners.length === 0 && (
            <div className="placeholder small">
              {onlyExposed ? t('외부에 노출된 포트가 없습니다.') : t('열린 포트가 없습니다.')}
            </div>
          )}
          {listeners.length > 0 && (
            <div className="table net-table">
              <div className="thead" style={{ gridTemplateColumns: '64px 80px 1fr 1fr' }}>
                <div>PROTO</div>
                <div className="num">PORT</div>
                <div>BIND</div>
                <div>PROCESS</div>
              </div>
              {listeners.map((l, i) => (
                <div
                  key={`${l.protocol}-${l.address}-${l.port}-${i}`}
                  className="trow net-row"
                  style={{ gridTemplateColumns: '64px 80px 1fr 1fr' }}
                >
                  <div className="mono">{l.protocol}</div>
                  <div className="num mono">{l.port}</div>
                  {/* The colour sits on the address rather than in a column of
                      its own at the far edge: scanning the list, that is where
                      the eye already is. It marks a wildcard bind and says
                      nothing about it — 0.0.0.0 is a thing people type on
                      purpose. */}
                  <div className="mono ellipsis muted" data-wildcard={l.exposed || undefined}>
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
                </div>
              ))}
            </div>
          )}
        </section>

        <KumaSection hostID={hostID} visible={visible} onError={onError} />

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
      {/* The caveat lives on hover, not in the layout. It is true and worth
          having somewhere — this reads the files, so it knows nothing about the
          defaults that apply where they are silent — but a paragraph of it above
          the findings is read once by everybody and needed by almost nobody.
          The long form is in docs/security.md. */}
      <h3
        className="net-heading"
        title={t('설정 파일에 적힌 값만 읽습니다. 파일이 정하지 않은 항목에는 sshd 기본값이 적용되며, 그 값은 배포판마다 다릅니다.')}
      >
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
          {notes.length === 0 && (
            <div className="placeholder small">{t('설정 파일에서 짚을 것이 없습니다.')}</div>
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

/**
 * Uptime Kuma, reached over the SSH session that is already open.
 *
 * There is no dashboard here and there will not be one. Kuma's own UI is
 * finished, and a summary of it in this window would be a worse copy of
 * something that exists. The single thing this window can do that Kuma cannot
 * do for itself is *reach* an instance that is bound to 127.0.0.1 on purpose —
 * so that is all the section offers: find it, forward a port to it, open it.
 *
 * Detection costs an HTTP request per candidate over the connection, so it runs
 * when the tab is opened and when the user asks again — never on the poll the
 * rest of this view runs on. A service does not appear while you watch it, and
 * every probe is something the server pays for (§3.2d).
 */
function KumaSection({
  hostID,
  visible,
  onError,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [view, setView] = useState<KumaView | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [port, setPort] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [opening, setOpening] = useState<number | null>(null)

  const detect = useCallback(async () => {
    setBusy(true)
    try {
      setView(await DetectKuma(hostID))
      setError(null)
    } catch (e) {
      // Its own line rather than the view's: a probe that could not run still
      // leaves a network tab worth looking at.
      setError(String(e))
      // The stored port and any open tunnel are known without the server, and
      // are what the settings form needs to render.
      try {
        setView(await KumaConfig(hostID))
      } catch {
        /* the binding layer is gone; the error above already says so */
      }
    } finally {
      setBusy(false)
    }
  }, [hostID])

  const asked = useRef('')
  useEffect(() => {
    if (!visible || asked.current === hostID) return
    asked.current = hostID
    void detect()
  }, [visible, hostID, detect])

  // A tunnel can end without anybody clicking — the connection drops, or the
  // host is disconnected from the sidebar. Listening beats re-probing: the
  // answer arrives immediately and costs the server nothing.
  useEffect(() => {
    const apply = (fn: (prev: TunnelView[]) => TunnelView[]) =>
      setView((v) => (v ? { ...v, tunnels: fn(v.tunnels) } : v))

    const offOpen = on<TunnelView>(`tunnel:opened:${hostID}`, (tn) =>
      apply((prev) => [...prev.filter((p) => p.id !== tn.id), tn]),
    )
    const offClose = on<TunnelView>(`tunnel:closed:${hostID}`, (tn) =>
      apply((prev) => prev.filter((p) => p.id !== tn.id)),
    )
    return () => {
      offOpen()
      offClose()
    }
  }, [hostID])

  const open = async (p: number) => {
    setOpening(p)
    try {
      const tn = await OpenKumaTunnel(hostID, p)
      setView((v) =>
        v ? { ...v, tunnels: [...v.tunnels.filter((x) => x.id !== tn.id), tn] } : v,
      )
      setError(null)
    } catch (e) {
      setError(String(e))
    } finally {
      setOpening(null)
    }
  }

  const close = async (id: string) => {
    try {
      await CloseKumaTunnel(id)
      setView((v) => (v ? { ...v, tunnels: v.tunnels.filter((t) => t.id !== id) } : v))
    } catch (e) {
      onError(String(e))
    }
  }

  const startEditing = () => {
    setPort(view?.configured ? String(view.port) : '')
    setApiKey('')
    setEditing(true)
  }

  const save = async () => {
    const n = port.trim() === '' ? 0 : Number(port)
    if (!Number.isInteger(n) || n < 0 || n > 65535) {
      setError(t('포트는 1 에서 65535 사이의 숫자여야 합니다.'))
      return
    }
    setBusy(true)
    try {
      // An untouched key field means "leave what is stored alone". Anything
      // else would erase a secret the form was never given in the first place.
      const res = await SetKumaConfig(hostID, n, apiKey, apiKey === '')
      if (!res.ok) {
        setError(res.error ?? t('설정을 저장하지 못했습니다.'))
        return
      }
      setEditing(false)
      setApiKey('')
      await detect()
    } finally {
      setBusy(false)
    }
  }

  const candidates = view?.candidates ?? []
  const tunnels = view?.tunnels ?? []
  const exposed = view?.exposed ?? []
  const tunnelFor = (p: number) => tunnels.find((tn) => tn.remotePort === p)
  const nothingFound =
    candidates.length === 0 && tunnels.length === 0 && exposed.length === 0

  return (
    <section>
      <h3 className="net-heading">
        Uptime Kuma
        <span className="spacer" />
        <button className="ghost small-btn" disabled={busy} onClick={() => void detect()}>
          {t('다시 찾기')}
        </button>
        <button className="ghost small-btn" onClick={() => (editing ? setEditing(false) : startEditing())}>
          {editing ? t('설정 접기') : t('설정')}
        </button>
      </h3>

      {error && <div className="placeholder small">{error}</div>}

      {editing && (
        <div className="kuma-form">
          <label>
            <span className="muted small">{t('포트')}</span>
            <input
              className="mono"
              inputMode="numeric"
              placeholder={String(view?.port ?? 3001)}
              value={port}
              onChange={(e) => setPort(e.target.value)}
            />
          </label>
          <label>
            <span className="muted small">{t('API 키 (MCP 조회용, 선택)')}</span>
            <input
              type="password"
              className="mono"
              autoComplete="off"
              placeholder={view?.hasApiKey ? t('저장되어 있음 — 바꾸려면 입력') : ''}
              value={apiKey}
              disabled={!view?.keychainOk}
              onChange={(e) => setApiKey(e.target.value)}
            />
          </label>
          <div className="kuma-form-actions">
            <button className="ghost small-btn" onClick={() => setEditing(false)}>
              {t('취소')}
            </button>
            <button className="small-btn" disabled={busy} onClick={() => void save()}>
              {t('저장')}
            </button>
          </div>
          {/* The key is only ever sent to Kuma itself, over this connection. It
              is worth saying out loud on the one form in the app that asks for
              a credential belonging to something other than the server. */}
          <p className="muted small">
            {view?.keychainOk
              ? t('API 키는 이 컴퓨터의 자격증명 저장소에만 저장되고, Kuma 의 /metrics 를 읽을 때만 쓰입니다. MCP 조회 도구에 필요하며 터널에는 필요하지 않습니다.')
              : t('이 컴퓨터에는 자격증명 저장소가 없어 API 키를 보관할 수 없습니다. 터널 열기는 키 없이 됩니다.')}
          </p>
        </div>
      )}

      {busy && !view && <div className="placeholder small">{t('Uptime Kuma 를 찾는 중…')}</div>}

      {!busy && nothingFound && (
        <div className="placeholder small">
          {t('127.0.0.1 에서만 듣는 Uptime Kuma 를 찾지 못했습니다. 기본 포트(3001)가 아니라면 설정에서 포트를 지정하세요.')}
        </div>
      )}

      {candidates.map((c) => (
        <KumaRow
          key={c.port}
          candidate={c}
          tunnel={tunnelFor(c.port)}
          busy={opening === c.port}
          onOpen={() => void open(c.port)}
          onClose={(id) => void close(id)}
        />
      ))}

      {/* A tunnel whose candidate is no longer in the list — the port stopped
          listening, or the user changed the configured port while it was open.
          It still has to be closable from here; it is a port on this machine. */}
      {tunnels
        .filter((tn) => !candidates.some((c) => c.port === tn.remotePort))
        .map((tn) => (
          <div key={tn.id} className="kuma-row">
            <span className="mono">:{tn.remotePort}</span>
            <a className="mono ellipsis" href={tn.url} target="_blank" rel="noreferrer">
              {tn.url}
            </a>
            <span className="spacer" />
            <button className="ghost small-btn" onClick={() => void close(tn.id)}>
              {t('터널 닫기')}
            </button>
          </div>
        ))}

      {exposed.map((c) => (
        <div key={`exposed-${c.port}`} className="kuma-row" data-exposed>
          <span className="mono">:{c.port}</span>
          <span className="badge warn">{t('외부 노출')}</span>
          {/* No tunnel offered: a browser reaches this already, and forwarding
              to it would be theatre. Naming it is the useful part — "I thought
              that was internal" is what this tab exists to catch. */}
          <span className="muted small">
            {t('이 Kuma 는 외부에서도 닿습니다 — 터널이 필요 없습니다.')}
          </span>
        </div>
      ))}
    </section>
  )
}

/** One detected instance, and the button that reaches it. */
function KumaRow({
  candidate,
  tunnel,
  busy,
  onOpen,
  onClose,
}: {
  candidate: KumaCandidate
  tunnel?: TunnelView
  busy: boolean
  onOpen: () => void
  onClose: (id: string) => void
}) {
  return (
    <div className="kuma-row">
      <span className="mono">
        {candidate.address}:{candidate.port}
      </span>
      {/* Confirmed means the port answered with Kuma's own page. An unconfirmed
          one is still offered — it is the port the user named — but calling it
          Kuma when it might be somebody's router would be the app inventing a
          fact it does not have. */}
      {candidate.confirmed ? (
        <span className="badge">{t('확인됨')}</span>
      ) : (
        <span className="badge warn" title={candidate.note}>
          {t('미확인')}
        </span>
      )}
      {candidate.process && <span className="muted small mono">{candidate.process}</span>}
      <span className="spacer" />
      {tunnel ? (
        <>
          <a className="mono small ellipsis" href={tunnel.url} target="_blank" rel="noreferrer">
            {tunnel.url}
          </a>
          <button className="ghost small-btn" onClick={() => onClose(tunnel.id)}>
            {t('터널 닫기')}
          </button>
        </>
      ) : (
        <button className="small-btn" disabled={busy} onClick={onOpen}>
          {busy ? t('여는 중…') : t('Kuma 열기 (SSH 터널)')}
        </button>
      )}
    </div>
  )
}
