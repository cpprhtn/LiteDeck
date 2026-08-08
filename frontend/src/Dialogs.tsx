import { useEffect, useRef, useState } from 'react'
import {
  AnswerHostKey,
  AnswerSecret,
  CancelPrompt,
  type HostKeyPrompt,
  type SecretPrompt,
} from './ipc'

/**
 * Trust on first use (§7.1).
 *
 * There is no "continue anyway" here for a key that contradicts a known one —
 * that case never reaches this dialog, because sshcore refuses it before
 * anyone is asked. This dialog only ever handles a host nobody has seen before.
 */
export function HostKeyDialog({
  prompt,
  onDone,
}: {
  prompt: HostKeyPrompt
  onDone: () => void
}) {
  const answer = async (decision: 'always' | 'once' | 'reject') => {
    try {
      await AnswerHostKey(prompt.id, decision)
    } finally {
      onDone()
    }
  }

  return (
    <div className="scrim">
      <div className="dialog" role="dialog" aria-modal="true">
        <h2>처음 접속하는 호스트입니다</h2>
        <p className="muted">
          이 서버의 키를 본 적이 없습니다. 지문이 예상과 같은지 확인하세요.
        </p>

        <dl className="keyinfo">
          <dt>주소</dt>
          <dd className="mono">{prompt.address}</dd>
          <dt>키 종류</dt>
          <dd className="mono">{prompt.keyType}</dd>
          <dt>지문</dt>
          <dd className="mono selectable">{prompt.fingerprint}</dd>
        </dl>

        <p className="muted small">
          서버에서 <code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code>{' '}
          으로 대조할 수 있습니다.
        </p>

        <div className="dialog-actions">
          <button onClick={() => answer('reject')}>거부</button>
          <button onClick={() => answer('once')}>이번만 허용</button>
          <button className="primary" onClick={() => answer('always')}>
            항상 신뢰
          </button>
        </div>
      </div>
    </div>
  )
}

/** Password, passphrase and 2FA challenges (§7.3). */
export function SecretDialog({
  prompt,
  onDone,
}: {
  prompt: SecretPrompt
  onDone: () => void
}) {
  const [value, setValue] = useState('')
  const [remember, setRemember] = useState(false)
  const [busy, setBusy] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => inputRef.current?.focus(), [])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      await AnswerSecret(prompt.id, value, remember)
    } finally {
      // The typed secret must not outlive the submit. React state is a string
      // and cannot be wiped, but dropping the only reference means it is
      // garbage the moment this dialog unmounts.
      setValue('')
      onDone()
    }
  }

  const cancel = async () => {
    setBusy(true)
    try {
      await CancelPrompt(prompt.id)
    } finally {
      setValue('')
      onDone()
    }
  }

  return (
    <div className="scrim">
      <form className="dialog" onSubmit={submit} role="dialog" aria-modal="true">
        <h2>인증이 필요합니다</h2>
        <p className="prompt-label">{prompt.label}</p>

        <input
          ref={inputRef}
          type={prompt.echo ? 'text' : 'password'}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          autoComplete="off"
          spellCheck={false}
        />

        {prompt.canRemember ? (
          <label className="checkbox">
            <input
              type="checkbox"
              checked={remember}
              onChange={(e) => setRemember(e.target.checked)}
            />
            OS 키체인에 저장
          </label>
        ) : (
          <p className="muted small">
            이 시스템에는 사용 가능한 키체인이 없어 저장하지 않습니다. 접속할
            때마다 입력이 필요합니다.
          </p>
        )}

        <div className="dialog-actions">
          <button type="button" onClick={cancel} disabled={busy}>
            취소
          </button>
          <button type="submit" className="primary" disabled={busy || !value}>
            확인
          </button>
        </div>
      </form>
    </div>
  )
}
