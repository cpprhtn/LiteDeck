import { useRef } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import type { ProcessRow } from './ipc'

// The process view's table (§4.4). Only the visible rows exist in the DOM —
// without that, a few thousand processes would make every poll a layout storm
// and the window would stop feeling local, which is the whole promise (§1.1).

const ROW_HEIGHT = 26

const COLUMNS = '76px 96px 64px 64px 88px 56px 84px 160px 1fr'

function fmtBytes(kb: number): string {
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

export function ProcessTable({ rows }: { rows: ProcessRow[] }) {
  const scrollRef = useRef<HTMLDivElement>(null)

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
  })

  return (
    <div className="table">
      <div className="thead" style={{ gridTemplateColumns: COLUMNS }}>
        <div className="num">PID</div>
        <div>USER</div>
        <div className="num">CPU%</div>
        <div className="num">MEM%</div>
        <div className="num">RSS</div>
        <div>STATE</div>
        <div className="num">TIME</div>
        <div>COMMAND</div>
        <div>ARGS</div>
      </div>

      <div className="tbody" ref={scrollRef}>
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((item) => {
            const r = rows[item.index]
            return (
              <div
                key={r.pid}
                className="trow"
                style={{
                  gridTemplateColumns: COLUMNS,
                  height: ROW_HEIGHT,
                  transform: `translateY(${item.start}px)`,
                }}
              >
                <div className="num mono">{r.pid}</div>
                <div className="ellipsis">{r.user}</div>
                <div className="num mono" data-hot={r.cpu > 50 || undefined}>
                  {r.cpu.toFixed(1)}
                </div>
                <div className="num mono">{r.mem.toFixed(1)}</div>
                <div className="num mono">{fmtBytes(r.rss)}</div>
                <div className="mono" data-zombie={r.state === 'Z' || undefined}>
                  {r.state}
                </div>
                <div className="num mono">{fmtElapsed(r.elapsed)}</div>
                <div className="ellipsis">{r.command}</div>
                <div className="ellipsis muted mono">{r.args}</div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
