import { useCallback, useEffect, useState } from 'react'
import { MCPState, SetMCPWritePolicy, type MCPStatus } from './ipc'
import { t } from './i18n'

// The per-host AI write mode, in the header beside the server it applies to.
//
// It lives here rather than only in the settings panel for two reasons. A mode
// that lets an agent change a server unattended must be *visible while it is
// on* — a switch nobody can see is one nobody remembers throwing. And it has to
// be reachable in one click, because the alternative to a convenient switch is
// not a careful user, it is a user who stops using the feature.
//
// Nothing here is shown unless the user has already shared this host with AI
// clients. For everyone else it is not a control they need to understand.

const MINUTE = 60

function remaining(until?: number): string {
  if (!until) return ''
  const left = until - Math.floor(Date.now() / 1000)
  if (left <= 0) return ''
  const h = Math.floor(left / 3600)
  const m = Math.round((left % 3600) / 60)
  return h > 0 ? t('{h}시간 {m}분', { h, m }) : t('{m}분', { m })
}

export function McpHostBadge({ hostID }: { hostID: string }) {
  const [state, setState] = useState<MCPStatus | null>(null)
  const [open, setOpen] = useState(false)

  const load = useCallback(async () => {
    try {
      setState(await MCPState())
    } catch {
      // The badge is an indicator; failing to draw it must not take the header
      // down with it.
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load, hostID])

  // A countdown that does not count down is worse than no countdown: it would
  // still read "7 hours left" an hour after the window closed.
  useEffect(() => {
    if (!state?.write?.[hostID]?.until) return
    const id = setInterval(() => void load(), 30_000)
    return () => clearInterval(id)
  }, [state, hostID, load])

  if (!state?.enabled || !state.hosts?.[hostID]) return null

  const mode = state.write?.[hostID]?.mode ?? 'ask'
  const left = remaining(state.write?.[hostID]?.until)

  const set = (next: string, minutes: number) => {
    setOpen(false)
    void SetMCPWritePolicy(hostID, next, minutes)
      .then(setState)
      .catch(() => {})
  }

  return (
    <span className="mcp-badge-wrap">
      <button
        className="mcp-badge"
        data-mode={mode}
        onClick={() => setOpen(!open)}
        title={t('이 서버에 대한 AI 변경 권한')}
      >
        {mode === 'bypass' && (
          <>
            {t('AI 통과')}
            {left && <span className="mcp-badge-left"> {left}</span>}
          </>
        )}
        {mode === 'strict' && t('AI 전부 승인')}
        {mode === 'ask' && t('AI 파일만 승인')}
      </button>

      {open && (
        <div className="mcp-badge-menu">
          <button onClick={() => set('strict', 0)}>
            {t('전부 물어보기')}
            <span className="muted small">{t('prod 처럼 틀리면 안 되는 서버')}</span>
          </button>
          <button onClick={() => set('ask', 0)}>
            {t('파일 변경만 물어보기')}
            <span className="muted small">{t('기본값. 재시작 등은 그냥 실행')}</span>
          </button>
          <button onClick={() => set('bypass', MINUTE)}>
            {t('1시간 안 묻기')}
          </button>
          <button onClick={() => set('bypass', 8 * MINUTE)}>
            {t('밤새 안 묻기 (8시간)')}
            <span className="muted small">{t('자리를 비울 때')}</span>
          </button>
        </div>
      )}
    </span>
  )
}
