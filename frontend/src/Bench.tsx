import { useEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { ProcessTable } from './ProcessTable'
import {
  BenchDiff,
  BenchResize,
  BenchSnapshot,
  BenchSweepDone,
  ColdStartMs,
  ReportSample,
  type ProcessRow,
} from './ipc'
import { initPlatform, shortcutLabel } from './platform'

// M0 risk ④ (§12): can a few thousand rows cross the Go→IPC→React boundary
// on every poll without the window ceasing to feel local?
//
// The app sweeps every row-count × transport combination on launch and hands
// each measurement back to Go, which appends it to a file. IPC and React
// commit cost can only be observed inside the webview, but a number nobody can
// read is not a measurement — the file is what makes this reproducible.
//
// "snapshot" is what §3.2(e) currently implies: ship the whole table each poll.
// "diff" ships only what moved, which is what the design must become if the
// snapshot path proves too expensive.

type Mode = 'snapshot' | 'diff'

const ROW_COUNTS = [500, 2000, 5000, 10000]
const MODES: Mode[] = ['snapshot', 'diff']
const WARMUP = 3
const SAMPLES = 12
const SPACING_MS = 120

/** The polling interval the results are judged against (§3.2d). */
const TARGET_INTERVAL_MS = 2000

interface Sample {
  rows: number
  mode: Mode
  ipc: number
  apply: number
  render: number
  total: number
  bytes: number
}

interface Summary {
  rows: number
  mode: Mode
  p50: number
  p95: number
  ipc: number
  render: number
  kb: number
  budget: number
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

function percentile(values: number[], p: number): number {
  if (!values.length) return 0
  const s = [...values].sort((a, b) => a - b)
  return s[Math.min(s.length - 1, Math.floor(s.length * p))]
}

export default function Bench() {
  const [rows, setRows] = useState<ProcessRow[]>([])
  const [status, setStatus] = useState('시작 중…')
  const [summaries, setSummaries] = useState<Summary[]>([])
  const [coldStart, setColdStart] = useState(0)
  const [platformLabel, setPlatformLabel] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return
    started.current = true

    const table = new Map<number, ProcessRow>()

    /** One polling cycle, measured end to end. */
    async function poll(mode: Mode, rowCount: number): Promise<Sample> {
      const t0 = performance.now()
      let next: ProcessRow[]
      let bytes: number
      let t1: number

      if (mode === 'snapshot') {
        const snap = await BenchSnapshot()
        t1 = performance.now()
        bytes = JSON.stringify(snap).length
        next = snap.rows
      } else {
        const diff = await BenchDiff()
        t1 = performance.now()
        bytes = JSON.stringify(diff).length
        for (const pid of diff.removed ?? []) table.delete(pid)
        for (const r of diff.upserted ?? []) table.set(r.pid, r)
        next = [...table.values()].sort((a, b) => a.pid - b.pid)
      }
      const t2 = performance.now()

      // flushSync forces the commit here, so the number covers real DOM work
      // rather than stopping at "React has been told about it".
      flushSync(() => setRows(next))
      const t3 = performance.now()

      return {
        rows: rowCount,
        mode,
        ipc: t1 - t0,
        apply: t2 - t1,
        render: t3 - t2,
        total: t3 - t0,
        bytes,
      }
    }

    ;(async () => {
      try {
        const p = await initPlatform()
        setPlatformLabel(`${p.os}/${p.arch} · mod=${p.modLabel}`)
        const cold = await ColdStartMs()
        setColdStart(cold)

        let sweepIndex = 0
        const collected: Summary[] = []

        for (const rowCount of ROW_COUNTS) {
          for (const mode of MODES) {
            setStatus(`측정 중 — ${rowCount.toLocaleString()}행 · ${mode}`)
            table.clear()
            await BenchResize(rowCount)

            // Diff mode needs a full table locally before deltas mean anything.
            if (mode === 'diff') {
              const snap = await BenchSnapshot()
              for (const r of snap.rows) table.set(r.pid, r)
            }
            for (let i = 0; i < WARMUP; i++) {
              await poll(mode, rowCount)
              await sleep(SPACING_MS)
            }

            const samples: Sample[] = []
            for (let i = 0; i < SAMPLES; i++) {
              const s = await poll(mode, rowCount)
              samples.push(s)
              void ReportSample({
                rows: s.rows,
                mode: s.mode,
                ipcMs: s.ipc,
                applyMs: s.apply,
                renderMs: s.render,
                totalMs: s.total,
                bytes: s.bytes,
                coldStart: cold,
                sweepIndex,
              })
              await sleep(SPACING_MS)
            }
            sweepIndex++

            const totals = samples.map((s) => s.total)
            const p95 = percentile(totals, 0.95)
            const summary: Summary = {
              rows: rowCount,
              mode,
              p50: percentile(totals, 0.5),
              p95,
              ipc: percentile(
                samples.map((s) => s.ipc),
                0.5,
              ),
              render: percentile(
                samples.map((s) => s.render),
                0.5,
              ),
              kb: samples[0].bytes / 1024,
              budget: (p95 / TARGET_INTERVAL_MS) * 100,
            }
            collected.push(summary)
            setSummaries([...collected])
          }
        }

        const path = await BenchSweepDone()
        setStatus(`완료 — ${path}`)
        setDone(true)
      } catch (e) {
        setError(String(e))
        setStatus('실패')
      }
    })()
  }, [])

  return (
    <div className="shell">
      <header className="titlebar">
        <div className="brand">
          LiteDeck <span className="muted">— 렌더 벤치마크 (M0 리스크 ④·⑤)</span>
        </div>
        <div className="muted small">
          {platformLabel}
          {coldStart > 0 && ` · 콜드스타트 ${coldStart.toFixed(0)}ms`}
          {` · 이름변경 ${shortcutLabel('rename')} · 삭제 ${shortcutLabel('delete')}`}
        </div>
      </header>

      {error && <div className="error">{error}</div>}

      <div className="controls">
        <strong>{status}</strong>
        {!done && <span className="muted small">2초 주기 기준으로 판정</span>}
      </div>

      <div className="stats">
        {summaries.map((s) => (
          <div className="stat" key={`${s.rows}-${s.mode}`} data-warn={s.budget > 25 || undefined}>
            <div className="stat-label">
              {s.rows.toLocaleString()} · {s.mode === 'snapshot' ? '스냅샷' : 'diff'}
            </div>
            <div className="stat-value">
              {s.p95.toFixed(0)}
              <span className="stat-unit">ms p95</span>
            </div>
            <div className="stat-label">
              {s.kb.toFixed(0)}KB · 주기 {s.budget.toFixed(1)}%
            </div>
          </div>
        ))}
      </div>

      <ProcessTable rows={rows} />

      <footer className="hint">
        주기 점유율이 25%를 넘으면 폴링이 창을 잡아먹기 시작한다 — §3.2(e)를 전체
        스냅샷에서 diff 전송으로 바꿔야 한다는 신호.
      </footer>
    </div>
  )
}
