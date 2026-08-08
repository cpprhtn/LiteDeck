import { useEffect, useState } from 'react'
import { DeleteHost, ForgetSecrets, SaveHost, type AuthMethod, type Host } from './ipc'

// Add and edit hosts (§4.1). The import path covers people who already keep an
// ~/.ssh/config; this covers everyone else and every correction afterwards.

const AUTH_LABELS: Record<AuthMethod, string> = {
  agent: 'ssh-agent',
  key: '개인키 파일',
  password: '비밀번호',
}

/** Tried in this order. Agent first: the key never enters this process. */
const AUTH_ORDER: AuthMethod[] = ['agent', 'key', 'password']

export function emptyHost(): Host {
  return {
    id: '',
    name: '',
    hostname: '',
    port: 22,
    user: '',
    auth: ['agent', 'password'],
  }
}

export function HostEditor({
  host,
  onClose,
  onSaved,
  onError,
}: {
  host: Host
  onClose: () => void
  onSaved: () => void
  onError: (msg: string) => void
}) {
  const [draft, setDraft] = useState<Host>(host)
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  useEffect(() => setDraft(host), [host])

  const isNew = host.id === ''
  const set = <K extends keyof Host>(k: K, v: Host[K]) =>
    setDraft((d) => ({ ...d, [k]: v }))

  const toggleAuth = (m: AuthMethod) =>
    setDraft((d) => {
      const has = d.auth.includes(m)
      // Preserve the canonical order rather than the click order, so the
      // fallback sequence does not depend on which checkbox was touched last.
      const next = AUTH_ORDER.filter((x) =>
        x === m ? !has : d.auth.includes(x),
      )
      return { ...d, auth: next }
    })

  const save = async () => {
    setBusy(true)
    try {
      await SaveHost({
        ...draft,
        name: draft.name.trim() || draft.hostname.trim(),
        hostname: draft.hostname.trim(),
        user: draft.user.trim(),
        group: draft.group?.trim() || undefined,
        identityFile: draft.identityFile?.trim() || undefined,
        proxyJump: draft.proxyJump?.trim() || undefined,
        port: Number(draft.port) || 22,
      })
      onSaved()
      onClose()
    } catch (e) {
      onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    setBusy(true)
    try {
      await DeleteHost(draft.id)
      onSaved()
      onClose()
    } catch (e) {
      onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const needsKeyFile = draft.auth.includes('key') && !draft.identityFile?.trim()
  const invalid =
    !draft.hostname.trim() || !draft.user.trim() || draft.auth.length === 0 || needsKeyFile

  return (
    <div className="scrim">
      <form
        className="dialog"
        onSubmit={(e) => {
          e.preventDefault()
          if (!invalid) void save()
        }}
      >
        <h2>{isNew ? '호스트 추가' : '호스트 편집'}</h2>

        <div className="form-grid">
          <label>표시 이름</label>
          <input
            value={draft.name}
            placeholder={draft.hostname || 'prod-web-01'}
            onChange={(e) => set('name', e.target.value)}
          />

          <label>주소</label>
          <input
            value={draft.hostname}
            placeholder="10.0.0.5 또는 example.com"
            spellCheck={false}
            onChange={(e) => set('hostname', e.target.value)}
          />

          <label>포트</label>
          <input
            type="number"
            min={1}
            max={65535}
            value={draft.port}
            onChange={(e) => set('port', Number(e.target.value))}
          />

          <label>사용자</label>
          <input
            value={draft.user}
            placeholder="deploy"
            spellCheck={false}
            onChange={(e) => set('user', e.target.value)}
          />

          <label>그룹</label>
          <input
            value={draft.group ?? ''}
            placeholder="(선택) production"
            onChange={(e) => set('group', e.target.value)}
          />

          <label>인증</label>
          <div className="auth-methods">
            {AUTH_ORDER.map((m) => (
              <label key={m} className="checkbox">
                <input
                  type="checkbox"
                  checked={draft.auth.includes(m)}
                  onChange={() => toggleAuth(m)}
                />
                {AUTH_LABELS[m]}
              </label>
            ))}
            <p className="muted small">
              체크한 순서가 아니라 위 순서대로 시도합니다. ssh-agent가 가장 먼저인
              이유는 개인키가 이 앱에 들어오지 않기 때문입니다.
            </p>
          </div>

          {draft.auth.includes('key') && (
            <>
              <label>개인키 경로</label>
              <input
                value={draft.identityFile ?? ''}
                placeholder="~/.ssh/id_ed25519"
                spellCheck={false}
                onChange={(e) => set('identityFile', e.target.value)}
              />
            </>
          )}

          <label>ProxyJump</label>
          <input
            value={draft.proxyJump ?? ''}
            placeholder="(선택) bastion — 아직 미구현"
            spellCheck={false}
            disabled
            onChange={(e) => set('proxyJump', e.target.value)}
          />
        </div>

        {needsKeyFile && (
          <p className="warn-text">개인키 인증을 쓰려면 키 파일 경로가 필요합니다.</p>
        )}

        <p className="muted small">
          비밀번호는 여기에 저장되지 않습니다 — 접속할 때 묻고, 사용자가 선택하면 OS
          키체인에 들어갑니다.
        </p>

        <div className="dialog-actions">
          {!isNew && (
            <>
              <button
                type="button"
                className="danger"
                disabled={busy}
                onClick={() => setConfirmDelete(true)}
              >
                삭제
              </button>
              <button
                type="button"
                disabled={busy}
                title="저장된 비밀번호·패스프레이즈를 키체인에서 지웁니다"
                onClick={() => void ForgetSecrets(draft.id).catch((e) => onError(String(e)))}
              >
                저장된 비밀 지우기
              </button>
            </>
          )}
          <span className="spacer" />
          <button type="button" onClick={onClose} disabled={busy}>
            취소
          </button>
          <button type="submit" className="primary" disabled={busy || invalid}>
            저장
          </button>
        </div>

        {confirmDelete && (
          <div className="scrim">
            <div className="dialog">
              <h2>호스트를 삭제하시겠습니까?</h2>
              <p className="muted">
                <strong>{draft.name || draft.hostname}</strong> 의 접속 정보와 저장된
                비밀이 함께 삭제됩니다. 서버 자체에는 아무 영향이 없습니다.
              </p>
              <div className="dialog-actions">
                <button onClick={() => setConfirmDelete(false)}>취소</button>
                <button className="danger" disabled={busy} onClick={() => void remove()}>
                  삭제
                </button>
              </div>
            </div>
          </div>
        )}
      </form>
    </div>
  )
}
