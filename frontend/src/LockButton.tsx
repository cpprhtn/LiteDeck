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
export function useSudoState(hostID: string): SudoState {
  const [state, setState] = useState<SudoState>({
    hostID,
    unlocked: false,
    available: false,
  })
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
  state: SudoState
  /** Called after the lock turned, so the view can read again with the
   *  permission it now has — or has just given up. */
  onChange?: () => void
}) {
  const [busy, setBusy] = useState(false)

  if (!state.available) {
    // Not a control. Offering a lock that cannot open is worse than saying so.
    return (
      <span className="muted small lock-none" title={t('이 계정에는 sudo 가 없습니다')}>
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
    <button
      className="ghost small-btn lock-btn"
      disabled={busy}
      onClick={() => turn(() => UnlockSecurity(hostID))}
      title={t('sudo 비밀번호를 한 번 묻고, 연결이 끊길 때까지 유지합니다')}
    >
      <Icon name="lock" />
      {t('잠금 해제')}
    </button>
  )
}
