import { useCallback, useEffect, useState } from 'react'
import {
  MCPState,
  RotateMCPToken,
  SetMCPEnabled,
  SetMCPHost,
  type HostView,
  type MCPStatus,
} from './ipc'
import { t } from './i18n'

// The AI integration's settings (§4 of the MCP design note).
//
// Two switches, deliberately separate. One opens a local endpoint; the other
// decides which servers it may read. Turning the feature on must not hand an
// AI every host that happens to be configured, and a single global switch
// would make the cautious answer "no" for everybody.
//
// Read-only today, and the panel says so where the user is deciding rather
// than in documentation they will not open. Somebody weighing this needs to
// know the worst case is that a model reads a file, not that it restarts a
// service.

export function McpPanel({
  hosts,
  onClose,
  onError,
}: {
  hosts: HostView[]
  onClose: () => void
  onError: (msg: string) => void
}) {
  const [state, setState] = useState<MCPStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState(false)
  const [reveal, setReveal] = useState(false)

  const load = useCallback(async () => {
    try {
      setState(await MCPState())
    } catch (e) {
      onError(String(e))
    }
  }, [onError])

  useEffect(() => {
    void load()
  }, [load])

  const apply = async (fn: () => Promise<MCPStatus>) => {
    setBusy(true)
    try {
      const next = await fn()
      setState(next)
      if (next.error) onError(next.error)
    } catch (e) {
      onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const copy = async () => {
    if (!state?.snippet) return
    try {
      await navigator.clipboard.writeText(state.snippet)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch (e) {
      onError(String(e))
    }
  }

  const shared = hosts.filter((h) => state?.hosts?.[h.id]).length

  return (
    <div className="scrim" onClick={onClose}>
      <div className="dialog mcp-dialog" onClick={(e) => e.stopPropagation()}>
        <h2>{t('AI 연동 (MCP)')}</h2>
        <p className="muted">
          {t(
            'Claude Code 나 Claude Desktop 같은 MCP 클라이언트가 이 앱을 통해 서버를 조회할 수 있게 합니다. AI 는 GUI 와 똑같은 어댑터·SSH 연결·Command Log 를 씁니다.',
          )}
        </p>

        {/* Stated where the decision is made. A user weighing this needs to know
            the worst case here is a file being read, not a service restarted. */}
        <p className="warn-text">
          {t(
            '지금은 조회만 됩니다. 서버를 바꾸는 도구는 없고, 파라미터로 숨겨져 있지도 않습니다.',
          )}
        </p>

        <label className="mcp-toggle">
          <input
            type="checkbox"
            disabled={busy}
            checked={!!state?.enabled}
            onChange={(e) => void apply(() => SetMCPEnabled(e.target.checked))}
          />
          <span>{t('로컬 MCP 엔드포인트 열기')}</span>
          {state?.running && <span className="badge ok">{t('실행 중')}</span>}
        </label>

        {state?.enabled && state.running && (
          <>
            <div className="mcp-endpoint">
              <label className="muted small">{t('주소')}</label>
              <code className="mono selectable">{state.url}</code>
              <span className="muted small">
                {t('127.0.0.1 에만 열립니다. 외부 인터페이스로 여는 설정은 없습니다.')}
              </span>
            </div>

            <div className="mcp-endpoint">
              <label className="muted small">{t('토큰')}</label>
              <code className="mono selectable ellipsis">
                {reveal ? state.token : '•'.repeat(24)}
              </code>
              <div className="mcp-row">
                <button className="ghost small-btn" onClick={() => setReveal(!reveal)}>
                  {reveal ? t('가리기') : t('보기')}
                </button>
                {/* Manual only. Rotating on the app's schedule would break the
                    line the user already pasted, at a moment they did not pick. */}
                <button
                  className="ghost small-btn"
                  disabled={busy}
                  onClick={() => void apply(() => RotateMCPToken())}
                  title={t('새 토큰을 만들고 이전 토큰을 무효화합니다')}
                >
                  {t('토큰 재발급')}
                </button>
              </div>
            </div>

            <div className="mcp-endpoint">
              <label className="muted small">{t('클라이언트에 붙여넣기')}</label>
              <code className="mono mcp-snippet selectable">{state.snippet}</code>
              <button className="primary small-btn" onClick={() => void copy()}>
                {copied ? t('복사됨') : t('복사')}
              </button>
            </div>
          </>
        )}

        <h3 className="mcp-heading">
          {t('읽을 수 있는 서버')}
          <span className="muted small">
            {' '}
            {t('{n}대 공유 중', { n: shared })}
          </span>
        </h3>
        <p className="muted small">
          {t(
            '기본은 전부 꺼짐입니다. 호스트를 등록했다고 AI 가 볼 수 있게 되지는 않습니다. 여기서 켠 것만 읽힙니다.',
          )}
        </p>

        {hosts.length === 0 && (
          <div className="placeholder small">{t('등록된 호스트가 없습니다.')}</div>
        )}
        <div className="mcp-hosts">
          {hosts.map((h) => (
            <label key={h.id} className="mcp-toggle">
              <input
                type="checkbox"
                disabled={busy}
                checked={!!state?.hosts?.[h.id]}
                onChange={(e) => void apply(() => SetMCPHost(h.id, e.target.checked))}
              />
              <span>{h.name || h.hostname}</span>
              <span className="muted small mono">
                {h.user}@{h.hostname}
              </span>
            </label>
          ))}
        </div>

        <div className="dialog-actions">
          <button className="primary" onClick={onClose}>
            {t('닫기')}
          </button>
        </div>
      </div>
    </div>
  )
}
