import { useCallback, useEffect, useRef, useState } from 'react'
import { HostNetwork, type NetworkView as NetView } from './ipc'

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

  useEffect(() => {
    if (!visible) return
    void refresh()
    const id = window.setInterval(() => void refresh(), POLL_MS)
    return () => window.clearInterval(id)
  }, [visible, refresh])

  if (loading && !net) {
    return <div className="placeholder">네트워크 상태를 읽는 중…</div>
  }

  const interfaces = net?.interfaces ?? []
  const listeners = (net?.listeners ?? []).filter((l) => !onlyExposed || l.exposed)
  const exposedCount = (net?.listeners ?? []).filter((l) => l.exposed).length

  return (
    <div className="view net-view">
      <div className="view-toolbar">
        <div className="segmented">
          <button data-on={!onlyExposed || undefined} onClick={() => setOnlyExposed(false)}>
            전체 {net?.listeners.length ?? 0}
          </button>
          <button
            data-on={onlyExposed || undefined}
            data-danger={exposedCount > 0 || undefined}
            onClick={() => setOnlyExposed(true)}
          >
            외부 노출 {exposedCount}
          </button>
        </div>
        <span className="spacer" />
        <button className="ghost" onClick={() => void refresh()}>
          새로고침
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
          <h3 className="net-heading">인터페이스</h3>
          {interfaces.length === 0 && (
            <div className="placeholder small">인터페이스를 읽지 못했습니다.</div>
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
                    {i.addresses.length === 0 && <span className="muted">주소 없음</span>}
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
            열린 포트
            <span className="muted small">
              {' '}
              — <strong>외부 노출</strong>은 0.0.0.0/[::]에 바인딩되어 다른 기기에서 닿을 수
              있다는 뜻입니다
            </span>
          </h3>
          {listeners.length === 0 && (
            <div className="placeholder small">
              {onlyExposed ? '외부에 노출된 포트가 없습니다.' : '열린 포트가 없습니다.'}
            </div>
          )}
          {listeners.length > 0 && (
            <div className="table net-table">
              <div className="thead" style={{ gridTemplateColumns: '64px 80px 1fr 1fr 90px' }}>
                <div>PROTO</div>
                <div className="num">PORT</div>
                <div>BIND</div>
                <div>PROCESS</div>
                <div>노출</div>
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
                      <span className="badge warn">외부</span>
                    ) : (
                      <span className="muted small">로컬만</span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
