import { useEffect, useState } from 'react'
import { Icon } from './icons'
import { HostSudoState, LockSecurity, UnlockSecurity, on, type SudoState } from './ipc'
import { t } from './i18n'

// The sudo lock (§7.2).
//
// # One lock, not one per tab
//
// The security tab and the network tab want the same permission for the same
// reason — reading what other users' processes are doing. It used to live only
// in the security tab, so the network tab could say "administrator rights are
// needed" with nowhere on that screen to obtain them, and obtaining them next
// door changed nothing there.
//
// Now the lock is a property of the connection. Turning it anywhere turns it
// everywhere, Go broadcasts the change, and it closes by itself when the
// connection ends. The password is held in memory for that connection and
// written nowhere.

/** Follows the lock for one host, wherever it is turned. */
export function useSudoState(hostID: string): SudoState | null {
  // null until the first answer, not a made-up one. `available: false` as the
  // initial value drew "this account has no sudo" for the moment before the
  // real answer arrived — a flicker that says the opposite of what is usually
  // true, on a control the user is reaching for.
  const [state, setState] = useState<SudoState | null>(null)
  useEffect(() => {
    let alive = true
    void HostSudoState(hostID)
      .then((s) => alive && setState(s))
      .catch(() => {})
    const off = on<SudoState>('sudo:state', (s) => {
      if (s.hostID === hostID) setState(s)
    })
    return () => {
      alive = false
      off()
    }
  }, [hostID])
  return state
}

export function LockButton({
  hostID,
  state,
  onChange,
}: {
  hostID: string
  state: SudoState | null
  /** Called after the lock turned, so the view can read again with the
   *  permission it now has — or has just given up. */
  onChange?: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [cancelled, setCancelled] = useState(false)

  if (!state) {
    // Still asking. Nothing rather than a guess.
    return null
  }
  if (!state.available) {
    // Not a control. Offering a lock that cannot open is worse than saying so.
    return (
      <span className="muted small lock-none" title={t('이 계정에는 sudo가 없습니다')}>
        <Icon name="lock" />
        {t('잠김')}
      </span>
    )
  }

  const turn = (fn: () => Promise<unknown>) => {
    setBusy(true)
    void fn()
      .then(() => onChange?.())
      .catch(() => {})
      .finally(() => setBusy(false))
  }

  if (state.unlocked) {
    return (
      <button
        className="ghost small-btn lock-btn"
        disabled={busy}
        onClick={() => turn(() => LockSecurity(hostID))}
        title={t('연결이 끊기면 자동으로 잠깁니다')}
      >
        <Icon name="unlock" />
        {t('잠그기')}
      </button>
    )
  }
  return (
    <>
      {cancelled && (
        <span className="muted small" title={t('비밀번호 대화상자를 닫으면 잠금은 그대로입니다')}>
          {t('잠긴 채입니다')}
        </span>
      )}
    <button
      className="ghost small-btn lock-btn"
      disabled={busy}
      onClick={() =>
        turn(async () => {
          // The answer matters: false is "the user cancelled the password
          // dialog", which used to look identical to success — the view
          // re-read, found itself still locked, and said nothing.
          setCancelled(false)
          const ok = await UnlockSecurity(hostID)
          if (!ok) setCancelled(true)
          return ok
        })
      }
      title={t('sudo 비밀번호를 한 번 묻고, 연결이 끊길 때까지 유지합니다')}
    >
      <Icon name="lock" />
      {t('잠금 해제')}
    </button>
    </>
  )
}
