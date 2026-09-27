import { useCallback, useEffect, useState } from 'react'
import { Scrim } from './Scrim'
import {
  SyncApplyPending,
  SyncChangePassphrase,
  SyncCreate,
  SyncDisable,
  SyncDismissPending,
  SyncGenerateKey,
  SyncHistory,
  SyncJoin,
  SyncNow,
  SyncPending,
  SyncSetRemember,
  SyncSetToken,
  SyncState,
  SyncUnlock,
  on,
  type SyncHistoryEntry,
  type SyncPendingChange,
  type SyncView,
} from './ipc'
import { t } from './i18n'
import { stamp } from './datetime'

// Settings sync (§9.1 of the sync design).
//
// # Three tabs, and a wizard in front of them
//
// Until a sync exists there is nothing to show but two buttons, so the panel opens
// on the setup screen. After that the tabs are: what the sync is doing, what is
// waiting for this person, and what it has done. The middle one is the reason the
// whole feature can be trusted — a policy never gets looser by itself — so it
// carries a count in the tab.
//
// # What this screen has to say out loud
//
// Three things, because each of them is a decision the user cannot unmake later
// from inside the app:
//
//   - the passphrase cannot be recovered. Lose it and the repository is ciphertext
//     nobody can read, including them;
//   - a deploy key needs *write* access. A read-only one produces a sync that works
//     until the first push, and then fails in a way that looks like our bug;
//   - turning the sync off here leaves the repository alone. People expect a switch
//     called "stop syncing" to be safe, and it is — but only because it is written
//     that way.

type Tab = 'status' | 'pending' | 'history'
type Wizard = 'none' | 'create' | 'join'
type AuthKind = 'deploy_key' | 'agent' | 'token'

/** The field names the Go side sends, in words. */
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
      return t('저장소 기록이 되돌려진 것 같습니다')
    case 'vanished':
      return t('레코드가 tombstone 없이 사라졌습니다')
    case 'host_key_mismatch':
      return t('호스트 키가 이 기기의 것과 다릅니다')
    case 'deleted_but_modified':
      return t('다른 기기에서 삭제됐지만 이 기기에서 수정했습니다')
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
  const [state, setState] = useState<SyncView | null>(null)
  const [tab, setTab] = useState<Tab>('status')
  const [wizard, setWizard] = useState<Wizard>('none')
  const [pending, setPending] = useState<SyncPendingChange[]>([])
  const [history, setHistory] = useState<SyncHistoryEntry[]>([])
  const [busy, setBusy] = useState(false)

  // Wizard fields.
  const [url, setUrl] = useState('')
  const [authKind, setAuthKind] = useState<AuthKind>('deploy_key')
  const [pass, setPass] = useState('')
  const [pass2, setPass2] = useState('')
  const [token, setToken] = useState('')
  const [pubKey, setPubKey] = useState('')
  const [remember, setRemember] = useState(true)

  // Passphrase change.
  const [oldPass, setOldPass] = useState('')
  const [newPass, setNewPass] = useState('')
  const [changing, setChanging] = useState(false)

  const load = useCallback(async () => {
    try {
      const v = await SyncState()
      setState(v)
      setRemember(v.remember || !v.canRemember ? v.remember : true)
      setPending((await SyncPending()) ?? [])
    } catch (e) {
      onError(String(e))
    }
  }, [onError])

  useEffect(() => {
    void load()
    // Go says when a sync starts, finishes or changes anything, so the status line
    // is not a poll.
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

  if (state && !state.available) {
    return (
      <Scrim onClose={onClose}>
        <div className="dialog">
          <h2>{t('설정 동기화')}</h2>
          <p className="muted small">{t('서버 모드에서는 설정 동기화를 쓸 수 없습니다')}</p>
          <div className="dialog-actions">
            <button onClick={onClose}>{t('닫기')}</button>
          </div>
        </div>
      </Scrim>
    )
  }

  const notSetUp = !state?.enabled && !state?.remoteUrl
  const showWizard = wizard !== 'none' || notSetUp

  const passphraseTooShort = pass.length > 0 && pass.length < 12
  const passphraseMismatch = pass2.length > 0 && pass !== pass2
  const canSubmitWizard =
    url.trim() !== '' && pass.length >= 12 && pass === pass2 && !busy

  const submitWizard = async () => {
    const kind = authKind
    await run(async () => {
      if (kind === 'token') {
        if (!token) throw new Error(t('토큰을 입력하세요'))
        await SyncSetToken(token)
      }
      if (wizard === 'join') {
        await SyncJoin(url.trim(), kind, pass, remember)
      } else {
        await SyncCreate(url.trim(), kind, pass, remember)
      }
      setWizard('none')
      setPass('')
      setPass2('')
      setToken('')
    })
  }

  return (
    <Scrim onClose={onClose}>
      <div className="dialog mcp-dialog">
        <h2>{t('설정 동기화')}</h2>
        <p className="muted small">
          {t(
            '호스트 목록과 승인 정책을 내 git 저장소에 암호화해서 올리고, 다른 기기에서 같은 저장소를 읽습니다. 계정은 없습니다.',
          )}
        </p>

        {showWizard ? (
          <div className="mcp-tabbody">
            {/* The same tab strip the panel uses elsewhere, so the selected one
                is visibly selected. Two plain buttons side by side showed which
                wizard you were in nowhere at all. */}
            <nav className="mcp-tabs">
              <button
                data-on={wizard !== 'join' || undefined}
                onClick={() => setWizard('create')}
                disabled={busy}
              >
                {t('새 동기화 만들기')}
              </button>
              <button
                data-on={wizard === 'join' || undefined}
                onClick={() => setWizard('join')}
                disabled={busy}
              >
                {t('기존 동기화에 합류')}
              </button>
            </nav>

            <div className="form-grid">
              <label>{t('저장소 주소')}</label>
              <input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="ssh://git@github.com/me/litedeck-sync.git"
                spellCheck={false}
              />
            </div>
            <p className="muted small">
              {t('자기 서버를 쓰려면 그 서버에서 한 번: git init --bare ~/litedeck-sync.git')}
            </p>

            <div className="form-grid">
              <label>{t('인증 방식')}</label>
              <select value={authKind} onChange={(e) => setAuthKind(e.target.value as AuthKind)}>
                <option value="deploy_key">{t('동기화 전용 배포 키 (권장)')}</option>
                {/* Hidden where it cannot work: the Windows OpenSSH agent is a
                    named pipe this app does not speak, and offering the choice
                    would produce a connection failure we would then have to
                    explain. */}
                {state?.agentAvailable && <option value="agent">{t('기존 SSH 키 / ssh-agent')}</option>}
                <option value="token">{t('HTTPS + 토큰')}</option>
              </select>
            </div>

            {authKind === 'deploy_key' && (
              <div className="mcp-endpoint">
                <button
                  className="ghost small-btn"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      setPubKey(await SyncGenerateKey())
                    })
                  }
                >
                  {t('동기화 전용 키 만들기')}
                </button>
                {pubKey && (
                  <>
                    <code className="mono selectable">{pubKey.trim()}</code>
                    <span className="muted small">
                      {t(
                        '저장소 설정 → Deploy keys 에 이 공개키를 넣고 쓰기 권한을 주세요. 읽기 전용이면 첫 push 에서 실패합니다.',
                      )}
                    </span>
                  </>
                )}
              </div>
            )}

            {authKind === 'token' && (
              <div className="form-grid">
                <label>{t('토큰')}</label>
                <input
                  type="password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  spellCheck={false}
                />
              </div>
            )}

            <div className="form-grid">
              <label>{t('패스프레이즈 (12자 이상)')}</label>
              <input type="password" value={pass} onChange={(e) => setPass(e.target.value)} />
              <label>{t('패스프레이즈 확인')}</label>
              <input type="password" value={pass2} onChange={(e) => setPass2(e.target.value)} />
            </div>
            {passphraseTooShort && (
              <span className="badge warn">{t('12자 이상이어야 합니다')}</span>
            )}
            {passphraseMismatch && <span className="badge warn">{t('두 값이 다릅니다')}</span>}
            <p className="muted small">
              {t('이 패스프레이즈를 잃으면 동기화된 설정을 복구할 수 없습니다.')}
            </p>

            <label className="mcp-toggle">
              <input
                type="checkbox"
                checked={remember && !!state?.canRemember}
                disabled={!state?.canRemember || busy}
                onChange={(e) => setRemember(e.target.checked)}
              />
              <span>{t('이 기기에 기억 (OS 자격 증명 저장소)')}</span>
            </label>
            {!state?.canRemember && (
              <span className="muted small">
                {t(
                  '이 기기에는 자격 증명 저장소가 없습니다 — 앱을 열 때마다 패스프레이즈를 묻습니다. 파일로 저장하지는 않습니다.',
                )}
              </span>
            )}

            <div className="dialog-actions">
              <button onClick={onClose}>{t('닫기')}</button>
              <button className="primary" disabled={!canSubmitWizard} onClick={() => void submitWizard()}>
                {wizard === 'join' ? t('합류') : t('만들기')}
              </button>
            </div>
          </div>
        ) : (
          <>
            <nav className="mcp-tabs">
              <button data-on={tab === 'status' || undefined} onClick={() => setTab('status')}>
                {t('상태')}
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

            {tab === 'status' && (
              <div className="mcp-tabbody">
                <div className="mcp-endpoint">
                  <label className="muted small">{t('저장소')}</label>
                  <code className="mono selectable">{state?.remoteUrl}</code>
                  <span className="muted small">
                    {state?.lastSync
                      ? t('마지막 동기화: {when}', { when: stamp(state.lastSync * 1000) })
                      : t('아직 동기화하지 않았습니다')}
                  </span>
                  {state?.error && <span className="badge warn">{state.error}</span>}
                </div>

                {!state?.unlocked && (
                  <div className="mcp-endpoint">
                    <span className="muted small">
                      {t('이 기기에서는 저장소가 잠겨 있습니다 — 패스프레이즈를 입력하세요')}
                    </span>
                    <input
                      type="password"
                      value={pass}
                      onChange={(e) => setPass(e.target.value)}
                      placeholder={t('패스프레이즈')}
                    />
                    <button
                      className="primary small-btn"
                      disabled={busy || pass.length < 12}
                      onClick={() =>
                        void run(async () => {
                          await SyncUnlock(pass, remember && !!state?.canRemember)
                          setPass('')
                        })
                      }
                    >
                      {t('열기')}
                    </button>
                  </div>
                )}

                <div className="mcp-row">
                  <button
                    disabled={busy || state?.syncing || !state?.unlocked}
                    onClick={() => void run(() => SyncNow())}
                  >
                    {state?.syncing ? t('동기화 중…') : t('지금 동기화')}
                  </button>
                  <button
                    className="ghost small-btn"
                    disabled={busy || !state?.unlocked}
                    onClick={() => setChanging(!changing)}
                  >
                    {t('패스프레이즈 변경')}
                  </button>
                </div>

                {changing && (
                  <div className="mcp-endpoint">
                    <input
                      type="password"
                      value={oldPass}
                      onChange={(e) => setOldPass(e.target.value)}
                      placeholder={t('현재 패스프레이즈')}
                    />
                    <input
                      type="password"
                      value={newPass}
                      onChange={(e) => setNewPass(e.target.value)}
                      placeholder={t('새 패스프레이즈 (12자 이상)')}
                    />
                    <span className="muted small">
                      {t('레코드를 다시 암호화하지 않습니다 — 볼트 키만 새 패스프레이즈로 다시 감쌉니다.')}
                    </span>
                    <button
                      className="primary small-btn"
                      disabled={busy || newPass.length < 12}
                      onClick={() =>
                        void run(async () => {
                          await SyncChangePassphrase(oldPass, newPass)
                          setOldPass('')
                          setNewPass('')
                          setChanging(false)
                        })
                      }
                    >
                      {t('변경')}
                    </button>
                  </div>
                )}

                <label className="mcp-toggle">
                  <input
                    type="checkbox"
                    checked={!!state?.remember}
                    disabled={!state?.canRemember || busy}
                    onChange={(e) => void run(() => SyncSetRemember(e.target.checked))}
                  />
                  <span>{t('이 기기에 기억 (OS 자격 증명 저장소)')}</span>
                </label>

                <div className="mcp-row">
                  <button
                    className="ghost small-btn"
                    disabled={busy}
                    onClick={() => void run(() => SyncDisable())}
                  >
                    {t('이 기기에서 동기화 끄기')}
                  </button>
                  <span className="muted small">
                    {t('호스트 목록은 그대로 남고, 저장소는 건드리지 않습니다.')}
                  </span>
                </div>
              </div>
            )}

            {tab === 'pending' && (
              <div className="mcp-tabbody">
                {pending.length === 0 && (
                  <p className="muted small">{t('확인을 기다리는 변경이 없습니다.')}</p>
                )}
                {pending.map((p) => (
                  <div className="mcp-endpoint" key={`${p.recordId}/${p.field}/${p.rev}`}>
                    <label className="muted small">{fieldLabel(p.field)}</label>
                    <code className="mono selectable">
                      {p.local} → {p.incoming}
                    </code>
                    <span className="muted small">
                      {t('기기 {device} · {when}', {
                        device: p.by.slice(0, 8),
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
                        onClick={() =>
                          void run(() => SyncDismissPending(p.recordId, p.field, p.rev))
                        }
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
                      {t('받음 {received} · 보냄 {sent} · 확인 필요 {pending}', {
                        received: e.received ?? 0,
                        sent: e.sent ?? 0,
                        pending: e.pending ?? 0,
                      })}
                    </span>
                    {e.error && <span className="badge warn">{e.error}</span>}
                    {(e.warnings ?? []).map((w, j) => (
                      <span className="badge warn" key={j}>
                        {warningLabel(w.kind)}
                      </span>
                    ))}
                    {(e.conflicts ?? []).map((c, j) => (
                      <span className="muted small" key={`c${j}`}>
                        {t('충돌: {kept} 를 남기고 {dropped} 를 덮어썼습니다', {
                          kept: c.kept,
                          dropped: c.dropped,
                        })}
                      </span>
                    ))}
                  </div>
                ))}
              </div>
            )}

            <div className="dialog-actions">
              <button onClick={onClose}>{t('닫기')}</button>
            </div>
          </>
        )}
      </div>
    </Scrim>
  )
}
