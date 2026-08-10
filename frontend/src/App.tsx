import { Suspense, lazy, useCallback, useEffect, useState, type ReactNode } from 'react'
import Bench from './Bench'
import { CommandLogPanel } from './CommandLogPanel'
import { ErrorBoundary } from './ErrorBoundary'
import { HostKeyDialog, McpWriteDialog, SecretDialog } from './Dialogs'
import { ContainerView } from './ContainerView'
import { FileExplorer } from './FileExplorer'
import { HostEditor, emptyHost } from './HostEditor'
import { HostSidebar } from './HostSidebar'
import { MetricsBar } from './MetricsBar'
import { NetworkView } from './NetworkView'
import { ProcessView } from './ProcessView'
import { SessionView } from './SessionView'
import { ServiceView } from './ServiceView'

// xterm.js is ~340KB — more than the rest of the app combined. The terminal is
// the secondary surface (§1.6) and most sessions never open it, so it loads on
// demand rather than being paid for at every cold start (a first-class metric).
const TerminalView = lazy(() =>
  import('./TerminalView').then((m) => ({ default: m.TerminalView })),
)
import {
  BenchMode,
  ApplyLanguage,
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
  type MCPWritePrompt,
} from './ipc'
import { getLanguage, initLanguage, k, t, useT } from './i18n'
import { McpPanel } from './McpPanel'
import { McpHostBadge } from './McpHostBadge'
import { closeHost } from './openFiles'
import { initPlatform } from './platform'

// The application shell (§8): host sidebar on the left, the selected host's
// tabs in the middle, the Command Log along the bottom.
//
// Scoped to what §1.6 fixed — a handful of servers, opened when something needs
// doing, GUI first. No tray, no alerting, no fleet view.

type Tab = 'services' | 'files' | 'processes' | 'containers' | 'network' | 'sessions' | 'terminal'

// files and terminal have no capability because they need nothing but SSH
// itself — SFTP and a PTY. That is what keeps them working on a host no adapter
// can drive, such as a Windows box running OpenSSH.
const TABS: { id: Tab; label: string; capability?: string }[] = [
  { id: 'files', label: k('파일') },
  { id: 'services', label: k('서비스'), capability: 'services' },
  { id: 'processes', label: k('프로세스'), capability: 'processes' },
  { id: 'containers', label: k('컨테이너'), capability: 'containers' },
  { id: 'network', label: k('네트워크'), capability: 'network' },
  { id: 'sessions', label: k('세션'), capability: 'sessions' },
  { id: 'terminal', label: k('터미널') },
]

const PLATFORM_LABEL: Record<string, string> = {
  windows: 'Windows',
  darwin: 'macOS',
  bsd: 'BSD',
  unknown: k('알 수 없는 OS'),
}

export default function App() {
  // Subscribing here re-renders every view on a language change; nothing else
  // needs to know about it.
  const t = useT() // shadows the module import; subscribing is the point
  const [benchMode, setBenchMode] = useState<boolean | null>(null)
  const [mcpOpen, setMcpOpen] = useState(false)
  const [mcpWrite, setMcpWrite] = useState<MCPWritePrompt | null>(null)
  const [boot, setBoot] = useState<BootstrapData | null>(null)
  const [hosts, setHosts] = useState<HostView[]>([])
  const [activeID, setActiveID] = useState<string | null>(null)
  const [tab, setTab] = useState<Tab>('services')
  const [info, setInfo] = useState<Record<string, ServerInfo>>({})
  const [hostKeyPrompt, setHostKeyPrompt] = useState<HostKeyPrompt | null>(null)
  const [secretPrompt, setSecretPrompt] = useState<SecretPrompt | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // Collapsed by default: the work happens in the view above it, and the log is
  // there to be consulted rather than watched. The collapsed bar still carries
  // the entry count and the failure badge, so nothing goes unnoticed for being
  // shut — which is the only reason this is a safe default.
  const [logOpen, setLogOpen] = useState(false)
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
        // An explicit choice wins; otherwise follow the OS. Go is told the
        // resolved answer either way, so an English UI does not raise Korean
        // error banners.
        initLanguage(b.language, b.systemLanguage)
        void ApplyLanguage(getLanguage()).catch(() => {})
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
    const offWrite = on<MCPWritePrompt>('prompt:mcpwrite', setMcpWrite)
    const offState = on<ConnectionState>('conn:state', (s) => {
      setHosts((prev) =>
        prev.map((h) => (h.id === s.hostId ? { ...h, state: s.state } : h)),
      )
      if (s.error) setError(s.error)
    })
    return () => {
      offKey()
      offSecret()
      offWrite()
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
      // Always the file tab. Landing on services was a guess at what somebody
      // wants first; files is what they actually reach for, it works on every
      // host including one no adapter can drive, and it is the one tab that
      // never lands dead because SFTP comes from SSH itself.
      setTab('files')
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
      // Editor tabs go with the connection: there is no longer anywhere to save
      // them, and leaving them open invites edits that cannot land.
      closeHost(id)
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
          ? t('{path} 에서 가져올 호스트를 찾지 못했습니다.', { path: res.path })
          : null,
      )
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  if (benchMode === null) return <div className="boot">{t('시작 중…')}</div>
  if (benchMode) return <Bench />

  const active = hosts.find((h) => h.id === activeID) ?? null
  const activeInfo = activeID ? info[activeID] : undefined
  const connected = active?.state === 'connected'
  // A reachable host with no adapter. Not an error — the connection is fine and
  // files and terminals work — but none of the polling views can run.
  //
  // The answer comes from Go. Deriving it here as `platform !== 'linux'` is what
  // made the Windows adapter invisible: the backend supported the host and this
  // side went on rendering "not supported" on top of it.
  const unsupported = !!activeInfo && !activeInfo.supported

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
        onOpenMCP={() => setMcpOpen(true)}
      />

      {mcpOpen && (
        <McpPanel hosts={hosts} onClose={() => setMcpOpen(false)} onError={setError} />
      )}

      <main className="main">
        <header className="main-head">
          {active ? (
            <>
              <div>
                <div className="host-title">{active.name || active.hostname}</div>
                <div className="muted small">
                  {active.user}@{active.hostname}
                  {activeInfo && ` · ${activeInfo.prettyName}`}
                  {/* Only when the server named itself and WMI did not give a
                      friendlier name. Printing both put "Windows 10 Pro ·
                      Windows (stderr가 UTF-8이 아님)" in the subtitle — the same
                      fact twice, the second time as an implementation note. */}
                  {unsupported &&
                    !activeInfo?.prettyName &&
                    ` · ${activeInfo?.kernel || PLATFORM_LABEL[activeInfo!.platform]}`}
                  {unsupported && activeInfo?.prettyName && activeInfo.kernel && (
                    <span title={activeInfo.kernel}> · {activeInfo.kernel}</span>
                  )}
                  {activeInfo?.hasSystemd && ` · systemd ${activeInfo.systemdVersion}`}
                  {activeInfo && !activeInfo.systemdJson && activeInfo.hasSystemd && (
                    <span title={t('systemd 246 미만 — 표 파싱으로 폴백')}> {t('(표 폴백)')}</span>
                  )}
                </div>
              </div>
              <McpHostBadge hostID={active.id} />
              {unsupported && (
                <span
                  className="badge warn"
                  title={t('LiteDeck은 systemd 기반 Linux만 다룹니다. 파일과 터미널은 그대로 쓸 수 있습니다')}
                >
                  {t(PLATFORM_LABEL[activeInfo!.platform] ?? k('미지원 OS'))} — {t('일부 기능만')}
                </span>
              )}
              {boot && !boot.keychainOk && (
                <span className="badge warn" title={t('OS 키체인을 사용할 수 없어 비밀번호를 저장하지 않습니다')}>
                  {t('키체인 없음')}
                </span>
              )}
            </>
          ) : (
            <div className="muted">{t('좌측에서 호스트를 선택하세요.')}</div>
          )}
        </header>

        {error && (
          <div className="error">
            <span>{error}</span>
            <button className="ghost small-btn" onClick={() => setError(null)}>
              {t('닫기')}
            </button>
          </div>
        )}

        {active && connected && (
          <>
            {/* Above the tabs, so "is this box healthy" is answered wherever
                the user happens to be (§4.7). Omitted entirely on a host with no
                adapter: it reads /proc, so leaving it mounted meant a failing
                command every two seconds for as long as the app was open. */}
            {!unsupported && (
              <ErrorBoundary key={`metrics:${active.id}`} label={t('상태 표시줄')}>
                <MetricsBar hostID={active.id} />
              </ErrorBoundary>
            )}

            {/* Tabs stay clickable even when the server cannot serve them.
                Greying one out tells the user nothing about why, and a tooltip
                is not an answer — the view explains itself instead. */}
            <nav className="tabs">
              {TABS.map((entry) => {
                const unsupported =
                  entry.capability && activeInfo?.capabilities?.[entry.capability] === false
                return (
                  <button
                    key={entry.id}
                    data-on={tab === entry.id || undefined}
                    data-unsupported={unsupported || undefined}
                    onClick={() => setTab(entry.id)}
                  >
                    {t(entry.label)}
                  </button>
                )
              })}
            </nav>

            {/* Scoped per tab and keyed by host: a crash in one view leaves the
                others usable, and switching hosts starts the view clean. */}
            <ErrorBoundary key={`${active.id}:${tab}`} label={TABS.find((x) => x.id === tab)?.label ?? tab}>
              {renderTab(tab, active.id, activeInfo, setError, () => setTab('files'))}
            </ErrorBoundary>
          </>
        )}

        {active && !connected && (
          <div className="placeholder">
            {active.state === 'connecting'
              ? t('연결하는 중…')
              : t('연결되어 있지 않습니다. 좌측에서 접속을 누르세요.')}
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
      <McpWriteDialog prompt={mcpWrite} onDone={() => setMcpWrite(null)} />
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
  children,
}: {
  title: string
  detail: string
  hint?: string
  children?: ReactNode
}) {
  return (
    <div className="unavailable">
      <h3>{title}</h3>
      <p className="muted">{detail}</p>
      {hint && <p className="muted small">{hint}</p>}
      {children}
    </div>
  )
}

function renderTab(
  tab: Tab,
  hostID: string,
  info: ServerInfo | undefined,
  onError: (msg: string) => void,
  /** A `code`/`vi` caught in the terminal wants the file tab (§4.6a). */
  onReveal: () => void,
) {
  if (!info) {
    return <div className="placeholder">{t('서버를 확인하는 중…')}</div>
  }

  // No adapter for this platform. Checked before the per-tab logic because the
  // reason is the same for all four views, and because the alternative — letting
  // them mount and fail — is what produced a wall of console-codepage errors on
  // a Windows host while never saying the server was Windows.
  //
  // files and terminal fall through deliberately: SFTP and a PTY are provided by
  // SSH itself, so they work here.
  if (!info.supported && tab !== 'files' && tab !== 'terminal') {
    const name = info.prettyName || t(PLATFORM_LABEL[info.platform] ?? '') || t('이 OS')
    // prettyName first: once detection has an answer, saying "the server did not
    // respond" over the top of "Windows 10 Pro" in the header is a contradiction
    // the user has to resolve, and they cannot.
    const detail = info.prettyName
      ? t('서버가 {os} 로 확인됐습니다. 이 OS를 다루는 어댑터가 아직 없습니다.', { os: info.prettyName })
      : info.kernel
        ? t('서버가 {kernel} 로 응답했습니다.', { kernel: info.kernel })
        : t('서버가 어떤 OS인지 확인할 수 없었습니다 — 아래 감지 과정을 참고하세요.')
    return (
      <Unavailable
        title={t('{os} 서버는 아직 지원하지 않습니다', { os: name })}
        detail={detail}
        hint={t('파일 탭과 터미널 탭은 그대로 쓸 수 있습니다 — SFTP와 PTY는 SSH가 직접 제공하기 때문입니다. 어댑터 기여는 환영합니다 (CONTRIBUTING.md).')}
      >
        {/* The probe transcript, shown only when detection gave up. Without it
            "알 수 없는 OS" is a dead end: nobody can tell which probe misbehaved
            without rebuilding the app with print statements in it. */}
        {!!info.warnings?.length && (
          <details className="diag">
            <summary className="muted small">{t('감지 과정 보기')}</summary>
            <pre className="mono small">{info.warnings.join('\n')}</pre>
          </details>
        )}
      </Unavailable>
    )
  }

  switch (tab) {
    case 'files':
      return <FileExplorer hostID={hostID} onError={onError} />

    case 'processes':
      return <ProcessView hostID={hostID} visible onError={onError} />

    case 'services':
      // Keyed on the capability, not on hasSystemd. Asking about systemd directly
      // meant a Windows host — which has a service adapter and no systemd — was
      // told it had no init system, over the top of a working service list.
      // The capability map is the one place that knows what each platform can do.
      if (!info.capabilities?.services) {
        return (
          <Unavailable
            title={t('이 서버의 서비스 목록을 읽을 수 없습니다')}
            detail={t('{os} — systemctl을 찾지 못했습니다.', { os: info.prettyName || t('알 수 없는 배포판') })}
            hint={t('OpenRC(Alpine)·SysVinit 어댑터는 아직 없습니다. 프로세스 탭은 그대로 쓸 수 있습니다.')}
          />
        )
      }
      return <ServiceView hostID={hostID} visible onError={onError} />

    case 'containers':
      if (!info.hasDocker && !info.hasPodman) {
        return (
          <Unavailable
            title={t('이 서버에는 컨테이너 런타임이 없습니다')}
            detail={t('docker와 podman 둘 다 PATH에서 찾지 못했습니다.')}
            hint={t('설치되어 있는데도 이렇게 나온다면, 로그인 셸의 PATH에 없을 수 있습니다 — 터미널 탭에서 `command -v docker` 로 확인해보세요.')}
          />
        )
      }
      return <ContainerView hostID={hostID} visible onError={onError} />

    case 'network':
      // The one view that had no check at all. Tabs stay clickable by design, so
      // every branch has to answer for itself — without this, opening the tab on
      // Windows ran `ip -j addr` and `ss -tulnp` against cmd.exe every few
      // seconds and filled the Command Log with console-codepage errors.
      if (!info.capabilities?.network) {
        return (
          <Unavailable
            title={t('이 서버의 네트워크 정보를 읽을 수 없습니다')}
            detail={
              info.platform === 'windows'
                ? t('Windows 어댑터에 네트워크 뷰가 아직 없습니다.')
                : t('{os} — iproute2(ip, ss)를 찾지 못했습니다.', { os: info.prettyName || t('이 서버') })
            }
            hint={
              info.platform === 'windows'
                ? t('Get-NetIPAddress와 Get-NetTCPConnection으로 만들 수 있습니다 — 아직 구현하지 않았습니다.')
                : t('배포판에 따라 iproute2 패키지를 설치하면 됩니다. 다른 탭은 그대로 쓸 수 있습니다.')
            }
          />
        )
      }
      return <NetworkView hostID={hostID} visible onError={onError} />

    case 'sessions':
      if (!info.capabilities?.sessions) {
        return (
          <Unavailable
            title={t('이 서버의 SSH 접속 목록을 읽을 수 없습니다')}
            detail={
              info.platform === 'windows'
                ? t('Windows sshd 는 세션을 "sshd: user@pts" 프로세스로 만들지 않아, 지금 파서가 읽을 것이 없습니다.')
                : t('{os} — ps 를 실행하지 못했습니다.', { os: info.prettyName || t('이 서버') })
            }
            hint={t('다른 탭은 그대로 쓸 수 있습니다.')}
          />
        )
      }
      return <SessionView hostID={hostID} visible onError={onError} />

    case 'terminal':
      return (
        <Suspense fallback={<div className="placeholder">{t('터미널을 불러오는 중…')}</div>}>
          <TerminalView
            hostID={hostID}
            visible
            onError={onError}
            onReveal={onReveal}
          />
        </Suspense>
      )
  }
}
