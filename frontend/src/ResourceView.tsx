import { useState } from 'react'
import type { CPUSplit, Core, Filesystem, GPU } from './ipc'
import { useMetrics, useMetricsHistory } from './metricsStore'
import { TimeChart } from './TimeChart'
import { shortGPUName } from './gpuName'
import { t } from './i18n'

// The resource detail (§4.7, arch/07).
//
// Laid out as a panel grid rather than a stack of full-width sections. The
// question this answers is "is anything wrong", and that is answered by
// *comparing* — CPU against memory against disk — which needs them beside each
// other. Stacked full width, each figure was legible and the comparison was
// three scrolls away, which is the version of this screen nobody opens twice.
//
// Every panel is the same shape on purpose: label, one big number, a line over
// time, then the detail in small type. A grid where the eye has to relearn each
// cell is slower to read than one where it does not, however much better any
// single cell might be on its own.
//
// The readings all come from the summary bar's poll (metricsStore). Nothing
// here asks the server for anything.

function fmtBytes(n: number): string {
  if (n <= 0) return '0B'
  const units = ['B', 'K', 'M', 'G', 'T', 'P']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)}${units[i]}`
}

function fmtUptime(sec: number): string {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return t('{d}일 {h}시간', { d, h })
  if (h > 0) return t('{h}시간 {m}분', { h, m })
  return t('{m}분', { m })
}

/** A percentage that may be unknown. -1 is "no second sample yet" for CPU and
 *  "this card does not report it" for a fan — never a real zero. */
function pct(v: number): string {
  return v < 0 ? '—' : `${Math.round(v)}%`
}

export function ResourceView({ hostID, cpuModel }: { hostID: string; cpuModel?: string }) {
  // No poll of its own: the summary bar above is already reading these numbers
  // for this host, and asking again would be a second round trip for the same
  // answer.
  const m = useMetrics(hostID)
  const history = useMetricsHistory(hostID)

  if (!m) return <div className="placeholder">{t('읽는 중…')}</div>

  const cores = m.cores ?? []
  const swapPct = m.swapTotal > 0 ? (m.swapUsed / m.swapTotal) * 100 : -1

  return (
    <div className="res-pane">
      <div className="res-grid">
        <Panel
          label="CPU"
          value={pct(m.cpu)}
          sub={cpuModel ? [cpuModel] : undefined}
          warn={m.cpu >= 90}
        >
          <TimeChart
            samples={history}
            pick={(s) => s.cpu}
            height={44}
            warnAt={90}
            label={t('CPU 사용률 추이')}
          />
          <SplitBar split={m.split} />
          {cores.length > 1 && <CoreDie cores={cores} />}
        </Panel>

        <Panel
          label={t('메모리')}
          value={pct(m.memPercent)}
          sub={[`${fmtBytes(m.memUsed)} / ${fmtBytes(m.memTotal)}`]}
          warn={m.memPercent >= 90}
        >
          <TimeChart
            samples={history}
            pick={(s) => s.mem}
            height={44}
            warnAt={90}
            label={t('메모리 사용률 추이')}
          />
          {(m.memCached > 0 || m.memBuffers > 0) && (
            <>
              <MemoryBar
                total={m.memTotal}
                used={m.memUsed}
                buffers={m.memBuffers}
                cached={m.memCached}
              />
              <Legend
                items={[
                  { key: 'app', label: t('프로그램'), text: fmtBytes(Math.max(0, m.memUsed - m.memBuffers - m.memCached)) },
                  { key: 'buffers', label: t('버퍼'), text: fmtBytes(m.memBuffers) },
                  { key: 'cached', label: t('캐시'), text: fmtBytes(m.memCached) },
                ]}
              />
            </>
          )}
        </Panel>

        {m.gpus.map((g) => (
          <Panel
            key={g.index}
            label={m.gpus.length > 1 ? t('GPU {n}', { n: g.index }) : 'GPU'}
            value={pct(g.utilization)}
            sub={[shortGPUName(g.name)]}
            title={g.name}
            warn={g.utilization >= 95 || g.tempC >= 85}
          >
            <TimeChart
              samples={history}
              pick={(s) => s.gpu[g.index] ?? -1}
              height={44}
              warnAt={95}
              label={t('GPU {n} 사용률 추이', { n: g.index })}
            />
            <Facts
              rows={[
                // A fan reading of "—" is a passively cooled card, not a stopped
                // fan. Showing 0 there would be alarming and wrong.
                [t('팬'), pct(g.fan)],
                [t('온도'), g.tempC < 0 ? '—' : `${Math.round(g.tempC)}°C`],
                ['VRAM', `${fmtBytes(g.memUsed)} / ${fmtBytes(g.memTotal)}`],
              ]}
            />
          </Panel>
        ))}

        {m.hasLoad && (
          <Panel label={t('로드')} value={m.load1.toFixed(2)} warn={cores.length > 0 && m.load1 > cores.length}>
            {/* Three numbers, because one says nothing: the shape is the
                reading — climbing, falling, or a spike already over. */}
            <Facts
              rows={[
                [t('1분'), m.load1.toFixed(2)],
                [t('5분'), m.load5.toFixed(2)],
                [t('15분'), m.load15.toFixed(2)],
                ...(cores.length ? [[t('코어'), String(cores.length)] as [string, string]] : []),
              ]}
            />
          </Panel>
        )}

        <Panel
          label={t('스왑')}
          value={swapPct < 0 ? '—' : pct(swapPct)}
          sub={[m.swapTotal > 0 ? `${fmtBytes(m.swapUsed)} / ${fmtBytes(m.swapTotal)}` : t('없음')]}
          warn={swapPct >= 50}
        >
          <Facts rows={[[t('가동 시간'), fmtUptime(m.uptimeSeconds)]]} />
        </Panel>
      </div>

      <section className="res-block">
        <h3>{t('파일시스템')}</h3>
        <FilesystemTable rows={m.filesystems} shown={m.disks} />
      </section>
    </div>
  )
}

/** Every panel is the same shape: label, one big number, then whatever detail
 *  that particular resource has. Consistency is the point — a grid the eye has
 *  to relearn per cell reads slower than one it does not. */
function Panel({
  label,
  value,
  sub,
  title,
  warn,
  children,
}: {
  label: string
  value: string
  sub?: string[]
  title?: string
  warn?: boolean
  children?: React.ReactNode
}) {
  return (
    <div className="res-panel" data-warn={warn || undefined}>
      <div className="res-panel-head">
        <span className="res-panel-label">{label}</span>
        <span className="res-panel-value">{value}</span>
      </div>
      {sub?.map((line, i) => (
        <div className="res-panel-sub ellipsis" key={i} title={title ?? line}>
          {line}
        </div>
      ))}
      {children}
    </div>
  )
}

/** key/value pairs on one line each, tight. Small type and tabular numbers so
 *  the values line up down the panel. */
function Facts({ rows }: { rows: [string, string][] }) {
  return (
    <dl className="res-facts">
      {rows.map(([k, v]) => (
        <div key={k}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  )
}

/** The CPU breakdown as one stacked bar with an inline legend.
 *
 *  Stacked rather than four numbers because the shape is the reading: a bar
 *  that is mostly IO wait looks nothing like one that is mostly user, and no
 *  amount of staring at percentages makes that jump out. */
function SplitBar({ split }: { split: CPUSplit }) {
  if (split.user < 0) {
    return <div className="res-note muted">{t('두 번째 표본을 기다리는 중')}</div>
  }
  const parts = [
    { key: 'user', label: t('사용자 시간'), v: split.user, hint: t('프로그램이 직접 쓴 시간') },
    { key: 'system', label: t('커널 시간'), v: split.system, hint: t('커널이 그 프로그램을 대신해 쓴 시간') },
    { key: 'iowait', label: t('IO 대기'), v: split.iowait, hint: t('디스크를 기다린 시간 — 높으면 CPU 가 아니라 디스크가 문제입니다') },
    { key: 'steal', label: t('뺏김'), v: split.steal, hint: t('하이퍼바이저가 다른 손님에게 넘긴 시간 — 이 서버가 느린 이유가 이 서버 밖에 있습니다') },
  ]
  return (
    <>
      <div className="res-split">
        {parts.map((p) => (
          <span
            key={p.key}
            data-part={p.key}
            style={{ width: `${p.v}%` }}
            title={`${p.label} ${p.v.toFixed(1)}% — ${p.hint}`}
          />
        ))}
      </div>
      <Legend
        items={parts.map((p) => ({
          key: p.key,
          label: p.label,
          text: `${p.v.toFixed(0)}%`,
          hint: p.hint,
        }))}
      />
    </>
  )
}

function Legend({
  items,
}: {
  items: { key: string; label: string; text: string; hint?: string }[]
}) {
  return (
    <div className="res-legend">
      {items.map((it) => (
        <span key={it.key} title={it.hint ?? it.label}>
          <i data-part={it.key} />
          {it.label} {it.text}
        </span>
      ))}
    </div>
  )
}

/**
 * The cores, laid out the way a die is rather than as one long row.
 *
 * The aggregate cannot tell "every core half busy" from "one core pinned", and
 * the second is what a single-threaded bottleneck looks like — the most common
 * shape of "the server is slow". A single row made that visible but grew
 * unreadably wide past a dozen cores and read as a waveform, which it is not:
 * the cores have no order that means anything, so a shape implying one is a
 * shape that misleads.
 *
 * A block does not imply order, packs a 64-core machine into the same panel as
 * a 4-core one, and looks like the thing it describes.
 */
function CoreDie({ cores }: { cores: Core[] }) {
  const n = cores.length
  // The nearest clean rectangle, so 8 is 4×2 and 12 is 4×3 rather than both
  // being ragged. A prime count has no clean pair, so it falls back to square.
  let rows = 1
  for (let d = Math.floor(Math.sqrt(n)); d >= 1; d--) {
    if (n % d === 0) {
      rows = d
      break
    }
  }
  const cols = rows > 1 ? n / rows : Math.ceil(Math.sqrt(n))

  // Columns are fractions so the die grows into the panel instead of sitting in
  // its corner — but only up to a cell size, and then it centres. Letting them
  // grow without limit turned a four-core machine in a wide panel into four
  // strips 220px across and 34 tall, which is a bar chart wearing a grid's
  // clothes. Squares that stop growing and sit in the middle read better than
  // rectangles that fill every pixel.
  const cap = n <= 4 ? 64 : n <= 16 ? 52 : n <= 64 ? 40 : 26
  const gap = cap >= 40 ? 3 : 2

  return (
    <div
      className="res-die"
      style={{
        gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))`,
        maxWidth: cols * cap + (cols - 1) * gap,
        gap,
      }}
      role="img"
      aria-label={t('코어 {n}개', { n })}
    >
      {cores.map((c) => (
        <div
          key={c.index}
          className="res-core"
          data-warn={c.usage >= 90 || undefined}
          style={{ ['--fill' as string]: `${c.usage < 0 ? 0 : Math.min(100, c.usage)}%` }}
          title={t('코어 {n} — {v}', { n: c.index, v: pct(c.usage) })}
        >
          {/* The number only where it can be read. Below that height it would
              be a smudge, and the tooltip already says which core this is. */}
          {cap >= 40 && <span className="res-core-n">{c.index}</span>}
        </div>
      ))}
    </div>
  )
}

/** Used / buffers / cached as one bar. "70% used" is meaningless until the
 *  cache is separated out — the kernel hands that back the moment anything
 *  asks, so it is not pressure. */
function MemoryBar({
  total,
  used,
  buffers,
  cached,
}: {
  total: number
  used: number
  buffers: number
  cached: number
}) {
  if (total <= 0) return null
  const w = (n: number) => `${Math.max(0, Math.min(100, (n / total) * 100))}%`
  const app = Math.max(0, used - buffers - cached)
  return (
    <div className="res-mem">
      <span data-part="app" style={{ width: w(app) }} />
      <span data-part="buffers" style={{ width: w(buffers) }} />
      <span data-part="cached" style={{ width: w(cached) }} />
    </div>
  )
}

function FilesystemTable({ rows, shown }: { rows: Filesystem[]; shown: Filesystem[] }) {
  const [all, setAll] = useState(false)
  const interesting = new Set(shown.map((d) => d.mountPoint))
  const list = all ? rows : shown
  const hidden = rows.length - shown.length

  return (
    <>
      <table className="res-table">
        <thead>
          <tr>
            <th>{t('마운트')}</th>
            <th>{t('장치')}</th>
            <th className="num">{t('사용')}</th>
            <th className="num">{t('전체')}</th>
            <th className="num">{t('여유')}</th>
            <th className="res-bar-col" />
          </tr>
        </thead>
        <tbody>
          {list.map((f) => (
            <tr key={f.mountPoint} data-dim={!interesting.has(f.mountPoint) || undefined}>
              <td className="mono ellipsis">{f.mountPoint}</td>
              <td className="mono ellipsis muted">{f.device}</td>
              <td className="num">{fmtBytes(f.used)}</td>
              <td className="num">{fmtBytes(f.size)}</td>
              <td className="num">{fmtBytes(f.available)}</td>
              <td>
                <div className="res-bar" data-warn={f.percent >= 90 || undefined}>
                  <span style={{ width: `${Math.min(100, Math.max(0, f.percent))}%` }} />
                  <em>{pct(f.percent)}</em>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {hidden > 0 && (
        <button className="ghost small" onClick={() => setAll((v) => !v)}>
          {all ? t('요약만 보기') : t('나머지 {n}개도 보기 — tmpfs·overlay 등', { n: hidden })}
        </button>
      )}
    </>
  )
}
