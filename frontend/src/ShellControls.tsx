import { useEffect, useState } from 'react'
import { CheckForUpdate, SetLanguage, type UpdateInfo } from './ipc'
import { LANGUAGES, getLanguage, setLanguage, t, type Language } from './i18n'

// The version, the MCP button, and the language picker — the three shell
// controls that live in the sidebar footer on the desktop. Extracted so the
// server's "this server" mode, which hides the sidebar, can put them in the
// header instead without duplicating the markup.
export function ShellControls({
  version,
  onOpenMCP,
}: {
  version?: string
  onOpenMCP: () => void
}) {
  const [update, setUpdate] = useState<UpdateInfo | null>(null)
  const [checking, setChecking] = useState(false)

  // Nothing goes out until this is pressed. That is the whole privacy story —
  // no background request means no preference to explain, which is what the
  // checkbox that used to sit here was failing to do.
  const check = () => {
    setChecking(true)
    void CheckForUpdate()
      .then(setUpdate)
      .catch(() => setUpdate({ checked: true, reached: false }))
      .finally(() => setChecking(false))
  }

  return (
    <>
      <span className="muted small mono" title={t('버그 리포트에 이 버전을 함께 적어주세요')}>
        LiteDeck {version ?? '—'}
      </span>
      {/* A new release is the one state that becomes a link — everything else
          this button says is about the button. */}
      {update?.newer && update.latest ? (
        <a
          className="small update-link"
          href={update.url}
          target="_blank"
          rel="noreferrer"
          title={t('내려받아 덮어쓰면 됩니다. OS 키체인에 저장한 비밀번호는 그대로 남습니다. 자동 업데이트는 준비 중입니다.')}
        >
          {t('{v} 나왔습니다', { v: update.latest })}
        </a>
      ) : (
        <button
          className="ghost small-btn"
          disabled={checking}
          aria-busy={checking}
          onClick={check}
          title={t('github.com 에 새 릴리스가 있는지 묻습니다. 이 앱이 사용자가 지정하지 않은 곳에 보내는 유일한 요청입니다.')}
        >
          {/* The slot is always there, spinning or not, and the label does not
              change while it turns. Both together are what keeps the width
              fixed: swapping the label for a circle — or even for a shorter
              word — narrowed the button mid-check, which moved the language
              picker up onto the line above and back again a second later.
              The width does change when a *result* arrives, which is fine:
              that one is a real change of content, and it happens once. */}
          <span className="spin-slot">
            {checking && <span className="spin" aria-label={t('확인 중')} />}
          </span>
          {update?.checked && !checking
            ? update.reached
              ? t('최신 버전')
              : t('확인 실패')
            : t('새 버전 확인')}
        </button>
      )}
      <span className="spacer" />
      <button className="ghost small-btn" onClick={onOpenMCP} title={t('MCP 연동 설정')}>
        MCP
      </button>
      {/* Each language is named in its own script: somebody who cannot read the
          current UI is exactly the person looking for this control. */}
      <select
        className="lang-select"
        aria-label="Language"
        value={getLanguage()}
        onChange={(e) => {
          const next = e.target.value as Language
          setLanguage(next)
          void SetLanguage(next).catch(() => {})
        }}
      >
        {LANGUAGES.map((l) => (
          <option key={l.id} value={l.id} title={l.title}>
            {l.label}
          </option>
        ))}
      </select>
    </>
  )
}
