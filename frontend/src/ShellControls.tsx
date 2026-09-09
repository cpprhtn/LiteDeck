import { useEffect, useState } from 'react'
import {
  CheckForUpdate,
  CheckUpdatesEnabled,
  SetCheckUpdates,
  SetLanguage,
  type UpdateInfo,
} from './ipc'
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

  // Asked once when the shell mounts. The Go side answers from a day-old cache
  // where it has one, so opening the app twice in a day asks github once —
  // see updates_check.go for why this stops at asking.
  useEffect(() => {
    void CheckUpdatesEnabled().then(setChecking).catch(() => {})
    void CheckForUpdate()
      .then(setUpdate)
      // Silent. A version check that cannot reach the internet is not something
      // to interrupt anybody about.
      .catch(() => {})
  }, [])

  return (
    <>
      <span className="muted small mono" title={t('버그 리포트에 이 버전을 함께 적어주세요')}>
        LiteDeck {version ?? '—'}
      </span>
      {update?.newer && update.latest && (
        <a
          className="small update-link"
          href={update.url}
          target="_blank"
          rel="noreferrer"
          title={t('내려받아 덮어쓰면 됩니다. OS 키체인에 저장한 비밀번호는 그대로 남습니다. 자동 업데이트는 준비 중입니다.')}
        >
          {t('{v} 나왔습니다', { v: update.latest })}
        </a>
      )}
      <span className="spacer" />
      {/* The switch for the check above. In the footer beside it rather than
          buried in a settings panel: the request it allows is the only one this
          app makes to anywhere the user did not name, so the control belongs
          where its effect is visible. */}
      <label className="checkbox small" title={t('하루 한 번 github.com 에 새 릴리스가 있는지 묻습니다. 이 앱이 사용자가 지정하지 않은 곳에 보내는 유일한 요청입니다.')}>
        <input
          type="checkbox"
          checked={checking}
          onChange={(e) => {
            const on = e.target.checked
            setChecking(on)
            void SetCheckUpdates(on)
              .then(() => {
                // Turning it off clears the notice too: leaving it up would be
                // showing the result of a check the user just declined.
                if (!on) {
                  setUpdate(null)
                  return
                }
                void CheckForUpdate().then(setUpdate).catch(() => {})
              })
              .catch(() => {})
          }}
        />
        {t('새 버전 확인')}
      </label>
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
