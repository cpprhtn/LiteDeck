import { useEffect, useState } from 'react'
import { Icon } from './icons'
import { LockSecurity, MCPState, UnlockSecurity, on, type MCPStatus } from './ipc'
import { useSudoState } from './LockButton'
import { t } from './i18n'

// The sudo lock, in the rail (§7.2).
//
// # Why it is here and not only on the security tab
//
// The lock is a property of the connection, and since MCP can use it, the
// moment you need it is the moment an AI client asks for something that needs
// root — which is while you are looking at whatever tab you happen to be on.
// Sending somebody to the security tab to turn a switch that has nothing to do
// with what that tab shows is a detour they have to learn, and a switch people
// have to go and find is one they leave shut.
//
// The security tab keeps its own labelled button. This is the same lock, in the
// one place that is on screen whatever else is.
//
// # Only where MCP is on
//
// A host nobody shared with an AI client already has this: the tabs ask for the
// password when they need it, in the place that needed it. The rail shortcut is
// for the case where the thing that needs root is not on screen at all, and
// that case is MCP. For everyone else it would be one more icon to explain.
//
// # Two icons, one row
//
// The state is the icon — shut padlock or open one — because that is what makes
// a glance enough. The label says the same thing in words for the same reason
// the tabs have labels, and the tooltip says what a click will do, which is the
// opposite of what the icon says.

/** Whether this host is shared with AI clients, following changes. */
function useMCPShared(hostID: string | null): boolean {
  const [state, setState] = useState<MCPStatus | null>(null)
  useEffect(() => {
    let alive = true
    void MCPState()
      .then((s) => alive && setState(s))
      .catch(() => {})
    // Go says when the settings change. Without this, sharing a host from the
    // panel would not put the lock in the rail until something else happened to
    // redraw it.
    const off = on<MCPStatus>('mcp:state', setState)
    return () => {
      alive = false
      off()
    }
  }, [hostID])
  if (!hostID) return false
  return !!state?.enabled && !!state.hosts?.[hostID]
}

export function RailLock({ hostID, mini }: { hostID: string | null; mini?: boolean }) {
  const shared = useMCPShared(hostID)
  const sudo = useSudoState(hostID ?? '')
  const [busy, setBusy] = useState(false)

  // Nothing until there is an answer. `available: false` drawn while the probe
  // is in flight says "this account has no sudo", which is usually the opposite
  // of the truth, on a control somebody is reaching for.
  if (!hostID || !shared || !sudo) return null

  const open = sudo.unlocked
  // A third state, and not a lock at all: sudo that needs no password is always
  // in effect and there is nothing held to let go of. Drawn as open and not
  // pressable, because a switch that does nothing when pressed is worse than no
  // switch — which is what this was until SudoState learned to say so.
  const fixed = !sudo.available || sudo.noPassword
  const label = open ? t('sudo 열림') : t('sudo 잠김')
  const title = !sudo.available
    ? t('이 계정에는 sudo가 없습니다')
    : sudo.noPassword
      ? t('이 계정은 비밀번호 없이 sudo를 씁니다 — 잠글 것이 없습니다')
      : open
        ? t('눌러서 잠급니다. 연결이 끊겨도 잠깁니다')
        : t('눌러서 엽니다. 비밀번호를 한 번 묻고 연결이 끊길 때까지 유지합니다')

  const turn = () => {
    if (busy || fixed) return
    setBusy(true)
    void (open ? LockSecurity(hostID) : UnlockSecurity(hostID))
      .catch(() => {})
      .finally(() => setBusy(false))
  }

  if (mini) {
    return (
      <button
        className="rail-mini-btn rail-lock"
        data-open={open || undefined}
        disabled={busy || fixed}
        onClick={turn}
        title={`${label} — ${title}`}
        aria-label={label}
      >
        <Icon name={open ? 'unlock' : 'lock'} />
      </button>
    )
  }

  return (
    <button
      className="rail-item rail-lock"
      data-open={open || undefined}
      disabled={busy || fixed}
      onClick={turn}
      title={title}
    >
      <Icon name={open ? 'unlock' : 'lock'} />
      {label}
    </button>
  )
}
