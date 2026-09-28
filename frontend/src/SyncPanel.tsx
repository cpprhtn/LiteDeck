import { useCallback, useEffect, useState } from 'react'
import { Scrim } from './Scrim'
import { SyncFilePanel } from './SyncFilePanel'
import {
  SyncApplyPending,
  SyncDismissPending,
  SyncPending,
  on,
  type SyncPendingChange,
} from './ipc'
import { t } from './i18n'
import { stamp } from './datetime'

// Moving settings between machines.
//
// # Two tabs
//
// The file is the feature. The waiting list is what makes reading somebody else's
// file safe to do: permissions in it that are looser than this machine's are not
// applied, they wait here for a person. Without somewhere to see that, "withheld"
// would mean "silently dropped".
//
// There was a third tab, a log of past imports. It went: it answered "what
// happened while I was not looking", which is a question a repository syncing
// itself every five minutes raises and a person opening a file does not — they
// are looking, and the result is on the screen in front of them.

type Tab = 'file' | 'pending'

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

export function SyncPanel({
  onClose,
  onError,
}: {
  onClose: () => void
  onError: (msg: string) => void
}) {
  const [tab, setTab] = useState<Tab>('file')
  const [pending, setPending] = useState<SyncPendingChange[]>([])
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setPending((await SyncPending()) ?? [])
    } catch (e) {
      onError(String(e))
    }
  }, [onError])

  useEffect(() => {
    void load()
    const off = on('sync:state', () => void load())
    return off
  }, [load])

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
        <h2>{t('설정 동기화')}</h2>
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
        </nav>

        {tab === 'file' && <SyncFilePanel onError={onError} />}

        {tab === 'pending' && (
          <div className="mcp-tabbody">
            {/* The empty state has to explain the tab, because that is when
                somebody reads it: a list with nothing in it and no sentence is
                a tab whose name is the only clue to what it was for. */}
            {pending.length === 0 ? (
              <p className="muted small">
                {t(
                  '가져온 파일에 이 기기보다 느슨한 권한이 들어 있으면 그 항목만 적용을 미루고 여기 모읍니다. 지금은 없습니다.',
                )}
              </p>
            ) : (
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

        <div className="dialog-actions">
          <button onClick={onClose}>{t('닫기')}</button>
        </div>
      </div>
    </Scrim>
  )
}
