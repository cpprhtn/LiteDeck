import { useState } from 'react'
import type { Filesystem, GPU } from './ipc'
import { useMetrics } from './metricsStore'
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

  if (!m) return <div className="placeholder">{t('읽는 중…')}</div>

  return (
    <div className="res-pane">
      <section className="res-section">
        <h3>{t('지금')}</h3>
        <div className="res-grid">
          <Stat label="CPU" value={pct(m.cpu)} />
          <Stat
            label={t('메모리')}
            value={pct(m.memPercent)}
            note={`${fmtBytes(m.memUsed)} / ${fmtBytes(m.memTotal)}`}
          />
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
              <GPUCard key={g.index} gpu={g} />
            ))}
          </div>
        </section>
      )}
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

function GPUCard({ gpu }: { gpu: GPU }) {
  return (
    <div className="res-stat res-gpu">
      <div className="res-stat-label ellipsis" title={gpu.name}>
        {gpu.index}: {gpu.name}
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
