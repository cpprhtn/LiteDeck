import { Suspense, lazy, useEffect, useRef, useState } from 'react'
import { Scrim } from './Scrim'
import {
  AnswerHostKey,
  AnswerMCPWrite,
  AnswerSecret,
  CancelPrompt,
  type HostKeyPrompt,
  type MCPWritePrompt,
  type SecretPrompt,
  SetMCPWritePolicy,
  SetMCPHostDelete,
  SetMCPHostExec,
} from './ipc'
import { k, t } from './i18n'

// The diff is the largest thing this dialog can show and most approvals never
// need it, so it arrives with the first file write rather than at startup.
const DiffView = lazy(() => import('./DiffView').then((m) => ({ default: m.DiffView })))


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
    // Escape rejects. Same reason as the MCP approval below: the Scrim eats the
    // key in the capture phase, so a handler anywhere under it is dead code,
    // and a fingerprint dialog that ignores Escape leaves the only way out as
    // a click on one of three buttons.
    <Scrim clickAway={false} onClose={() => answer('reject')}>
      <div className="dialog" role="dialog" aria-modal="true">
        <h2>{t('처음 접속하는 호스트입니다')}</h2>
        <p className="muted">
          {t('이 서버의 키를 본 적이 없습니다. 지문이 예상과 같은지 확인하세요.')}
        </p>

        <dl className="keyinfo">
          <dt>{t('주소')}</dt>
          <dd className="mono">{prompt.address}</dd>
          <dt>{t('키 종류')}</dt>
          <dd className="mono">{prompt.keyType}</dd>
          <dt>{t('지문')}</dt>
          <dd className="mono selectable">{prompt.fingerprint}</dd>
        </dl>

        <p className="muted small">
          {t('서버에서')} <code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code>{' '}
          {t('으로 대조할 수 있습니다.')}
        </p>

        <div className="dialog-actions">
          <button onClick={() => answer('reject')}>{t('거부')}</button>
          <button onClick={() => answer('once')}>{t('이번만 허용')}</button>
          <button className="primary" onClick={() => answer('always')}>
            {t('항상 신뢰')}
          </button>
        </div>
      </div>
    </Scrim>
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
    <Scrim onClose={cancel} clickAway={false}>
      <form className="dialog" onSubmit={submit} role="dialog" aria-modal="true">
        <h2>{t('인증이 필요합니다')}</h2>
        <p className="prompt-label">{prompt.label}</p>

        <input
          ref={inputRef}
          type={prompt.echo ? 'text' : 'password'}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          autoComplete="off"
          spellCheck={false}
        />

        {prompt.sessionOnly ? (
          <p className="muted small">
            {t('이 비밀번호는 저장하지 않습니다 — 연결이 끊기면 잊습니다.')}
          </p>
        ) : prompt.canRemember ? (
          <label className="checkbox">
            <input
              type="checkbox"
              checked={remember}
              onChange={(e) => setRemember(e.target.checked)}
            />
            {t('OS 키체인에 저장')}
          </label>
        ) : (
          <p className="muted small">
            {t('이 시스템에는 사용 가능한 키체인이 없어 저장하지 않습니다. 접속할 때마다 입력이 필요합니다.')}
          </p>
        )}

        <div className="dialog-actions">
          <button type="button" onClick={cancel} disabled={busy}>
            {t('취소')}
          </button>
          <button type="submit" className="primary" disabled={busy || !value}>
            {t('확인')}
          </button>
        </div>
      </form>
    </Scrim>
  )
}

/**
 * The approval a write from an MCP client waits on (§4.6).
 *
 * This dialog is the reason the gate lives in LiteDeck rather than being left
 * to the MCP client. The client's own confirmation shows the arguments a model
 * composed; this shows what the server is about to be told to do — the literal
 * command, or a diff against the file as it stands right now. When a model has
 * been talked into something by text it read in a log, those two are not the
 * same picture, and only the second one gives a person a chance to notice.
 */
export function McpWriteDialog({
  prompt,
  onDone,
}: {
  prompt: MCPWritePrompt | null
  onDone: () => void
}) {
  const deny = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    // Focus lands on Decline. It used to land on Approve, which made Enter —
    // the key somebody is already pressing when a dialog appears over what they
    // were typing — a one-keystroke yes. The comment below said neither button
    // was the default action of the window; the focus said otherwise.
    deny.current?.focus()
  }, [prompt])

  if (!prompt) return null

  const answer = (approved: boolean) => {
    void AnswerMCPWrite(prompt.id, approved).catch(() => {})
    onDone()
  }

  // "Yes, and stop asking" belongs here rather than only in a settings panel.
  // Somebody who has decided to let an agent work unattended decides it at the
  // moment they are being interrupted, not before — and a switch they have to
  // go and find is a switch they turn off by stopping using the feature.
  const allowFor = (minutes: number) => {
    void SetMCPWritePolicy(prompt.hostId, 'bypass', minutes).catch(() => {})
    answer(true)
  }

  if (prompt.grant) return <McpGrantDialog prompt={prompt} answer={answer} denyRef={deny} />
  if (prompt.tool === 'fs_read') return <McpSecretReadDialog prompt={prompt} answer={answer} denyRef={deny} />

  const isFileWrite = prompt.after !== undefined && prompt.path
  // A command follows the same modes as everything else here — the switch that
  // decides whether it is offered at all is elsewhere. What changes is the line
  // at the bottom: undo covers files, and there is nothing to undo here.
  const isExec = prompt.tool === 'run_command'

  return (
    // Escape declines. It goes on the Scrim rather than on the dialog: the
    // Scrim listens in the capture phase and stops the event there, so a
    // handler on the dialog below never ran — the key did nothing at all, and a
    // dialog that ignores Escape trains people to click something instead.
    // A dismissal without a decision must not be read as consent, so it is a
    // decline rather than a close.
    <Scrim clickAway={false} onClose={() => answer(false)}>
      <div className="dialog mcp-approve">
        <h2>{t('MCP 클라이언트가 서버를 바꾸려 합니다')}</h2>
        <p className="muted">
          {t('{host} · {tool}', { host: prompt.host, tool: prompt.tool })}
        </p>

        <p className="warn-text">{prompt.summary}</p>

        <McpGuardNote prompt={prompt} />

        {prompt.command && (
          <>
            <label className="muted small">{t('실행될 명령')}</label>
            <code className="mono mcp-command selectable">{prompt.command}</code>
          </>
        )}

        {isFileWrite && (
          <>
            <label className="muted small">{prompt.path}</label>
            <Suspense fallback={<div className="placeholder">{t('차이를 계산하는 중…')}</div>}>
              <DiffView
                path={prompt.path!}
                before={prompt.before ?? ''}
                after={prompt.after ?? ''}
              />
            </Suspense>
          </>
        )}

        <div className="dialog-actions mcp-approve-actions">
          {/* Decline is first and Approve is the one you reach for, but neither
              is the default action of the window: an approval nobody read is
              the failure this dialog exists to prevent. */}
          <button ref={deny} onClick={() => answer(false)}>
            {t('거부')}
          </button>
          <span className="spacer" />
          {/* Left out for a guarded request: the relaxed mode does not cover
              it, so offering to stop asking would be offering nothing — and
              the next one would arrive as if the button had been broken. */}
          {!prompt.guard && (
            <>
              <button className="ghost" onClick={() => allowFor(60)}>
                {t('허용 후 1시간 동안 묻지 않기')}
              </button>
              {/* The overnight option exists because that is when people actually
                  leave an agent running. An hour expiring at 3am blocks everything
                  until morning, which reads as the feature being broken. */}
              <button className="ghost" onClick={() => allowFor(8 * 60)}>
                {t('허용 후 8시간 동안 묻지 않기')}
              </button>
            </>
          )}
          <button className="danger" onClick={() => answer(true)}>
            {t('이번만 허용')}
          </button>
        </div>
        {/* Stays because this is the moment of the decision, not a preamble.
            It has to be true, though: undo landed after this line was written
            and said for a while that it did not exist. */}
        <p className="muted small">
          {isExec
            ? t('묻지 않기는 이 호스트에만 적용되고 시간이 지나면 스스로 돌아옵니다. 다만 명령 실행은 되돌릴 수 없습니다 — 이 서버에서 아예 끄는 것은 MCP 패널에서 합니다.')
            : t('묻지 않기는 이 호스트에만 적용되고 시간이 지나면 스스로 돌아옵니다. 파일 변경은 MCP 패널의 변경된 파일 탭에서 되돌릴 수 있습니다.')}
        </p>
      </div>
    </Scrim>
  )
}

/**
 * The question for a tool switched off on this host (askGrant in Go).
 *
 * Deleting files and running commands are off per server until somebody turns
 * them on, and that used to surface as a refusal in the agent's transcript —
 * the person found out only by asking why. This brings the question to them at
 * the moment it matters, with the file or the command in view, and lets them
 * answer it there: no, this once, or from now on for this server.
 */
function McpGrantDialog({
  prompt,
  answer,
  denyRef,
}: {
  prompt: MCPWritePrompt
  answer: (approved: boolean) => void
  denyRef: React.RefObject<HTMLButtonElement | null>
}) {
  const del = prompt.grant === 'delete'
  const always = () => {
    const turnOn = del ? SetMCPHostDelete : SetMCPHostExec
    void turnOn(prompt.hostId, true).catch(() => {})
    answer(true)
  }
  return (
    <Scrim clickAway={false} onClose={() => answer(false)}>
      <div className="dialog mcp-approve">
        <h2>{del ? t('AI가 이 서버에서 파일을 지우려 합니다') : t('AI가 이 서버에서 명령을 실행하려 합니다')}</h2>
        <p className="muted">{prompt.host}</p>
        <p className="warn-text">
          {del
            ? t('이 서버는 AI의 파일 삭제가 꺼져 있습니다(기본값). 허용할까요?')
            : t('이 서버는 AI의 명령 실행이 꺼져 있습니다(기본값). 허용할까요?')}
        </p>

        <McpGuardNote prompt={prompt} />

        {del && prompt.path ? (
          <>
            {/* The file as it stands, not a diff: the save dialog's diff
                captions its sides "server" and "to be saved", which is the
                wrong story for a file about to go. */}
            <label className="muted small">{t('지울 파일 — {path}', { path: prompt.path })}</label>
            <pre className="mono mcp-grant-file selectable">{prompt.before ?? ''}</pre>
          </>
        ) : (
          prompt.command && (
            <>
              <label className="muted small">{t('실행될 명령')}</label>
              <code className="mono mcp-command selectable">{prompt.command}</code>
            </>
          )
        )}

        <div className="dialog-actions mcp-approve-actions">
          <button ref={denyRef} onClick={() => answer(false)}>
            {t('거부')}
          </button>
          <span className="spacer" />
          <button className="ghost" onClick={always}>
            {t('이 서버에서 계속 허용')}
          </button>
          <button className="danger" onClick={() => answer(true)}>
            {t('이번만 허용')}
          </button>
        </div>
        <p className="muted small">
          {del
            ? t('지우기 전의 내용은 이 컴퓨터에 보관되어 AI 작업 화면에서 되돌릴 수 있습니다. 「계속 허용」은 MCP 연동 › 권한에서 다시 끌 수 있습니다.')
            : t('명령의 효과는 되돌릴 수 없습니다. 「계속 허용」은 MCP 연동 › 권한에서 다시 끌 수 있고, 켜 둔 뒤에도 실행 전에 묻는지는 변경 승인 모드를 따릅니다.')}
        </p>
      </div>
    </Scrim>
  )
}

/**
 * Why a request is asked about in every approval mode (mcp_guard.go in Go).
 *
 * Said in so many words because this dialog arrives even after somebody chose
 * "stop asking", and a dialog that seems to ignore that choice reads as a bug
 * unless it explains itself.
 */
const GUARD_TEXT: Record<NonNullable<MCPWritePrompt['guard']>, string> = {
  secret: k('키·토큰·비밀번호가 담겼을 수 있는 파일입니다. 내용이 AI 쪽으로 넘어가면 되돌릴 수 없습니다.'),
  foothold: k('접속 권한이나 나중에 저절로 실행될 것을 정하는 파일입니다. 바뀐 뒤 알아챌 때까지 그대로 효력이 있습니다.'),
  opaque: k('실행될 내용이 이 줄에 다 보이지 않습니다 — 다른 곳에서 받아 오거나 풀어낸 코드를 셸에 넘깁니다.'),
  root: k('root 권한을 요청합니다. 이 서버가 비밀번호 없는 sudo를 허용한다면 그대로 root로 실행됩니다.'),
  destructive: k('되돌릴 수 없고 서비스 하나의 범위를 넘는 변경입니다 — 루트·홈 재귀 삭제, 디스크 포맷, 서버 종료 같은 것입니다.'),
}

function McpGuardNote({ prompt }: { prompt: MCPWritePrompt }) {
  if (!prompt.guard) return null
  return (
    <div className="mcp-guard">
      <strong>{t('묻지 않기 모드에서도 항상 확인하는 항목입니다')}</strong>
      <p>{t(GUARD_TEXT[prompt.guard])}</p>
      {prompt.guardDetail && <code className="mono selectable">{prompt.guardDetail}</code>}
    </div>
  )
}

/**
 * A file that probably holds a credential, about to be read (askToRead in Go).
 *
 * Its own dialog rather than the write one: nothing on the server changes, so
 * "the MCP client wants to change the server" would be false, and there is no
 * diff to show. What there is to say is where the contents go once allowed.
 */
function McpSecretReadDialog({
  prompt,
  answer,
  denyRef,
}: {
  prompt: MCPWritePrompt
  answer: (approved: boolean) => void
  denyRef: React.RefObject<HTMLButtonElement | null>
}) {
  return (
    <Scrim clickAway={false} onClose={() => answer(false)}>
      <div className="dialog mcp-approve">
        <h2>{t('AI가 비밀이 담겼을 수 있는 파일을 읽으려 합니다')}</h2>
        <p className="muted">{prompt.host}</p>
        <label className="muted small">{t('읽을 파일')}</label>
        <code className="mono mcp-command selectable">{prompt.path}</code>
        <p className="warn-text">
          {t('허용하면 파일 내용이 MCP 클라이언트로 넘어가 AI의 대화 기록에 남습니다. 서버에서는 아무것도 바뀌지 않지만, 넘어간 내용은 되돌릴 수 없습니다.')}
        </p>
        <div className="dialog-actions mcp-approve-actions">
          <button ref={denyRef} onClick={() => answer(false)}>
            {t('거부')}
          </button>
          <span className="spacer" />
          <button className="danger" onClick={() => answer(true)}>
            {t('이번만 허용')}
          </button>
        </div>
        <p className="muted small">
          {t('이 확인은 승인 모드와 상관없이 뜹니다. 개인키·.env·키 저장소 같은 파일만 해당합니다.')}
        </p>
      </div>
    </Scrim>
  )
}
