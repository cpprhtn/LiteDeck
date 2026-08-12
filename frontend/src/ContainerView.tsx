import { useCallback, useEffect, useRef, useState } from 'react'
import { usePoll } from './usePoll'
import { ImagesVolumes } from './ImagesVolumes'
import { LogPanel } from './LogPanel'
import { PopMenu } from './PopMenu'
import {
  ComposeAction,
  ContainerAction,
  FollowContainerLog,
  ListContainers,
  RemoveContainer,
  StopLogStream,
  type Container,
  type LogStream,
} from './ipc'
import { t } from './i18n'

// The container view (§4.5). Cards rather than a table: a host runs a handful of
// containers, not thousands, and the things people want at a glance — image,
// ports, why it stopped — do not fit a row comfortably.

const POLL_MS = 5000

type Filter = 'all' | 'running' | 'stopped'

/** Containers Compose would restart along with this one. */
function projectPeers(all: Container[], project: string): Container[] {
  // One-offs from `compose run` carry the project label but are not part of the
  // declared set, so Compose leaves them alone — listing them would overstate
  // what the button does.
  return all.filter((c) => c.compose?.project === project && !c.compose.oneOff)
}

/**
 * Restart, with a scope to choose when the container came from Compose.
 *
 * Plain containers keep the plain button: a menu with one entry is worse than
 * no menu. The service entry appears only when the service has more than one
 * container, because otherwise it and "this container" do the same thing and
 * the user has to work out that they are the same.
 */
function RestartButton({
  container,
  peers,
  busy,
  onContainer,
  onService,
  onProject,
}: {
  container: Container
  peers: Container[]
  busy: boolean
  onContainer: () => void
  onService: (service: string) => void
  onProject: (project: string) => void
}) {
  const [open, setOpen] = useState(false)
  const caret = useRef<HTMLButtonElement>(null)
  const compose = container.compose

  if (!compose || compose.oneOff) {
    return (
      <button disabled={busy} onClick={onContainer}>
        {t('재시작')}
      </button>
    )
  }

  const inProject = projectPeers(peers, compose.project)
  const inService = inProject.filter((c) => c.compose?.service === compose.service)

  return (
    <span className="pop-wrap">
      <button disabled={busy} onClick={onContainer}>
        {t('재시작')}
      </button>
      <button
        ref={caret}
        className="pop-caret"
        disabled={busy}
        aria-label={t('재시작 범위')}
        aria-expanded={open}
        title={t('재시작 범위')}
        onClick={() => setOpen((v) => !v)}
      >
        ▾
      </button>
      {open && (
        <PopMenu anchor={caret.current} onClose={() => setOpen(false)}>
          <button
            onClick={() => {
              setOpen(false)
              onContainer()
            }}
          >
            {t('이 컨테이너만')}
            <span className="muted small ellipsis">{container.name}</span>
          </button>
          {inService.length > 1 && (
            <button
              onClick={() => {
                setOpen(false)
                onService(compose.service)
              }}
            >
              {t('서비스 전체')}
              <span className="muted small ellipsis">
                {t('{service} · 컨테이너 {n}개', {
                  service: compose.service,
                  n: inService.length,
                })}
              </span>
            </button>
          )}
          <button
            onClick={() => {
              setOpen(false)
              onProject(compose.project)
            }}
          >
            {t('프로젝트 전체')}
            <span className="muted small ellipsis">
              {t('{project} · 컨테이너 {n}개', {
                project: compose.project,
                n: inProject.length,
              })}
            </span>
          </button>
        </PopMenu>
      )}
    </span>
  )
}

export function ContainerView({
  hostID,
  visible,
  onError,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [containers, setContainers] = useState<Container[]>([])
  const [filter, setFilter] = useState<Filter>('all')
  const [pane, setPane] = useState<'containers' | 'storage'>('containers')
  const [pending, setPending] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [log, setLog] = useState<LogStream | null>(null)
  const [confirmRemove, setConfirmRemove] = useState<Container | null>(null)
  const [confirmProject, setConfirmProject] = useState<{
    container: Container
    project: string
  } | null>(null)
  const [needsRoot, setNeedsRoot] = useState<{ retry: () => void; message: string } | null>(
    null,
  )
  const inFlight = useRef(false)

  const refresh = useCallback(async () => {
    if (inFlight.current) return
    inFlight.current = true
    try {
      // `?? []` is belt and braces: the Go side now guarantees a non-nil
      // slice, but a future binding that forgets would blank the window again.
      setContainers((await ListContainers(hostID)) ?? [])
    } catch (e) {
      onError(String(e))
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [hostID, onError])

  usePoll(refresh, POLL_MS, visible)

  const run = async (
    id: string,
    fn: (elevate: boolean) => Promise<{ ok: boolean; needsElevation: boolean; error?: string }>,
  ) => {
    setPending(id)
    setNeedsRoot(null)
    try {
      const res = await fn(false)
      if (!res.ok) {
        if (res.needsElevation) {
          setNeedsRoot({
            message: res.error ?? t('권한이 필요합니다'),
            retry: () => void fn(true).then(() => refresh()),
          })
        } else {
          onError(res.error ?? t('실패했습니다'))
        }
        return
      }
      await refresh()
    } catch (e) {
      onError(String(e))
    } finally {
      setPending(null)
    }
  }

  // A follow rather than a one-shot tail: container logs are usually opened
  // because something is happening right now.
  const showLogs = async (c: Container) => {
    try {
      if (log) await StopLogStream(log.id)
      const stream = await FollowContainerLog(hostID, c.id, 200)
      setLog({ ...stream, title: c.name })
    } catch (e) {
      onError(String(e))
    }
  }

  const rows = containers.filter((c) => {
    if (filter === 'running') return c.state === 'running'
    if (filter === 'stopped') return c.state !== 'running'
    return true
  })

  const runningCount = containers.filter((c) => c.state === 'running').length
  const crashed = containers.filter((c) => c.exitCode > 0).length

  if (pane === 'storage') {
    return (
      <div className="view">
        <div className="view-toolbar">
          <div className="segmented">
            <button onClick={() => setPane('containers')}>{t('컨테이너')}</button>
            <button data-on>{t('이미지 · 볼륨')}</button>
          </div>
        </div>
        <ImagesVolumes hostID={hostID} visible onError={onError} />
      </div>
    )
  }

  return (
    <div className="view">
      <div className="view-toolbar">
        <div className="segmented">
          <button data-on>{t('컨테이너')}</button>
          <button onClick={() => setPane('storage')}>{t('이미지 · 볼륨')}</button>
        </div>
        <div className="segmented">
          <button data-on={filter === 'all' || undefined} onClick={() => setFilter('all')}>
            {t('전체 {n}', { n: containers.length })}
          </button>
          <button
            data-on={filter === 'running' || undefined}
            onClick={() => setFilter('running')}
          >
            {t('실행 중 {n}', { n: runningCount })}
          </button>
          <button
            data-on={filter === 'stopped' || undefined}
            data-danger={crashed > 0 || undefined}
            onClick={() => setFilter('stopped')}
          >
            {t('중지됨 {n}', { n: containers.length - runningCount })}
          </button>
        </div>
        <span className="spacer" />
        {crashed > 0 && <span className="badge danger">{t('비정상 종료 {n}', { n: crashed })}</span>}
        <button className="ghost" onClick={() => void refresh()}>
          {t('새로고침')}
        </button>
      </div>

      <div className="cards">
        {loading && <div className="placeholder">{t('컨테이너를 읽는 중…')}</div>}
        {!loading && rows.length === 0 && (
          <div className="placeholder">{t('조건에 맞는 컨테이너가 없습니다.')}</div>
        )}

        {rows.map((c) => {
          const running = c.state === 'running'
          const busy = pending === c.id
          return (
            <div key={c.id} className="card" data-running={running || undefined} data-busy={busy || undefined}>
              <div className="card-head">
                <span className="dot" data-status={running ? 'active' : c.exitCode > 0 ? 'failed' : undefined} />
                <strong className="ellipsis">{c.name}</strong>
                <span className="muted small ellipsis">{c.image}</span>
                {/* Why this card has a restart menu and the one below it does
                    not. The project is the part worth naming — the service is
                    usually already legible in the container name. */}
                {c.compose && (
                  <span
                    className="badge"
                    title={t('Compose 프로젝트 {project} · 서비스 {service}', {
                      project: c.compose.project,
                      service: c.compose.service,
                    })}
                  >
                    {c.compose.oneOff
                      ? t('{project} (일회성)', { project: c.compose.project })
                      : c.compose.project}
                  </span>
                )}
              </div>

              <div className="card-meta">
                <span className={c.exitCode > 0 ? 'danger-text' : 'muted'}>{c.status}</span>
                {(c.ports?.length ?? 0) > 0 && (
                  <span className="mono small">
                    {c.ports
                      .map((p) =>
                        p.hostPort
                          ? `${p.hostPort}→${p.container}/${p.protocol}`
                          : `${p.container}/${p.protocol}`,
                      )
                      .join(' · ')}
                  </span>
                )}
              </div>

              <div className="card-cmd mono small ellipsis muted" title={c.command}>
                {c.command}
              </div>

              <div className="card-actions">
                {running ? (
                  <>
                    <button
                      disabled={busy}
                      onClick={() =>
                        void run(c.id, (e) => ContainerAction(hostID, c.id, 'stop', e))
                      }
                    >
                      {t('중지')}
                    </button>
                    <RestartButton
                      container={c}
                      peers={containers}
                      busy={busy}
                      onContainer={() =>
                        void run(c.id, (e) => ContainerAction(hostID, c.id, 'restart', e))
                      }
                      onService={(service) =>
                        void run(c.id, (e) =>
                          ComposeAction(hostID, c.compose!.project, service, 'restart', e),
                        )
                      }
                      onProject={(project) => setConfirmProject({ container: c, project })}
                    />
                  </>
                ) : (
                  <button
                    disabled={busy}
                    onClick={() =>
                      void run(c.id, (e) => ContainerAction(hostID, c.id, 'start', e))
                    }
                  >
                    {t('시작')}
                  </button>
                )}
                <button disabled={busy} onClick={() => void showLogs(c)}>
                  {t('로그')}
                </button>
                <span className="spacer" />
                <button className="danger" disabled={busy} onClick={() => setConfirmRemove(c)}>
                  {t('삭제')}
                </button>
              </div>
            </div>
          )
        })}
      </div>

      {needsRoot && (
        <div className="elevate">
          <span>{needsRoot.message}</span>
          <button
            className="primary small-btn"
            onClick={() => {
              needsRoot.retry()
              setNeedsRoot(null)
            }}
          >
            {t('관리자 권한으로 재시도')}
          </button>
          <button className="ghost small-btn" onClick={() => setNeedsRoot(null)}>
            {t('취소')}
          </button>
        </div>
      )}

      {log && <LogPanel stream={log} onClose={() => setLog(null)} />}

      {/* Restarting one container is the user's own container coming back.
          Restarting a project takes other services down with it, and the list
          of which is already on screen — so it is shown rather than described.
          No extra command: the names come from the poll that drew the cards. */}
      {confirmProject && (
        <div className="scrim">
          <div className="dialog">
            <h2>{t('프로젝트 전체를 재시작하시겠습니까?')}</h2>
            <dl className="keyinfo">
              <dt>{t('프로젝트')}</dt>
              <dd className="mono">{confirmProject.project}</dd>
              <dt>{t('대상')}</dt>
              <dd className="mono">
                {projectPeers(containers, confirmProject.project)
                  .map((c) => c.name)
                  .join(' · ')}
              </dd>
            </dl>
            <div className="dialog-actions">
              <button onClick={() => setConfirmProject(null)}>{t('취소')}</button>
              <button
                className="primary"
                onClick={() => {
                  const { container, project } = confirmProject
                  setConfirmProject(null)
                  void run(container.id, (e) =>
                    ComposeAction(hostID, project, '', 'restart', e),
                  )
                }}
              >
                {t('재시작')}
              </button>
            </div>
          </div>
        </div>
      )}

      {confirmRemove && (
        <div className="scrim">
          <div className="dialog">
            <h2>{t('컨테이너를 삭제하시겠습니까?')}</h2>
            <p className="muted">
              {t('컨테이너와 그 안의 쓰기 계층이 삭제됩니다. 이미지와 명명된 볼륨은 남습니다.')}
            </p>
            <dl className="keyinfo">
              <dt>{t('이름')}</dt>
              <dd className="mono">{confirmRemove.name}</dd>
              <dt>{t('이미지')}</dt>
              <dd className="mono">{confirmRemove.image}</dd>
              <dt>{t('상태')}</dt>
              <dd className="mono">{confirmRemove.status}</dd>
            </dl>
            {confirmRemove.state === 'running' && (
              <p className="warn-text">
                {t('실행 중인 컨테이너입니다. 삭제하면')} <strong>{t('강제 종료')}</strong>{t('됩니다.')}
              </p>
            )}
            <div className="dialog-actions">
              <button onClick={() => setConfirmRemove(null)}>{t('취소')}</button>
              <button
                className="danger"
                onClick={() => {
                  const c = confirmRemove
                  setConfirmRemove(null)
                  void run(c.id, (e) =>
                    RemoveContainer(hostID, c.id, c.state === 'running', e),
                  )
                }}
              >
                {t('삭제')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
