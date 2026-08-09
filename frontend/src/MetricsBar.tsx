import { useCallback, useEffect, useRef, useState } from 'react'
import { HostMetrics, type MetricsView } from './ipc'
import { usePoll } from './usePoll'

// The summary bar (§4.7). Shown above every tab for the connected host, so
// "is this box healthy" is answered without navigating anywhere.
//
// A supporting feature, deliberately: four numbers and a sparkline each. A real
// monitoring stack does the rest, and §1.5 keeps LiteDeck out of that business.

const POLL_MS = 2000
const HISTORY = 60 // two minutes at the poll rate

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
  if (d > 0) return `${d}일 ${h}시간`
  if (h > 0) return `${h}시간 ${m}분`
  return `${m}분`
}

/**
 * A sparkline over the recent history.
 *
 * Drawn as an SVG polyline rather than a canvas: it is a handful of points,
 * it scales with the container for free, and it inherits `currentColor` so the
 * theme applies without a redraw.
 */
function Sparkline({ values, warn }: { values: number[]; warn?: boolean }) {
  const w = 56
  const h = 16
  if (values.length < 2) return <svg className="spark" width={w} height={h} />

  // Fixed 0–100 scale. Auto-scaling would make 2% CPU look identical to 90%,
  // which is exactly the distinction the bar exists to show.
  const step = w / (HISTORY - 1)
  const points = values
    .map((v, i) => {
      const x = (i + (HISTORY - values.length)) * step
      const y = h - (Math.max(0, Math.min(100, v)) / 100) * h
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')

  return (
    <svg className="spark" width={w} height={h} data-warn={warn || undefined}>
      <polyline points={points} fill="none" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  )
}

function Stat({
  label,
  value,
  unit,
  history,
  warn,
  title,
}: {
  label: string
  value: string
  unit?: string
  history?: number[]
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
        {history && <Sparkline values={history} warn={warn} />}
      </div>
    </div>
  )
}

export function MetricsBar({ hostID }: { hostID: string }) {
  const [m, setM] = useState<MetricsView | null>(null)
  const [cpuHist, setCpuHist] = useState<number[]>([])
  const [memHist, setMemHist] = useState<number[]>([])
  const [failed, setFailed] = useState<string | null>(null)
  const inFlight = useRef(false)

  useEffect(() => {
    // Host changed: the previous host's history says nothing about this one.
    setM(null)
    setCpuHist([])
    setMemHist([])
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
      setFailed(null)
      // CPU is -1 until a second sample exists; plotting that would draw a
      // spike down to zero on every connect.
      if (next.cpu >= 0) {
        setCpuHist((h) => [...h, next.cpu].slice(-HISTORY))
      }
      setMemHist((h) => [...h, next.memPercent].slice(-HISTORY))
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
        <span className="muted small">상태를 읽지 못했습니다 — {failed}</span>
      </div>
    )
  }
  if (!m) {
    return (
      <div className="metrics-bar">
        <span className="muted small">상태를 읽는 중…</span>
      </div>
    )
  }

  const disk = m.disks?.[0]

  return (
    <div className="metrics-bar">
      <Stat
        label="CPU"
        value={m.cpu < 0 ? '—' : m.cpu.toFixed(0)}
        unit={m.cpu < 0 ? undefined : '%'}
        history={cpuHist}
        warn={m.cpu >= 85}
        title={m.cpu < 0 ? '두 번째 샘플을 기다리는 중 — 누적 카운터라 한 번만으로는 알 수 없습니다' : undefined}
      />
      <Stat
        label="메모리"
        value={m.memPercent.toFixed(0)}
        unit="%"
        history={memHist}
        warn={m.memPercent >= 90}
        title={`${fmtBytes(m.memUsed)} / ${fmtBytes(m.memTotal)}`}
      />
      {disk && (
        <Stat
          label={`디스크 ${disk.mountPoint}`}
          value={disk.percent.toFixed(0)}
          unit="%"
          warn={disk.percent >= 90}
          title={`${fmtBytes(disk.used)} / ${fmtBytes(disk.size)} · 여유 ${fmtBytes(disk.available)}`}
        />
      )}
      {/* Hidden where the concept does not exist rather than shown as 0.00.
          Windows has no load average, and a zero here reads as an idle machine
          instead of as a figure that was never available. */}
      {m.hasLoad && (
        <Stat
          label="로드"
          value={`${m.load1.toFixed(2)}`}
          title={`1분 ${m.load1} · 5분 ${m.load5} · 15분 ${m.load15}`}
        />
      )}
      {m.swapTotal > 0 && (
        <Stat
          label="스왑"
          value={fmtBytes(m.swapUsed)}
          warn={m.swapUsed > m.swapTotal * 0.5}
          title={`${fmtBytes(m.swapUsed)} / ${fmtBytes(m.swapTotal)}`}
        />
      )}
      <span className="spacer" />
      <span className="muted small" title="서버 가동 시간">
        가동 {fmtUptime(m.uptimeSeconds)}
      </span>
      {failed && (
        <span className="badge warn" title={failed}>
          갱신 실패
        </span>
      )}
    </div>
  )
}
