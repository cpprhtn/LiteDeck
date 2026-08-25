import { SetLanguage } from './ipc'
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
  return (
    <>
      <span className="muted small mono" title={t('버그 리포트에 이 버전을 함께 적어주세요')}>
        LiteDeck {version ?? '—'}
      </span>
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
