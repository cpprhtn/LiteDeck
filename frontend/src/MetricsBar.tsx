import { useCallback, useEffect, useRef, useState } from 'react'
import { HostMetrics, type GPU, type MetricsView } from './ipc'
import { usePoll } from './usePoll'
import {
  GAP_MS,
  forgetMetrics,
  publishMetrics,
  useMetricsHistory,
  type Sample,
} from './metricsStore'
import { t } from './i18n'

// The summary bar (§4.7). Shown above every tab for the connected host, so
// "is this box healthy" is answered without navigating anywhere.
//
// A supporting feature, deliberately: four numbers and a sparkline each. A real
// monitoring stack does the rest, and §1.5 keeps LiteDeck out of that business.

const POLL_MS = 2000

function fmtBytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)}G`
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(0)}M`
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(0)}K`
  return `${n}B`
}

function fmtUptime(sec: number): string {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return t('{d}일 {h}시간', { d, h })
  if (h > 0) return t('{h}시간 {m}분', { h, m })
  return t('{m}분', { m })
}

/**
 * A sparkline over the recent history.
 *
 * Drawn as an SVG polyline rather than a canvas: it is a handful of points,
 * it scales with the container for free, and it inherits `currentColor` so the
 * theme applies without a redraw.
 */
function Sparkline({
  samples,
  pick,
  warn,
}: {
  samples: Sample[]
  pick: (s: Sample) => number
  warn?: boolean
}) {
  const w = 56
  const h = 16
  const usable = samples.filter((s) => pick(s) >= 0)
  if (usable.length < 2) return <svg className="spark" width={w} height={h} />

  // x is time, not index. Spacing the points evenly drew half an hour of idle
  // polling as though it were two minutes of active polling — the shape stayed
  // while the axis under it quietly changed scale (A-45).
  const t0 = usable[0].t
  const span = Math.max(1, usable[usable.length - 1].t - t0)

  // Fixed 0–100 scale. Auto-scaling would make 2% CPU look identical to 90%,
  // which is exactly the distinction the bar exists to show.
  const at = (s: Sample) =>
    `${(((s.t - t0) / span) * w).toFixed(1)},${(h - (Math.max(0, Math.min(100, pick(s))) / 100) * h).toFixed(1)}`

  // One run per stretch that was actually recorded. A gap is the app having
  // been away, and drawing through it invents a reading nobody took.
  const runs: string[][] = [[]]
  usable.forEach((s, i) => {
    if (i > 0 && s.t - usable[i - 1].t > GAP_MS) runs.push([])
    runs[runs.length - 1].push(at(s))
  })

  return (
    <svg className="spark" width={w} height={h} data-warn={warn || undefined}>
      {runs
        .filter((r) => r.length >= 2)
        .map((r, i) => (
          <polyline
            key={i}
            points={r.join(' ')}
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
          />
        ))}
    </svg>
  )
}

// -1 is "the card did not answer", which is not the same as zero: a passively
// cooled datacentre card really does read 0 RPM.
function fmtPct(v: number): string {
  return v < 0 ? '—' : v.toFixed(0)
}

function fmtTemp(v: number): string {
  return v < 0 ? '—' : `${v.toFixed(0)}°C`
}

function gpuTitle(g: GPU): string {
  const parts = [g.name]
  if (g.tempC >= 0) parts.push(fmtTemp(g.tempC))
  if (g.fan >= 0) parts.push(t('팬 {f}%', { f: g.fan.toFixed(0) }))
  if (g.memTotal > 0) parts.push(`${fmtBytes(g.memUsed)} / ${fmtBytes(g.memTotal)}`)
  return parts.join(' · ')
}

// A card is worth flagging when it is pinned or hot; either one is a reason to
// look before starting more work on it.
function gpuWarn(g: GPU): boolean {
  return g.utilization >= 90 || g.tempC >= 85
}

function Stat({
  label,
  value,
  unit,
  note,
  samples,
  pick,
  warn,
  title,
}: {
  label: string
  value: string
  unit?: string
  // A second, smaller figure on the same line. Fan speed rides along with GPU
  // load here rather than taking a tile of its own: it is the number you check
  // after the load, not instead of it.
  note?: string
  samples?: Sample[]
  pick?: (s: Sample) => number
  warn?: boolean
  title?: string
}) {
  return (
    <div className="metric" data-warn={warn || undefined} title={title}>
      <div className="metric-label">{label}</div>
      <div className="metric-row">
        <span className="metric-value">
          {value}
          {unit && <span className="metric-unit">{unit}</span>}
        </span>
        {note && <span className="metric-note">{note}</span>}
        {samples && pick && <Sparkline samples={samples} pick={pick} warn={warn} />}
      </div>
    </div>
  )
}

export function MetricsBar({ hostID }: { hostID: string }) {
  const [m, setM] = useState<MetricsView | null>(null)
  const [gpuOpen, setGpuOpen] = useState(false)
  const samples = useMetricsHistory(hostID)
  const [failed, setFailed] = useState<string | null>(null)
  const inFlight = useRef(false)
  const gpuPopRef = useRef<HTMLDivElement | null>(null)

  // The panel is an overlay over the tab below it, so it closes the way every
  // other overlay does: click away, or Escape.
  useEffect(() => {
    if (!gpuOpen) return
    const onDown = (e: MouseEvent) => {
      if (!gpuPopRef.current?.contains(e.target as Node)) setGpuOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setGpuOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [gpuOpen])

  useEffect(() => {
    // Host changed: the previous host's history says nothing about this one.
    // History lives in the store, which forgetMetrics clears — the bar keeps
    // no second copy to fall out of step with it.
    forgetMetrics(hostID)
    setM(null)
    setGpuOpen(false)
    setFailed(null)
  }, [hostID])

  // This bar sits above the tabs, so it polls for as long as the host is
  // connected — every view's worth of load put together is smaller than this
  // one running unattended all day, which is why it goes through usePoll.
  const tick = useCallback(async () => {
    if (inFlight.current) return
    inFlight.current = true
    try {
      const next = await HostMetrics(hostID)
      setM(next)
      // Anything else that wants these numbers reads them from here rather
      // than polling for its own copy — see metricsStore.
      publishMetrics(hostID, next)
      setFailed(null)
    } catch (e) {
      // The bar must never interrupt what the user is doing: a failure here is
      // shown in place, not raised as an app-level error.
      setFailed(String(e))
    } finally {
      inFlight.current = false
    }
  }, [hostID])

  usePoll(tick, POLL_MS)

  if (failed && !m) {
    return (
      <div className="metrics-bar">
        <span className="muted small">{t('상태를 읽지 못했습니다 — {err}', { err: failed })}</span>
      </div>
    )
  }
  if (!m) {
    return (
      <div className="metrics-bar">
        <span className="muted small">{t('상태를 읽는 중…')}</span>
      </div>
    )
  }

  const disk = m.disks?.[0]
  const gpus = m.gpus ?? []
  // The busiest card is what answers "is this box working right now". An
  // average across an idle second card would hide a pinned first one.
  const gpuBusy = gpus.reduce((a, g) => Math.max(a, g.utilization), -1)
  const gpuFan = gpus.reduce((a, g) => Math.max(a, g.fan), -1)

  return (
    <div className="metrics-bar">
      <Stat
        label="CPU"
        value={m.cpu < 0 ? '—' : m.cpu.toFixed(0)}
        unit={m.cpu < 0 ? undefined : '%'}
        samples={samples}
        pick={(s) => s.cpu}
        warn={m.cpu >= 85}
        title={m.cpu < 0 ? t('두 번째 샘플을 기다리는 중 — 누적 카운터라 한 번만으로는 알 수 없습니다') : undefined}
      />
      <Stat
        label={t('메모리')}
        value={m.memPercent.toFixed(0)}
        unit="%"
        samples={samples}
        pick={(s) => s.mem}
        warn={m.memPercent >= 90}
        title={`${fmtBytes(m.memUsed)} / ${fmtBytes(m.memTotal)}`}
      />
      {/* Sits with CPU and memory rather than at the end: on a box that has a
          card at all, it is the figure being watched. */}
      {gpus.length === 1 && (
        <Stat
          label="GPU"
          value={fmtPct(gpus[0].utilization)}
          unit={gpus[0].utilization < 0 ? undefined : '%'}
          note={gpus[0].fan < 0 ? fmtTemp(gpus[0].tempC) : t('팬 {f}%', { f: fmtPct(gpus[0].fan) })}
          samples={samples}
          pick={(s) => s.gpu[0] ?? -1}
          warn={gpuWarn(gpus[0])}
          title={gpuTitle(gpus[0])}
        />
      )}
      {/* Eight cards would eat the whole bar, so the many-card case collapses
          to the busiest one and opens the rest on click. */}
      {gpus.length > 1 && (
        <div className="metric-pop" ref={gpuPopRef}>
          <button
            type="button"
            className="metric-btn"
            aria-expanded={gpuOpen}
            onClick={() => setGpuOpen((o) => !o)}
            title={t('GPU {n}개 — 눌러서 카드별로 보기', { n: gpus.length })}
          >
            <Stat
              label={t('GPU ×{n}', { n: gpus.length })}
              value={fmtPct(gpuBusy)}
              unit={gpuBusy < 0 ? undefined : '%'}
              note={gpuFan < 0 ? undefined : t('팬 {f}%', { f: fmtPct(gpuFan) })}
              samples={samples}
              // The busiest card per sample, not one chosen card: following a
              // single line would make it jump as the lead changes hands.
              pick={(s) => (s.gpu.length ? Math.max(...s.gpu) : -1)}
              warn={gpus.some(gpuWarn)}
            />
            <span className="metric-caret" aria-hidden="true">
              {gpuOpen ? '▴' : '▾'}
            </span>
          </button>
          {gpuOpen && (
            <div className="gpu-panel">
              {gpus.map((g, i) => (
                <div className="gpu-row" key={g.index} data-warn={gpuWarn(g) || undefined}>
                  <span className="gpu-idx">#{g.index}</span>
                  <span className="gpu-name" title={g.name}>
                    {g.name}
                  </span>
                  <span className="gpu-num metric-value">
                    {g.utilization < 0 ? '—' : `${fmtPct(g.utilization)}%`}
                  </span>
                  <Sparkline
                    samples={samples}
                    pick={(sm) => sm.gpu[i] ?? -1}
                    warn={gpuWarn(g)}
                  />
                  <span className="gpu-num muted">
                    {g.fan < 0 ? t('팬 —') : t('팬 {f}%', { f: fmtPct(g.fan) })}
                  </span>
                  <span className="gpu-num muted">{fmtTemp(g.tempC)}</span>
                  <span className="gpu-num muted">
                    {g.memTotal > 0 ? `${fmtBytes(g.memUsed)} / ${fmtBytes(g.memTotal)}` : '—'}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
      {disk && (
        <Stat
          label={t('디스크 {mount}', { mount: disk.mountPoint })}
          value={disk.percent.toFixed(0)}
          unit="%"
          warn={disk.percent >= 90}
          title={t('{used} / {size} · 여유 {free}', {
            used: fmtBytes(disk.used),
            size: fmtBytes(disk.size),
            free: fmtBytes(disk.available),
          })}
        />
      )}
      {/* Hidden where the concept does not exist rather than shown as 0.00.
          Windows has no load average, and a zero here reads as an idle machine
          instead of as a figure that was never available. */}
      {m.hasLoad && (
        <Stat
          label={t('로드')}
          value={`${m.load1.toFixed(2)}`}
          title={t('1분 {a} · 5분 {b} · 15분 {c}', { a: m.load1, b: m.load5, c: m.load15 })}
        />
      )}
      {m.swapTotal > 0 && (
        <Stat
          label={t('스왑')}
          value={fmtBytes(m.swapUsed)}
          warn={m.swapUsed > m.swapTotal * 0.5}
          title={`${fmtBytes(m.swapUsed)} / ${fmtBytes(m.swapTotal)}`}
        />
      )}
      <span className="spacer" />
      <span className="muted small" title={t('서버 가동 시간')}>
        {t('가동 {up}', { up: fmtUptime(m.uptimeSeconds) })}
      </span>
      {failed && (
        <span className="badge warn" title={failed}>
          {t('갱신 실패')}
        </span>
      )}
    </div>
  )
}
