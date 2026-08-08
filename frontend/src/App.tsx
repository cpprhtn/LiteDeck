import { Suspense, lazy, useCallback, useEffect, useState } from 'react'
import Bench from './Bench'
import { CommandLogPanel } from './CommandLogPanel'
import { ErrorBoundary } from './ErrorBoundary'
import { HostKeyDialog, SecretDialog } from './Dialogs'
import { ContainerView } from './ContainerView'
import { FileExplorer } from './FileExplorer'
import { HostEditor, emptyHost } from './HostEditor'
import { HostSidebar } from './HostSidebar'
import { MetricsBar } from './MetricsBar'
import { NetworkView } from './NetworkView'
import { ProcessView } from './ProcessView'
import { ServiceView } from './ServiceView'

// xterm.js is ~340KB — more than the rest of the app combined. The terminal is
// the secondary surface (§1.6) and most sessions never open it, so it loads on
// demand rather than being paid for at every cold start (a first-class metric).
const TerminalView = lazy(() =>
  import('./TerminalView').then((m) => ({ default: m.TerminalView })),
)
import {
  BenchMode,
  Bootstrap,
  ConnectHost,
  DetectHost,
  DisconnectHost,
  ImportSSHConfig,
  ListHosts,
  on,
  type BootstrapData,
  type ConnectionState,
  type Host,
  type HostKeyPrompt,
  type HostView,
  type SecretPrompt,
  type ServerInfo,
} from './ipc'
import { initPlatform } from './platform'

// The application shell (§8): host sidebar on the left, the selected host's
// tabs in the middle, the Command Log along the bottom.
//
// Scoped to what §1.6 fixed — a handful of servers, opened when something needs
// doing, GUI first. No tray, no alerting, no fleet view.

type Tab = 'services' | 'files' | 'processes' | 'containers' | 'network' | 'terminal'

const TABS: { id: Tab; label: string; capability?: string }[] = [
  { id: 'files', label: '파일' },
  { id: 'services', label: '서비스', capability: 'services' },
  { id: 'processes', label: '프로세스', capability: 'processes' },
  { id: 'containers', label: '컨테이너', capability: 'containers' },
  { id: 'network', label: '네트워크' },
  { id: 'terminal', label: '터미널' },
]

export default function App() {
  const [benchMode, setBenchMode] = useState<boolean | null>(null)
  const [boot, setBoot] = useState<BootstrapData | null>(null)
  const [hosts, setHosts] = useState<HostView[]>([])
  const [activeID, setActiveID] = useState<string | null>(null)
  const [tab, setTab] = useState<Tab>('services')
  const [info, setInfo] = useState<Record<string, ServerInfo>>({})
  const [hostKeyPrompt, setHostKeyPrompt] = useState<HostKeyPrompt | null>(null)
  const [secretPrompt, setSecretPrompt] = useState<SecretPrompt | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [logOpen, setLogOpen] = useState(true)
  const [editing, setEditing] = useState<Host | null>(null)

  const reloadHosts = useCallback(async () => {
    try {
      setHosts(await ListHosts())
    } catch (e) {
      setError(String(e))
    }
  }, [])

  useEffect(() => {
    ;(async () => {
      try {
        if (await BenchMode()) {
          setBenchMode(true)
          return
        }
        setBenchMode(false)
        await initPlatform()
        const b = await Bootstrap()
        setBoot(b)
        setHosts(b.hosts)
        if (b.startupError) setError(b.startupError)
        if (b.hosts.length > 0) setActiveID(b.hosts[0].id)
      } catch (e) {
        setBenchMode(false)
        setError(String(e))
      }
    })()
  }, [])

  // Prompts arrive mid-handshake: sshcore is parked on a channel waiting for
  // the answer these dialogs send back.
  useEffect(() => {
    const offKey = on<HostKeyPrompt>('prompt:hostkey', setHostKeyPrompt)
    const offSecret = on<SecretPrompt>('prompt:secret', setSecretPrompt)
    const offState = on<ConnectionState>('conn:state', (s) => {
      setHosts((prev) =>
        prev.map((h) => (h.id === s.hostId ? { ...h, state: s.state } : h)),
      )
      if (s.error) setError(s.error)
    })
    return () => {
      offKey()
      offSecret()
      offState()
    }
  }, [])

  const connect = async (id: string) => {
    setBusy(true)
    setError(null)
    try {
      await ConnectHost(id)
      setActiveID(id)
      const detected = await DetectHost(id)
      setInfo((prev) => ({ ...prev, [id]: detected }))
      // Land on a tab the server can actually serve.
      setTab(detected.capabilities?.services ? 'services' : 'processes')
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
      void reloadHosts()
    }
  }

  const disconnect = async (id: string) => {
    setBusy(true)
    try {
      await DisconnectHost(id)
      setInfo((prev) => {
        const next = { ...prev }
        delete next[id]
        return next
      })
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
      void reloadHosts()
    }
  }

  const doImport = async () => {
    setBusy(true)
    try {
      const res = await ImportSSHConfig()
      await reloadHosts()
      setError(
        res.imported === 0 && res.skipped === 0
          ? `${res.path} 에서 가져올 호스트를 찾지 못했습니다.`
          : null,
      )
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  if (benchMode === null) return <div className="boot">시작 중…</div>
  if (benchMode) return <Bench />

  const active = hosts.find((h) => h.id === activeID) ?? null
  const activeInfo = activeID ? info[activeID] : undefined
  const connected = active?.state === 'connected'

  return (
    <div className="app">
      <HostSidebar
        hosts={hosts}
        activeID={activeID}
        onSelect={setActiveID}
        onConnect={(id) => void connect(id)}
        onDisconnect={(id) => void disconnect(id)}
        onImport={() => void doImport()}
        onAdd={() => setEditing(emptyHost())}
        onEdit={(h) => setEditing(h)}
        busy={busy}
        version={boot?.version}
      />

      <main className="main">
        <header className="main-head">
          {active ? (
            <>
              <div>
                <div className="host-title">{active.name || active.hostname}</div>
                <div className="muted small">
                  {active.user}@{active.hostname}
                  {activeInfo && ` · ${activeInfo.prettyName}`}
                  {activeInfo?.hasSystemd && ` · systemd ${activeInfo.systemdVersion}`}
                  {activeInfo && !activeInfo.systemdJson && activeInfo.hasSystemd && (
                    <span title="systemd 246 미만 — 표 파싱으로 폴백"> (표 폴백)</span>
                  )}
                </div>
              </div>
              {boot && !boot.keychainOk && (
                <span className="badge warn" title="OS 키체인을 사용할 수 없어 비밀번호를 저장하지 않습니다">
                  키체인 없음
                </span>
              )}
            </>
          ) : (
            <div className="muted">좌측에서 호스트를 선택하세요.</div>
          )}
        </header>

        {error && (
          <div className="error">
            <span>{error}</span>
            <button className="ghost small-btn" onClick={() => setError(null)}>
              닫기
            </button>
          </div>
        )}

        {active && connected && (
          <>
            {/* Above the tabs, so "is this box healthy" is answered wherever
                the user happens to be (§4.7). */}
            <ErrorBoundary key={`metrics:${active.id}`} label="상태 표시줄">
              <MetricsBar hostID={active.id} />
            </ErrorBoundary>

            {/* Tabs stay clickable even when the server cannot serve them.
                Greying one out tells the user nothing about why, and a tooltip
                is not an answer — the view explains itself instead. */}
            <nav className="tabs">
              {TABS.map((t) => {
                const unsupported =
                  t.capability && activeInfo?.capabilities?.[t.capability] === false
                return (
                  <button
                    key={t.id}
                    data-on={tab === t.id || undefined}
                    data-unsupported={unsupported || undefined}
                    onClick={() => setTab(t.id)}
                  >
                    {t.label}
                  </button>
                )
              })}
            </nav>

            {/* Scoped per tab and keyed by host: a crash in one view leaves the
                others usable, and switching hosts starts the view clean. */}
            <ErrorBoundary key={`${active.id}:${tab}`} label={TABS.find((t) => t.id === tab)?.label ?? tab}>
              {renderTab(tab, active.id, activeInfo, setError)}
            </ErrorBoundary>
          </>
        )}

        {active && !connected && (
          <div className="placeholder">
            {active.state === 'connecting'
              ? '연결하는 중…'
              : '연결되어 있지 않습니다. 좌측에서 접속을 누르세요.'}
          </div>
        )}
      </main>

      <CommandLogPanel open={logOpen} onToggle={() => setLogOpen((v) => !v)} />

      {editing && (
        <HostEditor
          host={editing}
          onClose={() => setEditing(null)}
          onSaved={() => void reloadHosts()}
          onError={setError}
        />
      )}

      {hostKeyPrompt && (
        <HostKeyDialog prompt={hostKeyPrompt} onDone={() => setHostKeyPrompt(null)} />
      )}
      {secretPrompt && (
        <SecretDialog prompt={secretPrompt} onDone={() => setSecretPrompt(null)} />
      )}
    </div>
  )
}

/**
 * Explains why a tab has nothing to show.
 *
 * Every path through renderTab returns *something*. A branch that renders
 * nothing produces a blank panel with no explanation, which is indistinguishable
 * from a crash — the user cannot tell "your server has no Docker" from "the app
 * is broken", and will reasonably assume the latter.
 */
function Unavailable({
  title,
  detail,
  hint,
}: {
  title: string
  detail: string
  hint?: string
}) {
  return (
    <div className="unavailable">
      <h3>{title}</h3>
      <p className="muted">{detail}</p>
      {hint && <p className="muted small">{hint}</p>}
    </div>
  )
}

function renderTab(
  tab: Tab,
  hostID: string,
  info: ServerInfo | undefined,
  onError: (msg: string) => void,
) {
  if (!info) {
    return <div className="placeholder">서버를 확인하는 중…</div>
  }

  switch (tab) {
    case 'files':
      return <FileExplorer hostID={hostID} onError={onError} />

    case 'processes':
      return <ProcessView hostID={hostID} visible onError={onError} />

    case 'services':
      if (!info.hasSystemd) {
        return (
          <Unavailable
            title="이 서버에는 systemd가 없습니다"
            detail={`${info.prettyName || '알 수 없는 배포판'} — systemctl을 찾지 못했습니다.`}
            hint="OpenRC(Alpine)·SysVinit 어댑터는 아직 없습니다. 프로세스 탭은 그대로 쓸 수 있습니다."
          />
        )
      }
      return <ServiceView hostID={hostID} visible onError={onError} />

    case 'containers':
      if (!info.hasDocker && !info.hasPodman) {
        return (
          <Unavailable
            title="이 서버에는 컨테이너 런타임이 없습니다"
            detail="docker와 podman 둘 다 PATH에서 찾지 못했습니다."
            hint="설치되어 있는데도 이렇게 나온다면, 로그인 셸의 PATH에 없을 수 있습니다 — 터미널 탭에서 `command -v docker` 로 확인해보세요."
          />
        )
      }
      return <ContainerView hostID={hostID} visible onError={onError} />

    case 'network':
      return <NetworkView hostID={hostID} visible onError={onError} />

    case 'terminal':
      return (
        <Suspense fallback={<div className="placeholder">터미널을 불러오는 중…</div>}>
          <TerminalView hostID={hostID} visible onError={onError} />
        </Suspense>
      )
  }
}
