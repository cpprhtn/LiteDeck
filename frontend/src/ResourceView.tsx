import { useState } from 'react'
import type { CPUSplit, Core, Filesystem, GPU } from './ipc'
import { useMetrics, useMetricsHistory } from './metricsStore'
import { TimeChart } from './TimeChart'
import { shortGPUName } from './gpuName'
import { t } from './i18n'

// The resource detail (§4.7, arch/07).
//
// The summary bar is the glance: it has to fit above every other tab, so it
// shows the filtered disk list, folds several GPUs into a badge, and hides the
// three load figures behind a tooltip. This is the same reading with the room
// to say all of it.
//
// The two are deliberately the same call. A detail view that polled separately
// would double the round trips to show numbers the bar already has.

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

export function ResourceView({ hostID }: { hostID: string }) {
  // No poll of its own: the summary bar above is already reading these numbers
  // for this host, and asking again would be a second round trip for the same
  // answer.
  const m = useMetrics(hostID)
  const history = useMetricsHistory(hostID)

  if (!m) return <div className="placeholder">{t('읽는 중…')}</div>

  return (
    <div className="res-pane">
      <section className="res-section">
        <h3>{t('CPU')}</h3>
        {/* The bar says 40%. This says what the 40% is — which is the only
            version of the number anybody can act on. */}
        {/* Windows reports one aggregate percentage and no breakdown, so the
            split says it does not know rather than drawing four zeroes. */}
        <SplitBar split={m.split} idle={m.cpu} />
        <TimeChart
          samples={history}
          pick={(s) => s.cpu}
          warnAt={90}
          label={t('CPU 사용률 추이')}
        />
        {(m.cores ?? []).length > 1 && <Cores cores={m.cores} />}
      </section>

      <section className="res-section">
        <h3>{t('메모리')}</h3>
        {(m.memCached > 0 || m.memBuffers > 0) && (
          <MemoryBar
            total={m.memTotal}
            used={m.memUsed}
            buffers={m.memBuffers}
            cached={m.memCached}
          />
        )}
        <TimeChart
          samples={history}
          pick={(s) => s.mem}
          warnAt={90}
          label={t('메모리 사용률 추이')}
        />
        <div className="res-grid">
          <Stat label={t('쓰는 중')} value={fmtBytes(m.memUsed)} note={pct(m.memPercent)} />
          {/* The breakdown comes from /proc/meminfo. A platform without one
              reports zeroes, and a tile reading "캐시 0B" is a claim about the
              machine rather than an absent figure — so it is not drawn. */}
          {m.memCached > 0 && <Stat label={t('캐시')} value={fmtBytes(m.memCached)} />}
          {m.memBuffers > 0 && <Stat label={t('버퍼')} value={fmtBytes(m.memBuffers)} />}
          {m.memShared > 0 && <Stat label={t('공유')} value={fmtBytes(m.memShared)} />}
          {/* Dirty is pages written but not yet on disk. A number that keeps
              climbing is a disk that cannot keep up with what is being written
              to it. */}
          {m.memDirty > 0 && (
            <Stat label={t('미기록')} value={fmtBytes(m.memDirty)} note={t('아직 디스크에 안 감')} />
          )}
        </div>
      </section>

      <section className="res-section">
        <h3>{t('그 밖')}</h3>
        <div className="res-grid">
          <Stat
            label={t('스왑')}
            value={m.swapTotal > 0 ? pct((m.swapUsed / m.swapTotal) * 100) : '—'}
            note={
              m.swapTotal > 0
                ? `${fmtBytes(m.swapUsed)} / ${fmtBytes(m.swapTotal)}`
                : t('없음')
            }
          />
          {/* Spelled out rather than hidden in a tooltip: one figure says
              nothing, and the shape of the three is the whole reading — rising,
              falling, or a spike that has already passed. */}
          {m.hasLoad && (
            <Stat
              label={t('로드')}
              value={m.load1.toFixed(2)}
              note={t('5분 {b} · 15분 {c}', { b: m.load5.toFixed(2), c: m.load15.toFixed(2) })}
            />
          )}
          <Stat label={t('가동 시간')} value={fmtUptime(m.uptimeSeconds)} />
        </div>
      </section>

      <section className="res-section">
        <h3>{t('파일시스템')}</h3>
        {/* Every mount, not the shortlist the bar shows. The one that filled up
            is often exactly the one the shortlist dropped. */}
        <FilesystemTable rows={m.filesystems} shown={m.disks} />
      </section>

      {m.gpus.length > 0 && (
        <section className="res-section">
          <h3>{t('GPU')}</h3>
          <div className="res-grid">
            {m.gpus.map((g) => (
              <GPUCard key={g.index} gpu={g} showIndex={m.gpus.length > 1} />
            ))}
          </div>
          {m.gpus.map((g) => (
            <TimeChart
              key={g.index}
              samples={history}
              pick={(s) => s.gpu[g.index] ?? -1}
              warnAt={95}
              label={t('GPU {n} 사용률 추이', { n: g.index })}
            />
          ))}
        </section>
      )}
    </div>
  )
}

/** The CPU breakdown as one bar. Stacked rather than four numbers, because the
 *  shape is the reading: a bar that is mostly iowait looks nothing like one
 *  that is mostly user, and no amount of staring at percentages makes that
 *  jump out. */
function SplitBar({ split, idle }: { split: CPUSplit; idle: number }) {
  if (split.user < 0) {
    return (
      <div className="muted small">
        {t('두 번째 표본을 기다리는 중 — 누적 카운터라 한 번만으로는 알 수 없습니다')}
      </div>
    )
  }
  const parts: { key: string; label: string; v: number; hint: string }[] = [
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
      <div className="res-legend">
        {parts.map((p) => (
          <span key={p.key} title={p.hint}>
            <i data-part={p.key} />
            {p.label} {p.v.toFixed(1)}%
          </span>
        ))}
        <span className="muted">
          {t('한가함')} {Math.max(0, 100 - (idle < 0 ? 0 : idle)).toFixed(1)}%
        </span>
      </div>
    </>
  )
}

/** One block per core. The aggregate cannot tell "every core half busy" from
 *  "one core pinned", and the second is what a single-threaded bottleneck looks
 *  like — the most common shape of "the server is slow". */
function Cores({ cores }: { cores: Core[] }) {
  return (
    <div className="res-cores">
      {cores.map((c) => (
        <div
          key={c.index}
          className="res-core"
          data-warn={c.usage >= 90 || undefined}
          title={t('코어 {n} — {v}', { n: c.index, v: pct(c.usage) })}
        >
          <span style={{ height: `${c.usage < 0 ? 0 : Math.min(100, c.usage)}%` }} />
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
  // used already excludes what the kernel would reclaim, so cache and buffers
  // are drawn beside it rather than inside it.
  const app = Math.max(0, used - buffers - cached)
  return (
    <div className="res-mem">
      <span data-part="app" style={{ width: w(app) }} title={t('프로그램이 쥐고 있는 메모리')} />
      <span data-part="buffers" style={{ width: w(buffers) }} title={t('버퍼')} />
      <span data-part="cached" style={{ width: w(cached) }} title={t('캐시')} />
    </div>
  )
}

function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="res-stat">
      <div className="res-stat-label">{label}</div>
      <div className="res-stat-value">{value}</div>
      {note && <div className="res-stat-note">{note}</div>}
    </div>
  )
}

function GPUCard({ gpu, showIndex }: { gpu: GPU; showIndex: boolean }) {
  return (
    <div className="res-stat res-gpu">
      {/* Wraps rather than truncating. A card name ending in "..." withholds
          the one part that identifies it — the model number is at the end. */}
      <div className="res-stat-label res-gpu-name" title={gpu.name}>
        {showIndex && <span className="res-gpu-idx">{gpu.index}</span>}
        {shortGPUName(gpu.name)}
      </div>
      <div className="res-stat-value">{pct(gpu.utilization)}</div>
      <div className="res-stat-note">
        {/* A fan reading of "—" is a passively cooled card, not a stopped fan.
            Showing 0 there would be alarming and wrong. */}
        {t('팬 {fan} · {temp}', {
          fan: pct(gpu.fan),
          temp: gpu.tempC < 0 ? '—' : `${Math.round(gpu.tempC)}°C`,
        })}
      </div>
      <div className="res-stat-note">
        {t('VRAM {used} / {total}', {
          used: fmtBytes(gpu.memUsed),
          total: fmtBytes(gpu.memTotal),
        })}
      </div>
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
          {all
            ? t('요약만 보기')
            : t('나머지 {n}개도 보기 — tmpfs·overlay 등', { n: hidden })}
        </button>
      )}
    </>
  )
}
