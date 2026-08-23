import { GAP_MS, type Sample } from './metricsStore'
import { t } from './i18n'

// A line over real time (§4.7, arch/07).
//
// Two rules, and both are about not lying.
//
// **x is time, not position.** Idle backoff stretches the poll gap from two
// seconds to thirty (A-45), so an evenly spaced array draws half an hour of
// quiet as though it were two minutes of it: the line keeps its shape while the
// axis underneath it silently changes scale.
//
// **A hole stays a hole.** When the window was closed or the host was away,
// nothing was recorded, and joining the two ends draws a reading nobody took.
// The line breaks instead, and the gap is shaded so it reads as absence rather
// than as a flat stretch.

export function TimeChart({
  samples,
  pick,
  height = 72,
  warnAt,
  label,
}: {
  samples: Sample[]
  /** The value to plot, or -1 where this sample has none. */
  pick: (s: Sample) => number
  height?: number
  /** Above this the line turns to the warning colour. */
  warnAt?: number
  label: string
}) {
  const usable = samples.filter((s) => pick(s) >= 0)
  if (usable.length < 2) {
    return (
      <div className="chart-empty placeholder small">
        {t('아직 그릴 만큼 모이지 않았습니다.')}
      </div>
    )
  }

  const w = 1000 // viewBox units; the SVG scales to its box
  const t0 = usable[0].t
  const t1 = usable[usable.length - 1].t
  const span = Math.max(1, t1 - t0)
  const x = (s: Sample) => ((s.t - t0) / span) * w
  const y = (v: number) => height - (Math.min(100, Math.max(0, v)) / 100) * height

  // One polyline per unbroken run. Splitting on the gap rather than drawing a
  // single line with holes keeps the break honest even when the renderer would
  // happily interpolate.
  const runs: string[][] = [[]]
  const gaps: { from: number; to: number }[] = []
  usable.forEach((s, i) => {
    if (i > 0 && s.t - usable[i - 1].t > GAP_MS) {
      gaps.push({ from: x(usable[i - 1]), to: x(s) })
      runs.push([])
    }
    runs[runs.length - 1].push(`${x(s).toFixed(1)},${y(pick(s)).toFixed(1)}`)
  })

  const peak = Math.max(...usable.map(pick))
  const hot = warnAt !== undefined && peak >= warnAt

  return (
    <div className="chart">
      <svg
        viewBox={`0 0 ${w} ${height}`}
        preserveAspectRatio="none"
        role="img"
        aria-label={label}
        data-warn={hot || undefined}
      >
        {/* 0–100 fixed. Auto-scaling would make 2% look identical to 90%,
            which is the distinction the chart exists to show. */}
        {[25, 50, 75].map((g) => (
          <line key={g} className="chart-grid" x1="0" x2={w} y1={y(g)} y2={y(g)} />
        ))}
        {gaps.map((g, i) => (
          <rect
            key={i}
            className="chart-gap"
            x={g.from}
            width={Math.max(1, g.to - g.from)}
            y="0"
            height={height}
          />
        ))}
        {runs.map((pts, i) =>
          pts.length < 2 ? (
            // A run of one is a single reading marooned between two gaps. Drawn
            // as a dot, because a polyline of one point renders as nothing and
            // the sample would vanish without being missing.
            pts.length === 1 ? (
              <circle
                key={i}
                className="chart-dot"
                cx={pts[0].split(',')[0]}
                cy={pts[0].split(',')[1]}
                r="2"
              />
            ) : null
          ) : (
            <polyline key={i} className="chart-line" points={pts.join(' ')} />
          ),
        )}
      </svg>
      <div className="chart-axis muted">
        <span>{clock(t0)}</span>
        {gaps.length > 0 && (
          <span title={t('이 구간은 앱이 보고 있지 않아 기록이 없습니다')}>
            {t('빈 구간 {n}곳', { n: gaps.length })}
          </span>
        )}
        <span>{clock(t1)}</span>
      </div>
    </div>
  )
}

function clock(ms: number): string {
  return new Date(ms).toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
  })
}
