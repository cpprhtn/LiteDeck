import type { ConnState, HostView } from './ipc'

// The host list (§4.1). At the scale §1.6 fixed — two to five servers — every
// name fits on screen, so this is a plain grouped list: no search box, no tags,
// no fleet view. Those belong to a different product.

const STATE_LABEL: Record<ConnState, string> = {
  disconnected: '연결 안 됨',
  connecting: '연결 중',
  connected: '연결됨',
  reconnecting: '재연결 중',
}

function StateDot({ state }: { state: ConnState }) {
  return <span className="dot" data-state={state} title={STATE_LABEL[state]} />
}

export function HostSidebar({
  hosts,
  activeID,
  onSelect,
  onConnect,
  onDisconnect,
  onImport,
  onAdd,
  onEdit,
  busy,
  version,
}: {
  hosts: HostView[]
  activeID: string | null
  onSelect: (id: string) => void
  onConnect: (id: string) => void
  onDisconnect: (id: string) => void
  onImport: () => void
  onAdd: () => void
  onEdit: (h: HostView) => void
  busy: boolean
  // Undefined until Bootstrap returns. Rendered as a dash rather than hidden —
  // a missing version reads as "the app failed to start properly", which is
  // information too.
  version?: string
}) {
  const groups = new Map<string, HostView[]>()
  for (const h of hosts) {
    const key = h.group || ''
    const list = groups.get(key) ?? []
    list.push(h)
    groups.set(key, list)
  }

  return (
    <aside className="sidebar">
      <div className="sidebar-head">
        <span className="sidebar-title">호스트</span>
        <span className="spacer" />
        <button className="ghost small-btn" onClick={onImport} disabled={busy} title="~/.ssh/config 가져오기">
          가져오기
        </button>
        <button className="ghost small-btn" onClick={onAdd} disabled={busy} title="호스트 추가">
          + 추가
        </button>
      </div>

      {hosts.length === 0 && (
        <div className="empty">
          <p>등록된 호스트가 없습니다.</p>
          <p className="muted small">
            <code>~/.ssh/config</code> 를 가져오거나 직접 추가하세요.
          </p>
        </div>
      )}

      <div className="host-list">
        {[...groups.entries()].map(([group, list]) => (
          <div key={group || '_'}>
            {group && <div className="group-label">{group}</div>}
            {list.map((h) => {
              const connected = h.state === 'connected'
              return (
                <div
                  key={h.id}
                  className="host-row"
                  data-active={h.id === activeID || undefined}
                  onClick={() => onSelect(h.id)}
                >
                  <StateDot state={h.state} />
                  <div className="host-text">
                    <div className="host-name ellipsis">{h.name || h.hostname}</div>
                    <div className="host-addr ellipsis muted">
                      {h.user}@{h.hostname}
                      {h.port && h.port !== 22 ? `:${h.port}` : ''}
                    </div>
                  </div>
                  <div className="host-actions">
                    <button
                      className="ghost small-btn"
                      disabled={busy}
                      title="편집"
                      onClick={(e) => {
                        e.stopPropagation()
                        onEdit(h)
                      }}
                    >
                      ⋯
                    </button>
                    <button
                      className="ghost small-btn"
                      disabled={busy || h.state === 'connecting'}
                      onClick={(e) => {
                        e.stopPropagation()
                        connected ? onDisconnect(h.id) : onConnect(h.id)
                      }}
                    >
                      {connected ? '끊기' : '접속'}
                    </button>
                  </div>
                </div>
              )
            })}
          </div>
        ))}
      </div>

      <div className="sidebar-foot">
        <span className="muted small mono" title="버그 리포트에 이 버전을 함께 적어주세요">
          LiteDeck {version ?? '—'}
        </span>
      </div>
    </aside>
  )
}
