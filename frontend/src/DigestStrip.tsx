import { useEffect, useState } from 'react'
import { HostDigest, MarkHostSeen, type DigestView } from './ipc'
import { t } from './i18n'

// "What happened since you last looked" (T-29).
//
// This app gets opened when something is already wrong. Nothing in it gives
// anybody a reason to open it when nothing is — and a tool nobody opens is one
// nobody has running when they finally need it. This strip is the reason:
// the server was up while nobody was watching, and this is the only place that
// answers what it did.
//
// It shows nothing far more often than it shows something, and that is the
// design. A strip that reports four zeroes every time is one people stop
// reading, which is worse than no strip at all — because then they also stop
// reading the time it was not zero.

export function DigestStrip({ hostID }: { hostID: string }) {
  const [view, setView] = useState<DigestView | null>(null)
  const [dismissed, setDismissed] = useState(false)

  useEffect(() => {
    let cancelled = false
    setView(null)
    setDismissed(false)
    HostDigest(hostID)
      .then((d) => !cancelled && setView(d))
      // Silent. This is a courtesy above the tabs; a server that will not
      // answer it should not put an error over the ones that did.
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [hostID])

  // Read means seen. The mark moves when somebody closes the strip rather than
  // when the host connects — moving it on connect would eat the answer before
  // anyone had a chance to look at it.
  const close = () => {
    setDismissed(true)
    void MarkHostSeen(hostID).catch(() => {})
  }

  if (!view || dismissed) return null
  // Nothing to say, could not tell, or a first visit where every count would be
  // "since the journal began" dressed up as news.
  if (view.quiet || view.first || !view.readable) return null

  const parts: string[] = []
  if (view.boots > 0) parts.push(t('재부팅 {n}회', { n: view.boots }))
  if (view.unitFailures > 0) parts.push(t('유닛 실패 {n}건', { n: view.unitFailures }))
  if (view.sudoCommands > 0) parts.push(t('권한 명령 {n}건', { n: view.sudoCommands }))
  if (view.authFailures > 0) parts.push(t('접속 실패 {n}건', { n: view.authFailures }))
  if (parts.length === 0) return null

  return (
    <div className="digest" data-loud={view.boots > 0 || view.unitFailures > 0 || undefined}>
      <span className="digest-cap">{t('마지막으로 본 이후')}</span>
      <span className="digest-body">{parts.join(' · ')}</span>
      <span className="spacer" />
      <button className="ghost small-btn" onClick={close}>
        {t('확인')}
      </button>
    </div>
  )
}
