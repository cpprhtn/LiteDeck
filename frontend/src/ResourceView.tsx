import { useState } from 'react'
import type { CPUSplit, Core, DiskIO, Filesystem, GPU, NetIface, Pressure } from './ipc'
import { useMetrics, useMetricsHistory, type Sample } from './metricsStore'
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

/** Bytes per second. -1 is "not known yet", never a real zero. */
function fmtRate(v: number): string {
  return v < 0 ? '—' : `${fmtBytes(v)}/s`
}

/** A percentage that may be unknown. -1 is "no second sample yet" for CPU and
 *  "this card does not report it" for a fan — never a real zero. */
function pct(v: number): string {
  return v < 0 ? '—' : `${Math.round(v)}%`
}

export function ResourceView({ hostID, facts }: { hostID: string; facts: SysFacts }) {
  // No poll of its own: the summary bar above is already reading these numbers
  // for this host, and asking again would be a second round trip for the same
  // answer.
  const m = useMetrics(hostID)
  const history = useMetricsHistory(hostID)

  if (!m) return <div className="placeholder">{t('읽는 중…')}</div>

  const cores = m.cores ?? []
  const swapPct = m.swapTotal > 0 ? (m.swapUsed / m.swapTotal) * 100 : -1

  const netRx = sumRate(m.net?.map((n) => n.rxRate))
  const netTx = sumRate(m.net?.map((n) => n.txRate))
  const diskR = sumRate(m.diskIO?.map((d) => d.readRate))
  const diskW = sumRate(m.diskIO?.map((d) => d.writeRate))
  const netBad = (m.net ?? []).reduce((a, n) => a + n.rxErrs + n.txErrs + n.rxDrop + n.txDrop, 0)

  return (
    <div className="res-pane">
      {/* The overview row: every resource as one number with its shape beside
          it. Text alone made this a list to read; the line makes it a row to
          scan, and scanning is what somebody opening a monitoring tab is doing.
          Whatever looks wrong here is what they click into below. */}
      <div className="res-top">
        <TopStat
          label="CPU"
          value={pct(m.cpu)}
          sub={cores.length ? t('코어 {n}', { n: cores.length }) : undefined}
          samples={history}
          pick={(x) => x.cpu}
          warn={m.cpu >= 90}
        />
        <TopStat
          label={t('메모리')}
          value={pct(m.memPercent)}
          sub={`${fmtBytes(m.memUsed)} / ${fmtBytes(m.memTotal)}`}
          samples={history}
          pick={(x) => x.mem}
          warn={m.memPercent >= 90}
        />
        {m.gpus.map((g) => (
          <TopStat
            key={g.index}
            label={m.gpus.length > 1 ? t('GPU {n}', { n: g.index }) : 'GPU'}
            value={pct(g.utilization)}
            sub={shortGPUName(g.name)}
            samples={history}
            pick={(x) => x.gpu[g.index] ?? -1}
            warn={g.utilization >= 95 || g.tempC >= 85}
          />
        ))}
        {(m.diskIO ?? []).length > 0 && (
          <TopStat
            label={t('디스크 I/O')}
            value={fmtRate(diskR)}
            sub={t('쓰기 {v}', { v: fmtRate(diskW) })}
            samples={history}
            pick={(x) => x.diskR}
            scale="auto"
          />
        )}
        {(m.net ?? []).length > 0 && (
          <TopStat
            label={t('네트워크')}
            value={fmtRate(netRx)}
            sub={t('보냄 {v}', { v: fmtRate(netTx) })}
            samples={history}
            pick={(x) => x.netRx}
            scale="auto"
            warn={netBad > 0}
          />
        )}
        {m.hasLoad && (
          <TopStat
            label={t('로드')}
            value={m.load1.toFixed(2)}
            sub={t('1분 평균')}
            warn={cores.length > 0 && m.load1 > cores.length}
          />
        )}
        <TopStat label={t('가동 시간')} value={fmtUptime(m.uptimeSeconds)} sub={since(m.uptimeSeconds)} />
      </div>

      <div className="res-grid">
        <Panel
          label="CPU"
          value={pct(m.cpu)}
          sub={facts.cpuModel ? [facts.cpuModel] : undefined}
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
          {m.hasPSI && <PressureLine kind="cpu" p={m.psiCPU} />}
          {/* vmstat's r and b. Between them they say which of the two a slow
              machine is short of: tasks queueing for a CPU, or tasks parked in
              uninterruptible IO. They went missing when the chip strip became
              the overview row — which is how a number nobody displays survives
              being collected. */}
          <Facts
            rows={[
              [t('실행 대기'), String(m.runnable ?? 0)],
              [t('IO 블록'), String(m.blocked ?? 0)],
            ]}
          />
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
          {m.hasPSI && <PressureLine kind="memory" p={m.psiMemory} />}
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

        {(m.net ?? []).length > 0 && (
          <Panel
            label={t('네트워크')}
            value={fmtRate(netRx)}
            sub={[t('보냄 {v}', { v: fmtRate(netTx) })]}
            warn={netBad > 0}
          >
            <TimeChart
              samples={history}
              pick={(s) => s.netRx}
              pick2={(s) => s.netTx}
              height={44}
              scale="auto"
              format={fmtRate}
              label={t('네트워크 처리량 추이')}
            />
            <IfaceList ifaces={m.net} />
          </Panel>
        )}

        {(m.diskIO ?? []).length > 0 && (
          <Panel
            label={t('디스크 I/O')}
            value={fmtRate(diskR)}
            sub={[t('쓰기 {v}', { v: fmtRate(diskW) })]}
          >
            {/* iowait says the machine is waiting on storage. It does not say
                which storage, and on a host with a fast root disk and a slow
                array that is the whole question. */}
            <TimeChart
              samples={history}
              pick={(s) => s.diskR}
              pick2={(s) => s.diskW}
              height={44}
              scale="auto"
              format={fmtRate}
              label={t('디스크 처리량 추이')}
            />
            {m.hasPSI && <PressureLine kind="io" p={m.psiIO} />}
            <Facts
              rows={(m.diskIO ?? [])
                .slice(0, 4)
                .map((d) => [d.name, `${fmtRate(d.readRate)} / ${fmtRate(d.writeRate)}`] as [string, string])}
            />
          </Panel>
        )}

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
        <h3>{t('시스템')}</h3>
        <div className="res-panel">
          <SystemInfo
            info={facts}
            uptime={m.uptimeSeconds}
            fdUsed={m.fdUsed}
            fdMax={m.fdMax}
            switchRate={m.switchRate}
          />
        </div>
      </section>

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

/** One resource in the overview row: a number and, where the figure moves, its
 *  shape. Uptime has no shape and simply does not get a line. */
function TopStat({
  label,
  value,
  sub,
  samples,
  pick,
  scale,
  warn,
}: {
  label: string
  value: string
  sub?: string
  samples?: Sample[]
  pick?: (s: Sample) => number
  scale?: 'percent' | 'auto'
  warn?: boolean
}) {
  return (
    <div className="res-top-card" data-warn={warn || undefined}>
      <div className="res-top-label">{label}</div>
      <div className="res-top-value">{value}</div>
      {sub && (
        <div className="res-top-sub ellipsis" title={sub}>
          {sub}
        </div>
      )}
      {samples && pick && (
        <TimeChart samples={samples} pick={pick} height={26} scale={scale} bare label={label} />
      )}
    </div>
  )
}

/** What the machine is, as opposed to what it is doing. None of it moves while
 *  the connection is up, so it is read once by detection rather than polled —
 *  and it is the half of the answer somebody needs before the numbers above
 *  mean anything. */
function SystemInfo({
  info,
  uptime,
  fdUsed,
  fdMax,
  switchRate,
}: {
  info: SysFacts
  uptime: number
  fdUsed: number
  fdMax: number
  switchRate: number
}) {
  const rows: [string, string][] = []
  if (info.prettyName) rows.push(['OS', info.prettyName])
  if (info.kernel) rows.push([t('커널'), info.kernel])
  if (info.hostname) rows.push([t('호스트명'), info.hostname])
  rows.push([t('가동 시간'), fmtUptime(uptime)])
  rows.push([t('부팅 시각'), since(uptime)])
  if (info.timezone) rows.push([t('시간대'), info.timezone])
  // Not facts about the machine but counters about the whole of it, and there
  // is nowhere better: running out of descriptors takes a server down in a way
  // that looks like nothing else is wrong.
  if (fdUsed > 0) {
    rows.push([t('열린 FD'), fdMax > 0 ? `${fdUsed} / ${fdMax}` : String(fdUsed)])
  }
  if (switchRate >= 0) {
    rows.push([t('컨텍스트 전환'), t('{n}/초', { n: Math.round(switchRate) })])
  }
  return <Facts rows={rows} />
}

export interface SysFacts {
  prettyName?: string
  kernel?: string
  hostname?: string
  /** Shown by the CPU panel, which is where somebody studying the processor is
   *  already looking — so the facts block does not repeat it. */
  cpuModel?: string
  timezone?: string
}

/** The wall-clock moment the machine came up, from how long it has been up.
 *  Shown in the reader's own timezone: they are reading it here. */
function since(uptimeSeconds: number): string {
  const d = new Date(Date.now() - uptimeSeconds * 1000)
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/** PSI as one line.
 *
 *  Utilisation says how much of a thing is being used; pressure says how much
 *  time was lost waiting for it. A box at 100% CPU can be perfectly happy —
 *  that is a machine doing its job — while one at 40% with rising pressure has
 *  tasks queueing. The kernel keeps the averages itself, so a connection that
 *  opened a second ago already carries the last five minutes. */
function PressureLine({ kind, p }: { kind: 'cpu' | 'memory' | 'io'; p: Pressure }) {
  const worst = Math.max(p.some10, p.some60, p.some300)
  // The panel already says which resource. Repeating it put an "IO 압력" a few
  // centimetres from the CPU split's "IO 대기" — two different numbers wearing
  // almost the same name, with nothing on screen saying so.
  const hint = {
    cpu: t('작업이 CPU 를 얻지 못해 멈춰 있던 시간의 비율입니다. 사용률과는 다릅니다 — 100% 로 일하는 기계는 멀쩡한 것이고, 이 값이 오르면 일이 밀리고 있는 것입니다.'),
    memory: t('작업이 메모리를 기다리느라 멈춰 있던 시간의 비율입니다. 회수와 스왑이 여기 들어갑니다. 여유가 남아 보여도 이 값이 오르면 실제로는 모자란 것입니다.'),
    io: t('작업이 저장장치를 기다리느라 멈춰 있던 시간의 비율입니다. CPU 분해의 「IO 대기」 와는 다른 값입니다 — 그쪽은 CPU 시간에서 차지하는 몫이고, 이쪽은 작업이 실제로 멈춰 있던 시간의 몫입니다.')
  }[kind]
  return (
    <div
      className="res-psi"
      data-warn={worst >= 10 || undefined}
      title={hint}
    >
      <span className="res-psi-label">{t('압력')}</span>
      <span className="res-psi-v">{p.some10.toFixed(1)}</span>
      <span className="muted">·</span>
      <span className="res-psi-v">{p.some60.toFixed(1)}</span>
      <span className="muted">·</span>
      <span className="res-psi-v">{p.some300.toFixed(1)}</span>
      <span className="muted res-psi-note">{t('10초·1분·5분 %')}</span>
    </div>
  )
}

/** Interfaces, with the counters that only matter when they climb: a card that
 *  dropped four hundred packets last March is not a problem, one dropping four
 *  a second is. */
function IfaceList({ ifaces }: { ifaces: NetIface[] }) {
  return (
    <dl className="res-facts">
      {ifaces.slice(0, 4).map((n) => {
        const bad = n.rxErrs + n.txErrs + n.rxDrop + n.txDrop
        return (
          <div key={n.name}>
            <dt className="mono">{n.name}</dt>
            <dd>
              {fmtRate(n.rxRate)} / {fmtRate(n.txRate)}
              {bad > 0 && (
                <span
                  className="res-bad"
                  title={t('오류 {e} · 버림 {d}', {
                    e: n.rxErrs + n.txErrs,
                    d: n.rxDrop + n.txDrop,
                  })}
                >
                  {' '}
                  ⚠ {bad.toLocaleString()}
                </span>
              )}
            </dd>
          </div>
        )
      })}
    </dl>
  )
}

/** A total that one unknown makes unknown — adding a -1 in would quietly
 *  subtract a byte per second. */
function sumRate(xs?: number[]): number {
  if (!xs || xs.length === 0) return -1
  return xs.some((v) => v < 0) ? -1 : xs.reduce((a, b) => a + b, 0)
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
    { key: 'iowait', label: t('IO 대기'), v: split.iowait, hint: t('CPU 가 할 일 없이 디스크를 기다린 시간의 몫입니다. 높으면 CPU 가 아니라 디스크가 문제입니다. 디스크 패널의 「압력」 과는 다른 값입니다 — 그쪽은 작업이 실제로 멈춰 있던 시간의 몫입니다.') },
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
        items={[
          ...parts.map((p) => ({
            key: p.key,
            label: p.label,
            text: `${p.v.toFixed(0)}%`,
            hint: p.hint,
          })),
          // The remainder. Went missing when this legend was built from the
          // four buckets alone, and without it the four rarely add to anything
          // a reader can check against.
          {
            key: 'idle',
            label: t('유휴'),
            text: `${Math.max(0, 100 - parts.reduce((a, p) => a + p.v, 0)).toFixed(0)}%`,
            hint: t('아무 일도 하지 않은 시간의 몫입니다.'),
          },
        ]}
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

  // Let CSS choose the columns from the width it actually has. The fixed
  // "nearest clean rectangle" put twenty cores in a 5×4 block regardless of
  // whether the panel was 240px or 450px wide, so a wide panel got a tall
  // square with empty space either side. auto-fill lays them out flat when
  // there is room and stacks them when there is not.
  const cell = n <= 16 ? 34 : n <= 64 ? 24 : 16

  return (
    <div
      className="res-die"
      style={{
        gridTemplateColumns: `repeat(auto-fill, minmax(${cell}px, 1fr))`,
        gap: cell >= 24 ? 3 : 2,
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
          {cell >= 24 && <span className="res-core-n">{c.index}</span>}
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
      <div className="res-scroll">
        <table className="res-table">
        <thead>
          <tr>
            <th>{t('마운트')}</th>
            <th>{t('장치')}</th>
            <th className="num">{t('사용')}</th>
            <th className="num">{t('전체')}</th>
            <th className="num">{t('여유')}</th>
            <th className="num">inode</th>
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
              {/* A filesystem with room and no inodes left cannot create a
                  file, and every tool then says "no space left on device" —
                  the same words as running out of bytes. Blank where the
                  filesystem has no inode table, which is normal for btrfs. */}
              <td className="num" data-warn={(f.inodesPercent ?? 0) >= 90 || undefined}>
                {!f.inodesTotal ? '—' : `${Math.round(f.inodesPercent ?? 0)}%`}
              </td>
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
      </div>
      {hidden > 0 && (
        <button className="ghost small" onClick={() => setAll((v) => !v)}>
          {all ? t('요약만 보기') : t('나머지 {n}개도 보기 — tmpfs·overlay 등', { n: hidden })}
        </button>
      )}
    </>
  )
}
