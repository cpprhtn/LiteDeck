import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { usePoll } from './usePoll'
import { useVirtualizer } from '@tanstack/react-virtual'
import {
  KillProcess,
  ListProcesses,
  ProcessExists,
  Renice,
  type ProcessInfo,
} from './ipc'

// The process view (§4.4): a task-manager table over a remote host.
//
// Polls faster than the service view because CPU and memory are the numbers
// people watch move. Only while visible, and never overlapping (§3.2d).

const ROW_HEIGHT = 26
const COLUMNS = '72px 96px 60px 60px 82px 52px 78px 1fr'
const POLL_MS = 2000

type SortKey = 'pid' | 'user' | 'cpu' | 'mem' | 'rss' | 'elapsed' | 'command'

function fmtKiB(kb: number): string {
  if (kb >= 1024 * 1024) return `${(kb / 1024 / 1024).toFixed(1)}G`
  if (kb >= 1024) return `${(kb / 1024).toFixed(1)}M`
  return `${kb}K`
}

function fmtElapsed(sec: number): string {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}`
  return `${m}:${String(Math.floor(sec % 60)).padStart(2, '0')}`
}

export function ProcessView({
  hostID,
  visible,
  onError,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [procs, setProcs] = useState<ProcessInfo[]>([])
  const [asTree, setAsTree] = useState(false)
  const [query, setQuery] = useState('')
  const [sortKey, setSortKey] = useState<SortKey>('cpu')
  const [desc, setDesc] = useState(true)
  const [selected, setSelected] = useState<number | null>(null)
  const [pending, setPending] = useState(false)
  const [loading, setLoading] = useState(true)
  const [confirmKill, setConfirmKill] = useState<ProcessInfo | null>(null)
  const [needsRoot, setNeedsRoot] = useState<{ retry: () => void; message: string } | null>(
    null,
  )
  const scrollRef = useRef<HTMLDivElement>(null)
  const inFlight = useRef(false)

  const refresh = useCallback(async () => {
    // A slow host must not queue polls behind each other (§3.2d).
    if (inFlight.current) return
    inFlight.current = true
    try {
      setProcs((await ListProcesses(hostID, asTree)) ?? [])
    } catch (e) {
      onError(String(e))
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [hostID, asTree, onError])

  usePoll(refresh, POLL_MS, visible)

  const run = async (fn: () => Promise<{ ok: boolean; needsElevation: boolean; error?: string }>, retry: () => void) => {
    setPending(true)
    setNeedsRoot(null)
    try {
      const res = await fn()
      if (!res.ok) {
        if (res.needsElevation) {
          setNeedsRoot({ retry, message: res.error ?? '권한이 필요합니다' })
        } else {
          onError(res.error ?? '실패했습니다')
        }
        return false
      }
      await refresh()
      return true
    } catch (e) {
      onError(String(e))
      return false
    } finally {
      setPending(false)
    }
  }

  // §3.4: ask politely first. Only if the process is still there afterwards is
  // KILL offered, and that needs its own confirmation (§7.4) — TERM lets a
  // program flush its state, KILL does not.
  const terminate = async (p: ProcessInfo) => {
    const ok = await run(
      () => KillProcess(hostID, p.pid, 'TERM', false),
      () => void KillProcess(hostID, p.pid, 'TERM', true).then(() => refresh()),
    )
    if (!ok) return
    window.setTimeout(async () => {
      try {
        if (await ProcessExists(hostID, p.pid)) setConfirmKill(p)
      } catch {
        /* the process list will show the truth soon enough */
      }
    }, 1500)
  }

  const forceKill = async (p: ProcessInfo) => {
    setConfirmKill(null)
    await run(
      () => KillProcess(hostID, p.pid, 'KILL', false),
      () => void KillProcess(hostID, p.pid, 'KILL', true).then(() => refresh()),
    )
  }

  const renice = async (p: ProcessInfo, nice: number) => {
    await run(
      () => Renice(hostID, p.pid, nice, false),
      () => void Renice(hostID, p.pid, nice, true).then(() => refresh()),
    )
  }

  const rows = useMemo(() => {
    const needle = query.trim().toLowerCase()
    const filtered = needle
      ? procs.filter(
          (p) =>
            p.command.toLowerCase().includes(needle) ||
            p.args.toLowerCase().includes(needle) ||
            p.user.toLowerCase().includes(needle) ||
            String(p.pid) === needle,
        )
      : procs
    // Tree order is itself the sort; re-sorting would destroy the hierarchy.
    if (asTree) return filtered
    const dir = desc ? -1 : 1
    return [...filtered].sort((a, b) => {
      const x = a[sortKey]
      const y = b[sortKey]
      if (typeof x === 'number' && typeof y === 'number') return (x - y) * dir
      return String(x).localeCompare(String(y)) * dir
    })
  }, [procs, query, sortKey, desc, asTree])

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
  })

  const header = (key: SortKey, label: string, numeric = false) => (
    <div
      className={numeric ? 'num sortable' : 'sortable'}
      onClick={() => {
        if (asTree) return
        if (sortKey === key) setDesc((d) => !d)
        else {
          setSortKey(key)
          setDesc(true)
        }
      }}
      data-sorted={!asTree && sortKey === key ? (desc ? 'desc' : 'asc') : undefined}
    >
      {label}
      {!asTree && sortKey === key && (desc ? ' ▾' : ' ▴')}
    </div>
  )

  const chosen = rows.find((p) => p.pid === selected)
  const zombies = procs.filter((p) => p.state.startsWith('Z')).length

  return (
    <div className="view">
      <div className="view-toolbar">
        <div className="segmented">
          <button data-on={!asTree || undefined} onClick={() => setAsTree(false)}>
            목록 {procs.length}
          </button>
          <button data-on={asTree || undefined} onClick={() => setAsTree(true)}>
            트리
          </button>
        </div>
        <input
          className="search"
          placeholder="명령 · 사용자 · PID 검색"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {zombies > 0 && <span className="badge warn">좀비 {zombies}</span>}
        <button className="ghost" onClick={() => void refresh()}>
          새로고침
        </button>
      </div>

      <div className="table">
        <div className="thead" style={{ gridTemplateColumns: COLUMNS }}>
          {header('pid', 'PID', true)}
          {header('user', 'USER')}
          {header('cpu', 'CPU%', true)}
          {header('mem', 'MEM%', true)}
          {header('rss', 'RSS', true)}
          <div>STATE</div>
          {header('elapsed', 'TIME', true)}
          {header('command', 'COMMAND')}
        </div>

        <div className="tbody" ref={scrollRef}>
          {loading && <div className="placeholder">프로세스를 읽는 중…</div>}
          {!loading && rows.length === 0 && (
            <div className="placeholder">조건에 맞는 프로세스가 없습니다.</div>
          )}
          <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
            {virtualizer.getVirtualItems().map((item) => {
              const p = rows[item.index]
              const zombie = p.state.startsWith('Z')
              return (
                <div
                  key={p.pid}
                  className="trow selectable-row"
                  data-selected={p.pid === selected || undefined}
                  style={{
                    gridTemplateColumns: COLUMNS,
                    height: ROW_HEIGHT,
                    transform: `translateY(${item.start}px)`,
                  }}
                  onClick={() => setSelected(p.pid)}
                >
                  <div className="num mono">{p.pid}</div>
                  <div className="ellipsis">{p.user}</div>
                  <div className="num mono" data-hot={p.cpu > 50 || undefined}>
                    {p.cpu.toFixed(1)}
                  </div>
                  <div className="num mono">{p.mem.toFixed(1)}</div>
                  <div className="num mono">{fmtKiB(p.rss)}</div>
                  <div className="mono" data-zombie={zombie || undefined}>
                    {p.state}
                  </div>
                  <div className="num mono">{fmtElapsed(p.elapsed)}</div>
                  <div className="ellipsis" style={{ paddingLeft: (p.depth ?? 0) * 14 }}>
                    <span className="mono">{p.command}</span>{' '}
                    <span className="muted">{p.args}</span>
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      {chosen && (
        <div className="detail">
          <div className="detail-head">
            <span className="mono ellipsis">
              {chosen.pid} · {chosen.command}
            </span>
            <button className="ghost small-btn" onClick={() => setSelected(null)}>
              닫기
            </button>
          </div>
          <div className="mono muted small ellipsis" style={{ marginBottom: 'var(--sp-2)' }}>
            {chosen.args}
          </div>
          <div className="detail-actions">
            <button disabled={pending} onClick={() => void terminate(chosen)}>
              종료 (TERM)
            </button>
            <button disabled={pending} onClick={() => setConfirmKill(chosen)}>
              강제 종료 (KILL)
            </button>
            <button disabled={pending} onClick={() => void renice(chosen, 10)}>
              우선순위 낮추기
            </button>
            <button disabled={pending} onClick={() => void renice(chosen, -5)}>
              우선순위 높이기
            </button>
          </div>
          {chosen.state.startsWith('Z') && (
            <p className="muted small">
              좀비 프로세스입니다 — 이미 종료됐고 부모({chosen.ppid})가 수거하지
              않은 상태라, 시그널을 보내도 사라지지 않습니다.
            </p>
          )}

          {needsRoot && (
            <div className="elevate">
              <span>{needsRoot.message}</span>
              <button
                className="primary small-btn"
                disabled={pending}
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
        </div>
      )}

      {confirmKill && (
        <div className="scrim">
          <div className="dialog" role="dialog" aria-modal="true">
            <h2>강제 종료하시겠습니까?</h2>
            <p className="muted">
              SIGKILL은 프로세스에 정리할 기회를 주지 않습니다. 쓰던 데이터가
              유실될 수 있습니다.
            </p>
            <dl className="keyinfo">
              <dt>PID</dt>
              <dd className="mono">{confirmKill.pid}</dd>
              <dt>사용자</dt>
              <dd className="mono">{confirmKill.user}</dd>
              <dt>명령</dt>
              <dd className="mono selectable">{confirmKill.args || confirmKill.command}</dd>
            </dl>
            <div className="dialog-actions">
              <button onClick={() => setConfirmKill(null)}>취소</button>
              <button className="danger" onClick={() => void forceKill(confirmKill)}>
                강제 종료
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
