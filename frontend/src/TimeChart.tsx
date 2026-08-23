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
//
// Shaded, and not counted. Readings are only collected for the host on screen
// (§3.2d), so gaps are what normally happens and not an exception worth
// announcing — a note saying "1 gap" under every chart is the Command Log's
// old habit of putting a red row against a routine condition, which teaches
// people to stop reading the notes that matter.

export function TimeChart({
  samples,
  pick,
  height = 72,
  warnAt,
  label,
  /** Percent charts share a fixed 0–100 scale so that 2% and 90% cannot draw
   *  the same picture. Rates have no ceiling to share, so they scale to their
   *  own peak — and the peak is printed on the axis, because a line without a
   *  number beside it says only "something happened". */
  scale = 'percent',
  format,
  /** A second series drawn under the first — transmit against receive, write
   *  against read. Two panels for one question reads worse than one. */
  pick2,
  bare,
}: {
  samples: Sample[]
  /** The value to plot, or -1 where this sample has none. */
  pick: (s: Sample) => number
  height?: number
  /** Above this the line turns to the warning colour. */
  warnAt?: number
  label: string
  scale?: 'percent' | 'auto'
  format?: (v: number) => string
  pick2?: (s: Sample) => number
  /** No axes, no ticks, no empty-state text: the sparkline inside a stat card,
   *  where the number beside it already says what the value is and the line is
   *  only there to say which way it has been going. */
  bare?: boolean
}) {
  const usable = samples.filter((s) => pick(s) >= 0)
  if (usable.length < 2) {
    if (bare) return <div className="chart-bare" style={{ height }} />
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

  // A flat zero would fill the panel with a line at the top if the ceiling were
  // taken from the data alone, so an all-quiet chart keeps a floor.
  const peak =
    scale === 'percent'
      ? 100
      : Math.max(
          1,
          ...usable.map(pick),
          ...(pick2 ? usable.map(pick2).filter((v) => v >= 0) : []),
        )
  const y = (v: number) => height - (Math.min(peak, Math.max(0, v)) / peak) * height

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

  const hot = warnAt !== undefined && Math.max(...usable.map(pick)) >= warnAt

  // Half and full. Three lines across a 44px panel is a grid; two is a scale.
  const ticks = [peak / 2, peak]
  const fmtTick = (v: number) => (format ? format(v) : `${Math.round(v)}%`)

  // Times along the bottom rather than only at the ends: with one label at each
  // edge the middle of the chart has no time attached to it at all, and "when
  // did it spike" is most of what anybody asks a chart.
  //
  // Three, not five. A panel is a quarter of the row, and five stamps of
  // "04:50 PM" ran into each other — five labels that overlap say less than
  // two that do not.
  const steps = 2
  const stamps: { at: number; x: number }[] = []
  for (let i = 0; i <= steps; i++) {
    stamps.push({ at: t0 + (span * i) / steps, x: (i / steps) * 100 })
  }

  // The second series gets the same gap treatment, but silently: one "N gaps"
  // note under the chart covers both.
  const runs2: string[][] = [[]]
  if (pick2) {
    const u2 = samples.filter((s) => pick2(s) >= 0)
    u2.forEach((s, i) => {
      if (i > 0 && s.t - u2[i - 1].t > GAP_MS) runs2.push([])
      runs2[runs2.length - 1].push(`${x(s).toFixed(1)},${y(pick2(s)).toFixed(1)}`)
    })
  }

  return (
    <div className="chart" data-bare={bare || undefined}>
      {!bare && (
        <>{/* The scale, printed. A line with no number beside it says only that
          something moved — and on a fixed 0–100 chart a flat line near the
          bottom and one near the top mean opposite things. */}
      <div className="chart-ticks" style={{ height }}>
        {ticks.map((v) => (
          <span key={v} style={{ bottom: `${(v / peak) * 100}%` }}>
            {fmtTick(v)}
          </span>
        ))}
      </div></>
      )}
      <svg
        viewBox={`0 0 ${w} ${height}`}
        // The rendered height has to be the same number the viewBox and the
        // tick column use, or the labels sit at fractions of one height while
        // the lines sit at fractions of another.
        style={{ height }}
        preserveAspectRatio="none"
        role="img"
        aria-label={label}
        data-warn={hot || undefined}
      >
        {!bare &&
          ticks.map((v) => (
            <line key={v} className="chart-grid" x1="0" x2={w} y1={y(v)} y2={y(v)} />
          ))}
        {gaps.map((g, i) => (
          <rect
            key={i}
            className="chart-gap"
            x={g.from}
            width={Math.max(1, g.to - g.from)}
            y="0"
            height={height}
          >
            {/* The explanation belongs on the shaded band, not on a line of
                text under the axis. Whoever wonders what the grey is hovers
                the grey. */}
            <title>{t('이 구간은 앱이 보고 있지 않아 기록이 없습니다')}</title>
          </rect>
        ))}
        {runs2.map((pts, i) =>
          pts.length >= 2 ? (
            <polyline key={`b${i}`} className="chart-line2" points={pts.join(' ')} />
          ) : null,
        )}
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
      {!bare && (
      <div className="chart-axis muted">
        {stamps.map((s2, i) => (
          <span
            key={i}
            style={{ left: `${s2.x}%` }}
            data-edge={i === 0 ? 'first' : i === steps ? 'last' : undefined}
          >
            {clock(s2.at, span)}
          </span>
        ))}
      </div>
      )}
    </div>
  )
}

/**
 * The time under a tick.
 *
 * 24-hour, because "04:50 PM" is eight characters against "16:50"'s five and
 * the panel is a quarter of a row — and because a chart axis reading 24-hour is
 * the convention every monitoring tool already set.
 *
 * Seconds appear on a short window. The history starts empty, so the first
 * minutes of any connection span less than a minute, and three stamps all
 * reading "16:50" tell the reader nothing about which end is which.
 */
function clock(ms: number, spanMs: number): string {
  return new Date(ms).toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    ...(spanMs < 10 * 60_000 ? { second: '2-digit' } : {}),
    hour12: false,
  })
}
