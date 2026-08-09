import { useCallback, useEffect, useRef, useState } from 'react'
import { usePoll } from './usePoll'
import { ImagesVolumes } from './ImagesVolumes'
import { LogPanel } from './LogPanel'
import {
  ContainerAction,
  FollowContainerLog,
  ListContainers,
  RemoveContainer,
  StopLogStream,
  type Container,
  type LogStream,
} from './ipc'

// The container view (§4.5). Cards rather than a table: a host runs a handful of
// containers, not thousands, and the things people want at a glance — image,
// ports, why it stopped — do not fit a row comfortably.

const POLL_MS = 5000

type Filter = 'all' | 'running' | 'stopped'

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
            message: res.error ?? '권한이 필요합니다',
            retry: () => void fn(true).then(() => refresh()),
          })
        } else {
          onError(res.error ?? '실패했습니다')
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
            <button onClick={() => setPane('containers')}>컨테이너</button>
            <button data-on>이미지 · 볼륨</button>
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
          <button data-on>컨테이너</button>
          <button onClick={() => setPane('storage')}>이미지 · 볼륨</button>
        </div>
        <div className="segmented">
          <button data-on={filter === 'all' || undefined} onClick={() => setFilter('all')}>
            전체 {containers.length}
          </button>
          <button
            data-on={filter === 'running' || undefined}
            onClick={() => setFilter('running')}
          >
            실행 중 {runningCount}
          </button>
          <button
            data-on={filter === 'stopped' || undefined}
            data-danger={crashed > 0 || undefined}
            onClick={() => setFilter('stopped')}
          >
            중지됨 {containers.length - runningCount}
          </button>
        </div>
        <span className="spacer" />
        {crashed > 0 && <span className="badge danger">비정상 종료 {crashed}</span>}
        <button className="ghost" onClick={() => void refresh()}>
          새로고침
        </button>
      </div>

      <div className="cards">
        {loading && <div className="placeholder">컨테이너를 읽는 중…</div>}
        {!loading && rows.length === 0 && (
          <div className="placeholder">조건에 맞는 컨테이너가 없습니다.</div>
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
                      중지
                    </button>
                    <button
                      disabled={busy}
                      onClick={() =>
                        void run(c.id, (e) => ContainerAction(hostID, c.id, 'restart', e))
                      }
                    >
                      재시작
                    </button>
                  </>
                ) : (
                  <button
                    disabled={busy}
                    onClick={() =>
                      void run(c.id, (e) => ContainerAction(hostID, c.id, 'start', e))
                    }
                  >
                    시작
                  </button>
                )}
                <button disabled={busy} onClick={() => void showLogs(c)}>
                  로그
                </button>
                <span className="spacer" />
                <button className="danger" disabled={busy} onClick={() => setConfirmRemove(c)}>
                  삭제
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
            관리자 권한으로 재시도
          </button>
          <button className="ghost small-btn" onClick={() => setNeedsRoot(null)}>
            취소
          </button>
        </div>
      )}

      {log && <LogPanel stream={log} onClose={() => setLog(null)} />}

      {confirmRemove && (
        <div className="scrim">
          <div className="dialog">
            <h2>컨테이너를 삭제하시겠습니까?</h2>
            <p className="muted">
              컨테이너와 그 안의 쓰기 계층이 삭제됩니다. 이미지와 명명된 볼륨은
              남습니다.
            </p>
            <dl className="keyinfo">
              <dt>이름</dt>
              <dd className="mono">{confirmRemove.name}</dd>
              <dt>이미지</dt>
              <dd className="mono">{confirmRemove.image}</dd>
              <dt>상태</dt>
              <dd className="mono">{confirmRemove.status}</dd>
            </dl>
            {confirmRemove.state === 'running' && (
              <p className="warn-text">
                실행 중인 컨테이너입니다. 삭제하면 <strong>강제 종료</strong>됩니다.
              </p>
            )}
            <div className="dialog-actions">
              <button onClick={() => setConfirmRemove(null)}>취소</button>
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
                삭제
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
