import { useCallback, useEffect, useRef, useState } from 'react'
import { usePoll } from './usePoll'
import { ImagesVolumes } from './ImagesVolumes'
import { LogPanel } from './LogPanel'
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

/** The verbs a project can be told to do. Deliberately not `down` or `up`. */
type ComposeVerb = 'start' | 'stop' | 'restart'

const VERB_LABEL: Record<ComposeVerb, () => string> = {
  start: () => t('시작'),
  stop: () => t('중지'),
  restart: () => t('재시작'),
}

/** Pending key for a project-wide action; container IDs cannot collide with it. */
const PROJECT_PENDING = 'compose:'

/** One compose project, or the containers belonging to none. */
interface Group {
  /** null for containers Compose did not start. */
  project: string | null
  /** Cards to draw — after the running/stopped filter. */
  shown: Container[]
  /** Every declared member of the project, filter or not. The header's buttons
   *  act on these, so the count beside them has to be this and not `shown`. */
  members: Container[]
  running: number
}

/**
 * Splits the list into compose projects, ungrouped last.
 *
 * `shown` and `members` are counted from different lists on purpose. A filter
 * of "running only" hides most cards, but "start the whole project" still
 * starts the whole project — a header claiming 1 container while the button
 * touches 5 would be lying about what it is about to do.
 *
 * One-offs from `compose run` sit with their project because that is where a
 * reader will look for them, but they are not members: Compose leaves them
 * alone, so counting them would overstate the buttons' reach.
 */
function groupByProject(shown: Container[], all: Container[]): Group[] {
  const order: string[] = []
  const byProject = new Map<string, Container[]>()
  const loose: Container[] = []

  for (const c of shown) {
    const project = c.compose?.project
    if (!project) {
      loose.push(c)
      continue
    }
    if (!byProject.has(project)) {
      byProject.set(project, [])
      order.push(project)
    }
    byProject.get(project)!.push(c)
  }

  const groups: Group[] = order.sort().map((project) => {
    const members = all.filter((c) => c.compose?.project === project && !c.compose.oneOff)
    return {
      project,
      shown: byProject.get(project)!,
      members,
      running: members.filter((c) => c.state === 'running').length,
    }
  })
  if (loose.length > 0) {
    groups.push({ project: null, shown: loose, members: loose, running: 0 })
  }
  return groups
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
    project: string
    action: ComposeVerb
    members: Container[]
  } | null>(null)
  /** Projects the user folded away. Keyed by name; '' is the ungrouped block. */
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
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

  const groups = groupByProject(rows, containers)
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

        {groups.map((g) => {
          const key = g.project ?? ''
          const folded = collapsed.has(key)
          return (
            <div key={key || '\u0000none'} className="card-group">
              <div className="card-group-head">
                <button
                  className="twisty"
                  aria-expanded={!folded}
                  onClick={() =>
                    setCollapsed((prev) => {
                      const next = new Set(prev)
                      if (!next.delete(key)) next.add(key)
                      return next
                    })
                  }
                >
                  {folded ? '\u25b8' : '\u25be'}
                </button>
                <span className="card-group-name">
                  {g.project ?? t('프로젝트 없음')}
                </span>
                <span className="muted small">
                  {g.project
                    ? t('{n}개 · {r} 실행 중', { n: g.members.length, r: g.running })
                    : t('{n}개', { n: g.shown.length })}
                </span>
                <span className="spacer" />
                {/* Only for real projects: "no project" is not a thing Compose
                    can be told to start. */}
                {g.project &&
                  (['start', 'stop', 'restart'] as const).map((action) => (
                    <button
                      key={action}
                      disabled={pending === PROJECT_PENDING + g.project}
                      onClick={() =>
                        setConfirmProject({ project: g.project!, action, members: g.members })
                      }
                    >
                      {action === 'start' && t('전체 시작')}
                      {action === 'stop' && t('전체 중지')}
                      {action === 'restart' && t('전체 재시작')}
                    </button>
                  ))}
              </div>

              {!folded && (
                <div className="card-grid">
                  {g.shown.map((c) => {
              const running = c.state === 'running'
              const busy = pending === c.id
              return (
                <div key={c.id} className="card" data-running={running || undefined} data-busy={busy || undefined}>
                  <div className="card-head">
                    <span className="dot" data-status={running ? 'active' : c.exitCode > 0 ? 'failed' : undefined} />
                    <strong className="ellipsis">{c.name}</strong>
                    <span className="muted small ellipsis">{c.image}</span>
                    {/* The group header already names the project, so the only
                        thing left to say is that Compose will skip this one. */}
                    {c.compose?.oneOff && (
                      <span className="badge" title={t('compose run 으로 만들어진 컨테이너입니다. 프로젝트 전체 동작에 포함되지 않습니다.')}>
                        {t('일회성')}
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
                        <button
                          disabled={busy}
                          onClick={() =>
                            void run(c.id, (e) => ContainerAction(hostID, c.id, 'restart', e))
                          }
                        >
                          {t('재시작')}
                        </button>
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
              )}
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

      {/* A card button is the user's own container coming back. A header
          button takes the others with it, and the list of which is already on
          screen — so it is shown rather than described. No extra command: the
          names come from the poll that drew the cards. */}
      {confirmProject && (
        <div className="scrim">
          <div className="dialog">
            <h2>
              {confirmProject.action === 'start' && t('프로젝트 전체를 시작하시겠습니까?')}
              {confirmProject.action === 'stop' && t('프로젝트 전체를 중지하시겠습니까?')}
              {confirmProject.action === 'restart' && t('프로젝트 전체를 재시작하시겠습니까?')}
            </h2>
            <dl className="keyinfo">
              <dt>{t('프로젝트')}</dt>
              <dd className="mono">{confirmProject.project}</dd>
              <dt>{t('대상')}</dt>
              <dd className="mono">
                {confirmProject.members.map((c) => c.name).join(' · ')}
              </dd>
            </dl>
            <div className="dialog-actions">
              <button onClick={() => setConfirmProject(null)}>{t('취소')}</button>
              <button
                className={confirmProject.action === 'stop' ? 'danger' : 'primary'}
                onClick={() => {
                  const { project, action } = confirmProject
                  setConfirmProject(null)
                  void run(PROJECT_PENDING + project, (e) =>
                    ComposeAction(hostID, project, '', action, e),
                  )
                }}
              >
                {VERB_LABEL[confirmProject.action]()}
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
