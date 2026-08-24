import { useCallback, useEffect, useRef, useState } from 'react'
import { usePoll } from './usePoll'
import { useVirtualizer } from '@tanstack/react-virtual'
import { LogPanel } from './LogPanel'
import { TimersView } from './TimersView'
import {
  FollowServiceLog,
  ListServices,
  ServiceAction,
  StopLogStream,
  type LogStream,
  type ServiceUnit,
} from './ipc'
import { t, k } from './i18n'

// The service view (§4.3). Rows come from two systemctl commands merged into
// one table, so a unit that is installed but disabled still appears — enabling
// it is usually why the user opened this tab.

const ROW_HEIGHT = 30
const COLUMNS = '18px 1fr 90px 96px 2fr'

/** Polling interval for this view (§3.2d). Only the visible tab polls. */
const POLL_MS = 5000

type Filter = 'all' | 'failed' | 'active'

const ACTIONS = [
  { verb: 'start', label: k('시작') },
  { verb: 'stop', label: k('중지') },
  { verb: 'restart', label: k('재시작') },
  { verb: 'reload', label: k('리로드') },
  { verb: 'enable', label: 'enable' },
  { verb: 'disable', label: 'disable' },
] as const

function statusOf(u: ServiceUnit): 'failed' | 'active' | 'inactive' | 'unloaded' {
  if (u.active === 'failed') return 'failed'
  if (u.active === 'active') return 'active'
  if (!u.load) return 'unloaded'
  return 'inactive'
}

export function ServiceView({
  hostID,
  visible,
  onError,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [units, setUnits] = useState<ServiceUnit[]>([])
  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)
  const [needsRoot, setNeedsRoot] = useState<{
    unit: string
    verb: string
    message: string
  } | null>(null)
  const [loading, setLoading] = useState(true)
  const [pane, setPane] = useState<'services' | 'timers'>('services')
  /** The open follow, with what it was opened on so it can be reopened. */
  const [log, setLog] = useState<{
    stream: LogStream
    unit: string
    elevated: boolean
  } | null>(null)
  const [logNeedsRoot, setLogNeedsRoot] = useState<{ unit: string; message: string } | null>(
    null,
  )
  const scrollRef = useRef<HTMLDivElement>(null)

  const refresh = useCallback(async () => {
    try {
      setUnits((await ListServices(hostID)) ?? [])
    } catch (e) {
      onError(String(e))
    } finally {
      setLoading(false)
    }
  }, [hostID, onError])

  // Only the visible tab polls, and it stops the moment it is hidden (§3.2d).
  // With no agent on the server this is the only way to stay current, so it has
  // to be frugal.
  usePoll(refresh, POLL_MS, visible)

  // A followed log on a hidden tab would hold one of the connection's
  // long-lived channels and go on receiving lines nobody can see. Dropping it
  // is what unmounting the whole view used to do, and LogPanel stops the
  // stream as it goes.
  useEffect(() => {
    if (!visible) setLog(null)
  }, [visible])

  // §7.2: run as the logged-in user first. Only if the server refuses do we
  // offer to retry as administrator — LiteDeck never reaches for root on its
  // own, because then the Command Log would stop matching what the user asked
  // for, and that log is the whole basis for trusting the app (§4.6).
  const act = async (unit: string, verb: string, elevate = false) => {
    setPending(unit)
    setNeedsRoot(null)
    try {
      const res = await ServiceAction(hostID, unit, verb, elevate)
      if (!res.ok) {
        if (res.needsElevation) {
          setNeedsRoot({ unit, verb, message: res.error ?? t('권한이 필요합니다') })
        } else {
          onError(res.error ?? t('실패했습니다'))
        }
        return
      }
      // Refresh immediately rather than waiting out the interval: the user
      // just did something and wants to see it (§3.2d).
      await refresh()
    } catch (e) {
      onError(String(e))
    } finally {
      setPending(null)
    }
  }

  // A user outside systemd-journal sees only their own messages and journalctl
  // does not fail — an empty panel would read as "this service never logged".
  // The Go side refuses instead, and the offer to elevate is made here.
  const openLog = async (unit: string, elevate: boolean) => {
    try {
      // One follow at a time: each holds a channel from the long-lived budget.
      if (log) await StopLogStream(log.stream.id)
      setLog({ stream: await FollowServiceLog(hostID, unit, 200, elevate), unit, elevated: elevate })
      setLogNeedsRoot(null)
    } catch (e) {
      const msg = String(e)
      if (msg.includes('systemd-journal')) {
        setLogNeedsRoot({ unit, message: msg })
      } else {
        onError(msg)
      }
    }
  }

  const needle = query.trim().toLowerCase()
  const rows = units.filter((u) => {
    if (filter === 'failed' && statusOf(u) !== 'failed') return false
    if (filter === 'active' && statusOf(u) !== 'active') return false
    if (!needle) return true
    return (
      u.name.toLowerCase().includes(needle) ||
      (u.description ?? '').toLowerCase().includes(needle)
    )
  })

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 10,
  })

  const failedCount = units.filter((u) => statusOf(u) === 'failed').length
  const chosen = rows.find((u) => u.name === selected)

  if (pane === 'timers') {
    return (
      <div className="view">
        <div className="view-toolbar">
          <div className="segmented">
            <button onClick={() => setPane('services')}>{t('서비스')}</button>
            <button data-on>{t('타이머')}</button>
          </div>
        </div>
        <TimersView hostID={hostID} visible={visible} onError={onError} />
      </div>
    )
  }

  return (
    <div className="view">
      <div className="view-toolbar">
        <div className="segmented">
          <button data-on>{t('서비스')}</button>
          <button onClick={() => setPane('timers')}>{t('타이머')}</button>
        </div>
        <div className="segmented">
          <button data-on={filter === 'all' || undefined} onClick={() => setFilter('all')}>
            {t('전체 {n}', { n: units.length })}
          </button>
          <button data-on={filter === 'active' || undefined} onClick={() => setFilter('active')}>
            {t('실행 중')}
          </button>
          <button
            data-on={filter === 'failed' || undefined}
            data-danger={failedCount > 0 || undefined}
            onClick={() => setFilter('failed')}
          >
            failed {failedCount}
          </button>
        </div>
        <input
          className="search"
          placeholder={t('이름 · 설명 검색')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button className="ghost" onClick={() => void refresh()}>
          {t('새로고침')}
        </button>
      </div>

      <div className="table">
        <div className="thead" style={{ gridTemplateColumns: COLUMNS }}>
          <div />
          <div>UNIT</div>
          <div>ACTIVE</div>
          <div>ENABLED</div>
          <div>DESCRIPTION</div>
        </div>

        <div className="tbody" ref={scrollRef}>
          {loading && <div className="placeholder">{t('서비스 목록을 읽는 중…')}</div>}
          {!loading && rows.length === 0 && (
            <div className="placeholder">{t('조건에 맞는 유닛이 없습니다.')}</div>
          )}
          <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
            {virtualizer.getVirtualItems().map((item) => {
              const u = rows[item.index]
              const status = statusOf(u)
              return (
                <div
                  key={u.name}
                  className="trow selectable-row"
                  data-selected={u.name === selected || undefined}
                  data-busy={pending === u.name || undefined}
                  style={{
                    gridTemplateColumns: COLUMNS,
                    height: ROW_HEIGHT,
                    transform: `translateY(${item.start}px)`,
                  }}
                  onClick={() => setSelected(u.name)}
                >
                  <span className="dot" data-status={status} />
                  <div className="ellipsis mono">
                    {u.name}
                    {u.template && <span className="badge">template</span>}
                  </div>
                  <div className="ellipsis" data-status={status}>
                    {u.active || '—'}
                  </div>
                  <div className="ellipsis muted">{u.enabled || '—'}</div>
                  <div className="ellipsis muted">{u.description || ''}</div>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      {logNeedsRoot && (
        <div className="elevate">
          <span>{logNeedsRoot.message}</span>
          <button
            className="primary small-btn"
            onClick={() => void openLog(logNeedsRoot.unit, true)}
          >
            {t('관리자 권한으로 보기')}
          </button>
          <button className="ghost small-btn" onClick={() => setLogNeedsRoot(null)}>
            {t('취소')}
          </button>
        </div>
      )}

      {log && (
        <LogPanel
          stream={log.stream}
          onClose={() => setLog(null)}
          onReopen={() => void openLog(log.unit, log.elevated)}
        />
      )}

      {chosen && (
        <div className="detail">
          <div className="detail-head">
            <span className="mono">{chosen.name}</span>
            <button className="ghost small-btn" onClick={() => setSelected(null)}>
              {t('닫기')}
            </button>
          </div>
          <div className="detail-actions">
            <button disabled={chosen.template} onClick={() => void openLog(chosen.name, false)}>
              {t('로그 보기')}
            </button>
            {ACTIONS.map((a) => (
              <button
                key={a.verb}
                disabled={pending === chosen.name || chosen.template}
                onClick={() => void act(chosen.name, a.verb)}
              >
                {t(a.label)}
              </button>
            ))}
          </div>
          {chosen.template && (
            <p className="muted small">
              {t('템플릿 유닛은 직접 실행할 수 없습니다 — 인스턴스만 가능합니다.')}
            </p>
          )}

          {needsRoot && needsRoot.unit === chosen.name && (
            <div className="elevate">
              <span>{needsRoot.message}</span>
              <button
                className="primary small-btn"
                disabled={pending === chosen.name}
                onClick={() => void act(needsRoot.unit, needsRoot.verb, true)}
              >
                {t('관리자 권한으로 재시도')}
              </button>
              <button className="ghost small-btn" onClick={() => setNeedsRoot(null)}>
                {t('취소')}
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
