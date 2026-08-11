import { useEffect, useState } from 'react'
import {
  CancelTransfer,
  ClearFinishedTransfers,
  GetTransfers,
  on,
  type Transfer,
} from './ipc'
import { k, t as t2 } from './i18n'

// The transfer queue panel (§4.2). Hides itself when there is nothing to show,
// so it costs no space in the common case.

function fmtBytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)}GB`
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)}MB`
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(0)}KB`
  return `${n}B`
}

const STATUS_LABEL: Record<Transfer['status'], string> = {
  queued: k('대기'),
  running: k('전송 중'),
  done: k('완료'),
  failed: k('실패'),
  cancelled: k('취소됨'),
}

export function TransferPanel({ onError }: { onError: (msg: string) => void }) {
  const [transfers, setTransfers] = useState<Transfer[]>([])

  useEffect(() => {
    void GetTransfers().then(setTransfers).catch(() => {})
    return on<Transfer>('transfer:progress', (t) =>
      setTransfers((prev) => {
        const i = prev.findIndex((x) => x.id === t.id)
        if (i < 0) return [...prev, t]
        const next = [...prev]
        next[i] = t
        return next
      }),
    )
  }, [])

  if (transfers.length === 0) return null

  const active = transfers.filter((t) => t.status === 'queued' || t.status === 'running')

  return (
    <div className="transfers">
      <div className="transfers-head">
        <strong>{t2('전송')}</strong>
        <span className="muted small">
          {active.length > 0
            ? t2('진행 중 {n}', { n: active.length })
            : t2('{n}건', { n: transfers.length })}
        </span>
        <span className="spacer" />
        <button
          className="ghost small-btn"
          onClick={() => {
            void ClearFinishedTransfers()
            setTransfers((prev) =>
              prev.filter((t) => t.status === 'queued' || t.status === 'running'),
            )
          }}
        >
          {t2('완료 항목 지우기')}
        </button>
      </div>

      <div className="transfers-body">
        {transfers.map((t) => {
          const pct = t.size > 0 ? Math.min(100, (t.done / t.size) * 100) : 0
          return (
            <div key={t.id} className="transfer-row" data-status={t.status}>
              <span className="muted">{t.direction === 'upload' ? '↑' : '↓'}</span>
              <span className="ellipsis mono" title={t.currentRel || undefined}>
                {t.dir && '▸ '}
                {t.direction === 'upload' ? t.remote : t.local}
                {t.dir && t.files ? (
                  <span className="muted">
                    {' '}
                    {t.filesDone ?? 0}/{t.files}
                    {t.currentRel ? ` · ${t.currentRel}` : ''}
                  </span>
                ) : null}
                {/* A tree whose walk has not finished has no total yet, so the
                    bar would sit at zero looking stuck. */}
                {t.dir && !t.files && t.status === 'running' ? (
                  <span className="muted"> {t2('목록을 세는 중…')}</span>
                ) : null}
              </span>
              <div className="progress" title={`${pct.toFixed(0)}%`}>
                <div className="progress-bar" style={{ width: `${pct}%` }} />
              </div>
              <span className="mono muted transfer-size">
                {fmtBytes(t.done)} / {fmtBytes(t.size)}
              </span>
              <span className="muted small">{t2(STATUS_LABEL[t.status])}</span>
              {(t.status === 'queued' || t.status === 'running') && (
                <button
                  className="ghost small-btn"
                  onClick={() => void CancelTransfer(t.id).catch((e) => onError(String(e)))}
                >
                  {t2('취소')}
                </button>
              )}
              {t.error && <div className="transfer-error mono">{t.error}</div>}
            </div>
          )
        })}
      </div>
    </div>
  )
}
