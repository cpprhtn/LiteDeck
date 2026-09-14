import { useEffect, useState } from 'react'
import {
  ApplyUpdate,
  CheckForUpdate,
  DownloadUpdate,
  SetLanguage,
  SetTheme,
  UpdateStatus,
  on,
  type UpdateInfo,
  type UpdateState,
} from './ipc'
import { LANGUAGES, getLanguage, setLanguage, t, type Language } from './i18n'
import { THEMES, setTheme, useTheme, type Theme } from './theme'
import { isWebMode } from './webTransport'

// The version, the MCP button, and the language picker — the three shell
// controls that live in the rail footer on the desktop. Extracted so the
// server's "this server" mode, which hides the rail, can put them in the header
// instead without duplicating the markup.
//
// # The update control is one button, four states
//
//   새 버전 확인  →  최신 버전 | 확인 실패
//                 →  1.9.0 업데이트  →  42%  →  업데이트 적용
//
// One control rather than a link beside a button, because at any moment there
// is exactly one thing to do about an update and this should be it. The release
// page stays reachable as the button's tooltip.

/** How long after launch the one automatic check runs.
 *
 *  Behind the first paint and the first connection — a version check has no
 *  business competing with either — and early enough to have happened before
 *  anybody looks at the footer. Once per launch, and no timer after it: an app
 *  somebody opens to do one thing and closes has no reason to poll github all
 *  afternoon. */
const FIRST_CHECK_MS = 10_000

/** Module state, because the component is unmounted whenever the rail folds. */
let checked = false
let lastCheck: UpdateInfo | null = null

export function ShellControls({
  version,
  onOpenMCP,
}: {
  version?: string
  onOpenMCP: () => void
}) {
  const theme = useTheme()
  const [update, setUpdate] = useState<UpdateInfo | null>(null)
  const [checking, setChecking] = useState(false)
  const [install, setInstall] = useState<UpdateState>({ stage: 'idle', percent: 0 })

  const check = () => {
    setChecking(true)
    checked = true
    void CheckForUpdate()
      .then((u) => {
        lastCheck = u
        setUpdate(u)
      })
      .catch(() => {
        lastCheck = { checked: true, reached: false }
        setUpdate(lastCheck)
      })
      .finally(() => setChecking(false))
  }

  useEffect(() => {
    // Nothing to update in server mode: the asset is the desktop build, and Go
    // refuses to install it there. A button that cannot work is worse than no
    // button — see DownloadUpdate.
    if (isWebMode()) return
    // Once per launch, not once per mount. Folding the rail swaps this
    // component for the collapsed one and unmounts it, so every fold and
    // unfold sent another request to github and reset the result to "check for
    // updates" for ten seconds.
    if (checked) {
      setUpdate(lastCheck)
      void UpdateStatus().then(setInstall).catch(() => {})
      return
    }
    const timer = setTimeout(check, FIRST_CHECK_MS)
    // The installer lives in Go and outlives this component, so its state is
    // asked for on mount as well as subscribed to.
    void UpdateStatus()
      .then(setInstall)
      .catch(() => {})
    const off = on<UpdateState>('update:state', setInstall)
    return () => {
      clearTimeout(timer)
      off()
    }
  }, [])

  const newer = !!(update?.newer && update.latest)

  let label = t('새 버전 확인')
  let onClick: (() => void) | undefined = check
  let title = t(
    'github.com에 새 릴리스가 있는지 묻습니다. 이 앱이 사용자가 지정하지 않은 곳에 보내는 유일한 요청입니다.',
  )
  let busy = checking
  let primary = false

  if (install.stage === 'downloading') {
    // A percentage the server did not give is not a percentage. Where there is
    // no content-length the spinner is the whole of the answer.
    label = install.percent >= 0 ? `${install.percent}%` : t('받는 중')
    onClick = undefined
    busy = true
    title = t('릴리스를 내려받아 SHA256SUMS.txt와 대조합니다.')
  } else if (install.stage === 'ready') {
    label = t('업데이트 적용')
    onClick = () => void ApplyUpdate().catch(() => {})
    primary = true
    title = t('앱을 닫고 새 버전으로 다시 엽니다. OS 키체인에 저장한 비밀번호는 그대로 남습니다.')
  } else if (install.stage === 'failed') {
    label = t('업데이트 실패')
    onClick = () => void DownloadUpdate().catch(() => {})
    // Verbatim from Go: it is already a sentence and already translated, and
    // paraphrasing it here would lose which step gave up.
    title = install.error ?? ''
  } else if (newer) {
    label = t('{v} 업데이트', { v: update!.latest! })
    onClick = () => void DownloadUpdate().catch(() => {})
    primary = true
    title = t('{v} 를 내려받아 적용합니다. 릴리스 노트: {url}', {
      v: update!.latest!,
      url: update!.url ?? '',
    })
  } else if (!checking && update?.checked) {
    label = update.reached ? t('최신 버전') : t('확인 실패')
  }

  return (
    <>
      <span className="muted small mono" title={t('버그 리포트에 이 버전을 함께 적어주세요')}>
        LiteDeck {version ?? '—'}
      </span>
      {!isWebMode() && (
      <button
        className={primary ? 'small-btn update-ready' : 'ghost small-btn'}
        disabled={!onClick}
        aria-busy={busy}
        onClick={onClick}
        title={title}
      >
        {/* The slot is always there, spinning or not, and the label does not
            change while it turns. Both together are what keeps the width fixed:
            swapping the label for a circle — or even for a shorter word —
            narrowed the button mid-check, which moved the language picker up
            onto the line above and back again a second later. The width does
            change when a *result* arrives, which is fine: that one is a real
            change of content, and it happens once. */}
        <span className="spin-slot">
          {busy && <span className="spin" aria-label={t('확인 중')} />}
        </span>
        {label}
      </button>
      )}
      <span className="spacer" />
      <button className="ghost small-btn" onClick={onOpenMCP} title={t('MCP 연동 설정')}>
        MCP
      </button>
      {/* Three positions, because "follow the OS" is the only one that keeps
          being right — a desktop that switches at sunset should take the app
          with it. A two-position switch cannot say that, and once you have
          clicked either half you are pinned until you find the control again.

          Applied before it is stored: the colours change on the same frame as
          the click, and Go hears about it afterwards. */}
      <select
        className="lang-select"
        aria-label={t('테마')}
        title={t('테마')}
        value={theme}
        onChange={(e) => {
          const next = e.target.value as Theme
          setTheme(next)
          void SetTheme(next).catch(() => {})
        }}
      >
        {THEMES.map((th) => (
          <option key={th.id || 'system'} value={th.id} title={t(th.title)}>
            {t(th.label)}
          </option>
        ))}
      </select>
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
