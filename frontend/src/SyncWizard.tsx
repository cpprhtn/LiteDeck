import { useState } from 'react'
import {
  SyncCreate,
  SyncGenerateKey,
  SyncJoin,
  SyncProbe,
  SyncSetToken,
  type SyncProbeResult,
  type SyncView,
} from './ipc'
import { t } from './i18n'

// Setting up the sync, one question at a time (§9.1).
//
// # Why this is a wizard and not a form
//
// The first version was one screen with every field on it: address, method, key,
// passphrase. Everything a person needed was visible, and it was unusable —
// because the fields are not independent. You cannot paste an address until you
// have made a repository; you cannot test the key until you have registered it on
// a page you have to go and find; and the passphrase is the one thing that cannot
// be changed by trying again. A form presents all of that as four blanks and
// leaves the order to you.
//
// So: one step, one thing to do, and the step does not advance until that thing
// has actually happened — checked against the remote, not against the fact that a
// field is non-empty.
//
// # Nothing is written until the last step
//
// The connection test reads. It clones into a temp directory that is thrown away,
// and it is what turns "could not sync" — later, on another screen, about a
// checkbox on GitHub — into "the key is not registered yet", here, next to the
// button that registers it.

type Step = 'where' | 'repo' | 'access' | 'passphrase' | 'confirm'
type Where = 'forge' | 'server' | 'custom'
type AuthKind = 'deploy_key' | 'agent' | 'token'

const STEPS: Step[] = ['where', 'repo', 'access', 'passphrase', 'confirm']

export function SyncWizard({
  mode,
  agentAvailable,
  canRemember,
  hostCount,
  onDone,
  onCancel,
  onError,
}: {
  /** 'create' makes a new sync; 'join' brings this machine into one. */
  mode: 'create' | 'join'
  agentAvailable: boolean
  canRemember: boolean
  /** How many of this machine's hosts would be uploaded. */
  hostCount: number
  onDone: (v: SyncView) => void
  onCancel: () => void
  onError: (msg: string) => void
}) {
  const [step, setStep] = useState<Step>('where')
  const [where, setWhere] = useState<Where>('forge')
  const [url, setUrl] = useState('')
  const [authKind, setAuthKind] = useState<AuthKind>('deploy_key')
  const [token, setToken] = useState('')
  const [pubKey, setPubKey] = useState('')
  const [probe, setProbe] = useState<SyncProbeResult | null>(null)
  const [pass, setPass] = useState('')
  const [pass2, setPass2] = useState('')
  const [remember, setRemember] = useState(true)
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState('')

  const index = STEPS.indexOf(step)
  const go = (s: Step) => {
    setStep(s)
    // A probe belongs to the address and method it was run for. Keeping it after
    // either changes is how a screen ends up saying "connected" about something
    // else.
    if (s === 'repo' || s === 'access') setProbe(null)
  }

  const copy = async (text: string, what: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(what)
      setTimeout(() => setCopied(''), 1500)
    } catch (e) {
      onError(String(e))
    }
  }

  const runProbe = async () => {
    setBusy(true)
    try {
      if (authKind === 'token' && token) await SyncSetToken(token)
      setProbe(await SyncProbe(url.trim(), authKind))
    } catch (e) {
      onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const generate = async () => {
    setBusy(true)
    try {
      setPubKey(await SyncGenerateKey())
    } catch (e) {
      onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const finish = async () => {
    setBusy(true)
    try {
      const v =
        mode === 'join'
          ? await SyncJoin(url.trim(), authKind, pass, remember && canRemember)
          : await SyncCreate(url.trim(), authKind, pass, remember && canRemember)
      onDone(v)
    } catch (e) {
      onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  // What the connection test found, as a sentence and a verdict. The verdict
  // decides whether the step may be left, so "it looked fine" and "it is fine"
  // cannot drift apart.
  const probeVerdict = (): { ok: boolean; text: string; warn?: boolean } | null => {
    if (!probe) return null
    switch (probe.kind) {
      case 'empty':
        return mode === 'join'
          ? {
              ok: false,
              warn: true,
              text: t('저장소가 비어 있습니다 — 여기에는 아직 동기화가 없습니다. 「새 동기화 만들기」로 시작하세요'),
            }
          : { ok: true, text: t('연결됐습니다. 저장소가 비어 있어 여기에 만들 수 있습니다') }
      case 'sync':
        return mode === 'join'
          ? {
              ok: true,
              text: t('연결됐습니다. 호스트 {n}개가 들어 있는 LiteDeck 동기화 저장소입니다', {
                n: probe.hosts,
              }),
            }
          : {
              ok: false,
              warn: true,
              text: t('여기에는 이미 동기화가 있습니다 — 「기존 동기화에 합류」를 쓰세요'),
            }
      case 'other':
        return {
          ok: false,
          warn: true,
          text: t('이 저장소에는 다른 파일이 들어 있습니다. LiteDeck 은 남의 저장소에 쓰지 않습니다 — 빈 저장소를 쓰세요'),
        }
      case 'denied':
        return {
          ok: false,
          warn: true,
          text: t('저장소에 닿았지만 거절당했습니다 — 공개키를 아직 등록하지 않았거나, 쓰기 권한이 없습니다'),
        }
      default:
        return {
          ok: false,
          warn: true,
          text: t('저장소에 닿지 못했습니다 — 주소를 확인하세요 ({detail})', {
            detail: probe.detail ?? '',
          }),
        }
    }
  }

  const verdict = probeVerdict()
  const canLeaveRepo = url.trim() !== ''
  const canLeaveAccess = !!verdict?.ok
  const canFinish = pass.length >= 12 && pass === pass2 && !busy

  return (
    <div className="mcp-tabbody sync-wizard">
      <div className="sync-steps">
        {STEPS.map((s, i) => (
          <span key={s} data-on={i === index || undefined} data-done={i < index || undefined}>
            {i + 1}
          </span>
        ))}
        <span className="muted small">
          {mode === 'join' ? t('기존 동기화에 합류') : t('새 동기화 만들기')}
        </span>
      </div>

      {step === 'where' && (
        <>
          <h3>{t('설정을 어디에 둘까요?')}</h3>
          <p className="muted small">
            {t('LiteDeck 은 빈 git 저장소 하나만 있으면 됩니다. 내용은 암호화되므로 저장소를 가진 쪽도 읽지 못합니다.')}
          </p>
          <div className="sync-choices">
            <button data-on={where === 'forge' || undefined} onClick={() => setWhere('forge')}>
              <strong>{t('GitHub · GitLab 비공개 저장소')}</strong>
              <span className="muted small">{t('가장 간단합니다. 계정만 있으면 됩니다')}</span>
            </button>
            <button data-on={where === 'server' || undefined} onClick={() => setWhere('server')}>
              <strong>{t('내 서버')}</strong>
              <span className="muted small">{t('이미 SSH 로 쓰는 서버에 저장소를 하나 만듭니다')}</span>
            </button>
            <button data-on={where === 'custom' || undefined} onClick={() => setWhere('custom')}>
              <strong>{t('직접 입력')}</strong>
              <span className="muted small">{t('이미 주소를 알고 있습니다')}</span>
            </button>
          </div>
          <div className="dialog-actions">
            <button onClick={onCancel}>{t('취소')}</button>
            <button className="primary" onClick={() => go('repo')}>
              {t('다음')}
            </button>
          </div>
        </>
      )}

      {step === 'repo' && (
        <>
          <h3>{mode === 'join' ? t('저장소 주소') : t('빈 저장소 만들기')}</h3>

          {mode === 'create' && where === 'forge' && (
            <ol className="sync-howto">
              <li>
                {t('브라우저에서 새 저장소를 만듭니다')}
                <code className="mono selectable">https://github.com/new</code>
              </li>
              <li>{t('이름은 아무거나 — litedeck-sync 를 권합니다')}</li>
              <li>
                <strong>{t('Private 를 고르세요.')}</strong>{' '}
                {t('내용은 암호화되지만, 호스트가 몇 개인지와 언제 바뀌었는지는 보입니다')}
              </li>
              <li>
                <strong>{t('README·.gitignore·라이선스는 체크하지 마세요.')}</strong>{' '}
                {t('저장소가 비어 있어야 합니다')}
              </li>
              <li>{t('만들어진 저장소 주소를 복사해 아래에 붙여넣으세요')}</li>
            </ol>
          )}

          {mode === 'create' && where === 'server' && (
            <ol className="sync-howto">
              <li>
                {t('저장소로 쓸 서버에서 한 번 실행합니다')}
                <span className="sync-copy">
                  <code className="mono selectable">git init --bare ~/litedeck-sync.git</code>
                  <button
                    className="ghost small-btn"
                    onClick={() => void copy('git init --bare ~/litedeck-sync.git', 'cmd')}
                  >
                    {copied === 'cmd' ? t('복사됨') : t('복사')}
                  </button>
                </span>
              </li>
              <li>
                {t('그 서버에 git 이 깔려 있어야 합니다')}{' '}
                <code className="mono selectable">git-upload-pack · git-receive-pack</code>
              </li>
              <li>
                {t('주소는 이 모양입니다')}
                <code className="mono selectable">ssh://사용자@서버주소/~/litedeck-sync.git</code>
              </li>
            </ol>
          )}

          {mode === 'join' && (
            <p className="muted small">
              {t('다른 기기에서 쓰고 있는 저장소의 주소를 그대로 붙여넣으세요. 형식은 상관없습니다.')}
            </p>
          )}

          <div className="form-grid">
            <label>{t('저장소 주소')}</label>
            <input
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="git@github.com:me/litedeck-sync.git"
              spellCheck={false}
              autoFocus
            />
          </div>
          <p className="muted small">
            {t('git@… · https://… · 서버 주소 어느 쪽이든 됩니다. 다음 단계에서 실제로 연결해 봅니다.')}
          </p>

          <div className="dialog-actions">
            <button onClick={() => go('where')}>{t('뒤로')}</button>
            <button className="primary" disabled={!canLeaveRepo} onClick={() => go('access')}>
              {t('다음')}
            </button>
          </div>
        </>
      )}

      {step === 'access' && (
        <>
          <h3>{t('저장소에 접근할 방법')}</h3>
          <div className="form-grid">
            <label>{t('방식')}</label>
            <select
              value={authKind}
              onChange={(e) => {
                setAuthKind(e.target.value as AuthKind)
                setProbe(null)
              }}
            >
              <option value="deploy_key">{t('동기화 전용 배포 키 (권장)')}</option>
              {agentAvailable && <option value="agent">{t('기존 SSH 키 / ssh-agent')}</option>}
              <option value="token">{t('HTTPS + 토큰')}</option>
            </select>
          </div>

          {authKind === 'deploy_key' && (
            <ol className="sync-howto">
              <li>
                {t('이 기기 전용 키를 만듭니다. 개인키는 OS 자격 증명 저장소에 들어가고 파일로 저장되지 않습니다')}
                <span className="sync-copy">
                  <button className="ghost small-btn" disabled={busy} onClick={() => void generate()}>
                    {pubKey ? t('다시 만들기') : t('키 만들기')}
                  </button>
                  {pubKey && (
                    <button className="ghost small-btn" onClick={() => void copy(pubKey.trim(), 'key')}>
                      {copied === 'key' ? t('복사됨') : t('공개키 복사')}
                    </button>
                  )}
                </span>
              </li>
              {pubKey && (
                <li>
                  <code className="mono selectable sync-key">{pubKey.trim()}</code>
                </li>
              )}
              <li>
                {t('저장소 설정의 Deploy keys 에 붙여넣습니다')}
                {probe?.deployKeysUrl ? (
                  <span className="sync-copy">
                    <code className="mono selectable">{probe.deployKeysUrl}</code>
                    <button
                      className="ghost small-btn"
                      onClick={() => void copy(probe.deployKeysUrl!, 'page')}
                    >
                      {copied === 'page' ? t('복사됨') : t('주소 복사')}
                    </button>
                  </span>
                ) : (
                  <code className="mono selectable">
                    {t('저장소 → Settings → Deploy keys → Add deploy key')}
                  </code>
                )}
              </li>
              <li>
                <strong>{t('Allow write access 를 반드시 켜세요.')}</strong>{' '}
                {t('읽기 전용이면 처음 올릴 때 실패합니다')}
              </li>
            </ol>
          )}

          {authKind === 'agent' && (
            <p className="muted small">
              {t('이 기기의 ssh-agent 에 들어 있는 키로 접속합니다. 그 키가 저장소에 접근할 수 있어야 합니다.')}
            </p>
          )}

          {authKind === 'token' && (
            <>
              <ol className="sync-howto">
                <li>{t('GitHub → Settings → Developer settings → Personal access tokens (fine-grained)')}</li>
                <li>{t('이 저장소 하나만 고르고, Contents 를 읽기·쓰기로 주세요')}</li>
              </ol>
              <div className="form-grid">
                <label>{t('토큰')}</label>
                <input
                  type="password"
                  value={token}
                  onChange={(e) => {
                    setToken(e.target.value)
                    setProbe(null)
                  }}
                  spellCheck={false}
                />
              </div>
            </>
          )}

          <div className="sync-copy">
            <button className="primary small-btn" disabled={busy} onClick={() => void runProbe()}>
              {busy ? t('확인 중…') : t('연결 테스트')}
            </button>
            {verdict && (
              <span className={verdict.ok ? 'badge ok' : 'badge warn'}>{verdict.text}</span>
            )}
          </div>
          {probe && probe.normalized !== url.trim() && (
            <p className="muted small">
              {t('LiteDeck 이 쓸 주소: {url}', { url: probe.normalized })}
            </p>
          )}

          <div className="dialog-actions">
            <button onClick={() => go('repo')}>{t('뒤로')}</button>
            <button className="primary" disabled={!canLeaveAccess} onClick={() => go('passphrase')}>
              {t('다음')}
            </button>
          </div>
        </>
      )}

      {step === 'passphrase' && (
        <>
          <h3>{mode === 'join' ? t('패스프레이즈 입력') : t('패스프레이즈 정하기')}</h3>
          <p className="muted small">
            {mode === 'join'
              ? t('저장소를 만든 기기에서 쓴 것과 같아야 합니다.')
              : t('저장소의 내용을 이것으로 암호화합니다. 다른 기기에서 합류할 때 같은 값을 씁니다.')}
          </p>
          <div className="form-grid">
            <label>{t('패스프레이즈')}</label>
            <input
              type="password"
              value={pass}
              onChange={(e) => setPass(e.target.value)}
              autoFocus
            />
            <label>{t('한 번 더')}</label>
            <input type="password" value={pass2} onChange={(e) => setPass2(e.target.value)} />
          </div>
          {pass.length > 0 && pass.length < 12 && (
            <span className="badge warn">{t('12자 이상이어야 합니다')}</span>
          )}
          {pass2.length > 0 && pass !== pass2 && (
            <span className="badge warn">{t('두 값이 다릅니다')}</span>
          )}
          {mode === 'create' && (
            <p className="warn-text">
              {t('이 패스프레이즈를 잃으면 동기화된 설정을 복구할 수 없습니다. LiteDeck 도 복구할 수 없습니다 — 키를 들고 있지 않기 때문입니다.')}
            </p>
          )}

          <label className="mcp-toggle">
            <input
              type="checkbox"
              checked={remember && canRemember}
              disabled={!canRemember}
              onChange={(e) => setRemember(e.target.checked)}
            />
            <span>{t('이 기기에 기억 (OS 자격 증명 저장소)')}</span>
          </label>
          {!canRemember && (
            <span className="muted small">
              {t('이 기기에는 자격 증명 저장소가 없습니다 — 앱을 열 때마다 패스프레이즈를 묻습니다. 파일로 저장하지는 않습니다.')}
            </span>
          )}

          <div className="dialog-actions">
            <button onClick={() => go('access')}>{t('뒤로')}</button>
            <button
              className="primary"
              disabled={pass.length < 12 || pass !== pass2}
              onClick={() => go('confirm')}
            >
              {t('다음')}
            </button>
          </div>
        </>
      )}

      {step === 'confirm' && (
        <>
          <h3>{mode === 'join' ? t('합류합니다') : t('올립니다')}</h3>
          <div className="mcp-endpoint">
            <label className="muted small">{t('저장소')}</label>
            <code className="mono selectable">{probe?.normalized ?? url.trim()}</code>
          </div>
          {mode === 'create' ? (
            <p className="muted small">
              {t('이 기기의 호스트 {n}개가 암호화되어 올라갑니다. 비밀번호·개인키·sudo 암호는 올라가지 않습니다.', {
                n: hostCount,
              })}
            </p>
          ) : (
            <p className="muted small">
              {t('저장소의 호스트 {n}개를 받습니다. 정책이 느슨한 항목은 바로 적용되지 않고 「확인 대기」에 들어갑니다.', {
                n: probe?.hosts ?? 0,
              })}
            </p>
          )}

          <div className="dialog-actions">
            <button onClick={() => go('passphrase')}>{t('뒤로')}</button>
            <button className="primary" disabled={!canFinish} onClick={() => void finish()}>
              {busy ? t('진행 중…') : mode === 'join' ? t('합류') : t('만들기')}
            </button>
          </div>
        </>
      )}
    </div>
  )
}
