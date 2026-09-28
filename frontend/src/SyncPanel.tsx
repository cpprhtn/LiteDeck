import { useCallback, useEffect, useState } from 'react'
import { Scrim } from './Scrim'
import { SyncFilePanel } from './SyncFilePanel'
import {
  SyncApplyPending,
  SyncDismissPending,
  SyncHistory,
  SyncPending,
  on,
  type SyncHistoryEntry,
  type SyncPendingChange,
} from './ipc'
import { t } from './i18n'
import { stamp } from './datetime'

// Settings to a file, and back.
//
// # Three tabs, and the middle one is the point
//
// Export and import are the feature. The waiting list is what makes importing
// somebody else's file safe to do: a policy in the file that is looser than this
// machine's is not applied, it waits here for a person. Without somewhere to see
// that, "withheld" would mean "silently dropped".
//
// The history is last because it is read after the fact, not acted on.

type Tab = 'file' | 'pending' | 'history'

/** The field names Go sends, in words. */
function fieldLabel(field: string): string {
  switch (field) {
    case 'policy.shared':
      return t('AI 클라이언트에 공유')
    case 'policy.mcp_approval':
      return t('승인 모드')
    case 'policy.exec_enabled':
      return t('명령 실행')
    case 'policy.delete_enabled':
      return t('파일 삭제')
    case 'host_keys':
      return t('호스트 키')
    default:
      return field
  }
}

function warningLabel(kind: string): string {
  switch (kind) {
    case 'rollback':
      return t('이 기기가 이미 본 것보다 오래된 파일입니다')
    case 'host_key_mismatch':
      return t('호스트 키가 이 기기의 것과 다릅니다')
    case 'undecryptable':
      return t('레코드를 열 수 없습니다')
    case 'address_conflict':
      return t('같은 주소에 서로 다른 호스트 키가 있습니다')
    default:
      return kind
  }
}

export function SyncPanel({
  onClose,
  onError,
}: {
  onClose: () => void
  onError: (msg: string) => void
}) {
  const [tab, setTab] = useState<Tab>('file')
  const [pending, setPending] = useState<SyncPendingChange[]>([])
  const [history, setHistory] = useState<SyncHistoryEntry[]>([])
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setPending((await SyncPending()) ?? [])
    } catch (e) {
      // Server mode answers with a refusal here, which is the normal answer
      // there and not something to put on the screen.
      onError(String(e))
    }
  }, [onError])

  useEffect(() => {
    void load()
    const off = on('sync:state', () => void load())
    return off
  }, [load])

  const loadHistory = useCallback(async () => {
    try {
      setHistory((await SyncHistory(50)) ?? [])
    } catch {
      // A history that will not load must not take the panel with it.
    }
  }, [])

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await fn()
      await load()
    } catch (e) {
      onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Scrim onClose={onClose}>
      <div className="dialog mcp-dialog">
        <h2>{t('설정 파일')}</h2>
        <p className="muted small">
          {t(
            '호스트 목록과 승인 정책을 암호화된 파일 하나로 내보내고, 다른 기기에서 그 파일을 읽습니다. 계정도 서버도 없습니다.',
          )}
        </p>

        <nav className="mcp-tabs">
          <button data-on={tab === 'file' || undefined} onClick={() => setTab('file')}>
            {t('내보내기·가져오기')}
          </button>
          <button data-on={tab === 'pending' || undefined} onClick={() => setTab('pending')}>
            {t('확인 대기')}
            {pending.length > 0 && <span className="badge">{pending.length}</span>}
          </button>
          <button
            data-on={tab === 'history' || undefined}
            onClick={() => {
              setTab('history')
              void loadHistory()
            }}
          >
            {t('기록')}
          </button>
        </nav>

        {tab === 'file' && <SyncFilePanel onError={onError} />}

        {tab === 'pending' && (
          <div className="mcp-tabbody">
            {pending.length === 0 && (
              <p className="muted small">{t('확인을 기다리는 변경이 없습니다.')}</p>
            )}
            {pending.length > 0 && (
              <p className="muted small">
                {t('가져온 파일이 이 기기보다 느슨한 권한을 담고 있었습니다. 적용은 여기서 사람이 합니다.')}
              </p>
            )}
            {pending.map((p) => (
              <div className="mcp-endpoint" key={`${p.recordId}/${p.field}/${p.rev}`}>
                <label className="muted small">{fieldLabel(p.field)}</label>
                <code className="mono selectable">
                  {p.local} → {p.incoming}
                </code>
                <span className="muted small">
                  {t('기기 {device} · {when}', {
                    device: p.by.slice(0, 8) || '—',
                    when: stamp(p.at),
                  })}
                </span>
                <div className="mcp-row">
                  <button
                    className="primary small-btn"
                    disabled={busy}
                    onClick={() => void run(() => SyncApplyPending(p.recordId, p.field))}
                  >
                    {t('적용')}
                  </button>
                  <button
                    className="ghost small-btn"
                    disabled={busy}
                    onClick={() => void run(() => SyncDismissPending(p.recordId, p.field, p.rev))}
                  >
                    {t('무시 (이 기기 값 유지)')}
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}

        {tab === 'history' && (
          <div className="mcp-tabbody">
            {history.length === 0 && <p className="muted small">{t('기록이 없습니다.')}</p>}
            {history.map((e, i) => (
              <div className="mcp-endpoint" key={i}>
                <label className="muted small">{stamp(e.at)}</label>
                <span className="muted small">
                  {t('받음 {received} · 확인 필요 {pending}', {
                    received: e.received ?? 0,
                    pending: e.pending ?? 0,
                  })}
                  {e.file ? ` · ${e.file}` : ''}
                </span>
                {e.error && <span className="badge warn">{e.error}</span>}
                {(e.warnings ?? []).map((w, j) => (
                  <span className="badge warn" key={j}>
                    {warningLabel(w.kind)}
                  </span>
                ))}
              </div>
            ))}
          </div>
        )}

        <div className="dialog-actions">
          <button onClick={onClose}>{t('닫기')}</button>
        </div>
      </div>
    </Scrim>
  )
}
