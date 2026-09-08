import { useCallback, useEffect, useState } from 'react'
import {
  HostNetwork,
  HostSecurity,
  LockSecurity,
  UnlockSecurity,
  type FirewallRule,
  type Listener,
  type SecurityUnit,
  type SecurityView as View,
} from './ipc'
import { k, t } from './i18n'

// What is guarding this server (T-35).
//
// The parts of "is this box exposed" were already being collected and were
// sitting in three different tabs — listening ports in the network view, failed
// logins in sessions, pending security updates in monitoring — and none of them
// could say the thing that matters, which is all of it at once. What was
// missing is what is actually guarding the machine.
//
// # Why the lock is on the detail and not on the tab
//
// Rules need root; whether a thing is switched on does not. Putting the whole
// tab behind a password would hide the free half for no reason and teach people
// to type their password at any dialog that appears — and the sshd config
// reader already learned that a view demanding a password before it shows
// anything is a view nobody opens.
//
// # Why both the unit and the file are shown
//
// Measured on a real server: `ufw.service` was enabled and active while
// `/etc/ufw/ufw.conf` said `ENABLED=no`. The unit is a oneshot that loads rules
// at boot and exits, so with the firewall off it starts, does nothing, and
// reports a healthy `active (exited)`. A screen reading the unit alone puts a
// green light on an unprotected machine. The file wins — it is what `ufw
// enable` writes — but the disagreement is shown rather than quietly resolved.

/** Tools whose absence is not news. iptables has no unit on a modern Ubuntu and
 *  saying "not installed" about it every time is noise, not information. */
const QUIET_WHEN_ABSENT = new Set(['iptables.service', 'firewalld.service', 'nftables.service'])

const FIREWALLS = new Set([
  'ufw.service',
  'nftables.service',
  'iptables.service',
  'firewalld.service',
])

export function SecurityView({
  hostID,
  visible,
  onError,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [view, setView] = useState<View | null>(null)
  const [listening, setListening] = useState<Listener[]>([])
  const [busy, setBusy] = useState(false)

  const load = useCallback(
    async (elevate: boolean) => {
      setBusy(true)
      try {
        // The same listener list the network tab shows, rather than a second
        // read of `ss` from inside the security script: two reads of the same
        // thing can disagree, and a security screen disagreeing with the
        // network screen about which ports are open is worse than a round trip.
        const [sec, net] = await Promise.all([
          HostSecurity(hostID, elevate),
          HostNetwork(hostID).catch(() => null),
        ])
        setView(sec)
        setListening(net?.listeners ?? [])
      } catch (e) {
        onError(String(e))
      } finally {
        setBusy(false)
      }
    },
    [hostID, onError],
  )

  // Read once, not polled. A firewall does not change between two ticks of a
  // timer, and the tab is one round trip.
  useEffect(() => {
    if (visible) void load(false)
  }, [visible, load])

  if (!view) {
    return <div className="placeholder">{busy ? t('읽는 중…') : t('보안 상태를 읽는 중…')}</div>
  }

  const firewalls = view.units.filter((u) => FIREWALLS.has(u.name))
  const f2b = view.units.find((u) => u.name === 'fail2ban.service')
  const active = firewalls.filter((u) => u.installed && u.active)
  // ufw's own file overrides its unit — see the note at the top.
  const ufwOff = view.ufwConfFound && !view.ufwEnabled
  const guarded = active.some((u) => !(u.name === 'ufw.service' && ufwOff))

  const unlock = async () => {
    setBusy(true)
    try {
      // Closing the dialog answers false. Told apart by the return value rather
      // than by reading the text of an error, which would break the first time
      // somebody switched the app to English.
      if (await UnlockSecurity(hostID)) {
        await load(true)
      } else {
        setBusy(false)
      }
    } catch (e) {
      onError(String(e))
      setBusy(false)
    }
  }

  const lock = async () => {
    await LockSecurity(hostID).catch(() => {})
    void load(false)
  }

  return (
    <div className="view security-view">
      <div className="view-toolbar">
        <span className="security-verdict" data-guarded={guarded || undefined}>
          {guarded ? t('방화벽 켜짐') : t('방화벽 없음')}
        </span>
        <span className="spacer" />
        {busy && <span className="muted small">{t('읽는 중…')}</span>}
        <LockButton view={view} onUnlock={() => void unlock()} onLock={() => void lock()} />
        <button className="ghost small-btn" disabled={busy} onClick={() => void load(view.unlocked)}>
          {t('다시 읽기')}
        </button>
      </div>

      <div className="security-body">
        <section className="panel">
          <h3>{t('방화벽')}</h3>
          {firewalls.filter((u) => u.installed || !QUIET_WHEN_ABSENT.has(u.name)).length === 0 && (
            <p className="muted small">{t('방화벽 도구가 설치되어 있지 않습니다.')}</p>
          )}
          {firewalls.map((u) =>
            !u.installed && QUIET_WHEN_ABSENT.has(u.name) ? null : (
              <ToolRow
                key={u.name}
                unit={u}
                override={
                  u.name === 'ufw.service' && view.ufwConfFound
                    ? { on: view.ufwEnabled, from: '/etc/ufw/ufw.conf' }
                    : undefined
                }
              />
            ),
          )}
          {ufwOff && (
            <p className="security-warn small">
              {t('ufw 유닛은 활성이지만 ufw 자체는 꺼져 있습니다 — 규칙을 싣지 않고 끝난 것입니다.')}
            </p>
          )}
        </section>

        <section className="panel">
          <h3>fail2ban</h3>
          {f2b ? (
            <ToolRow unit={f2b} />
          ) : (
            <p className="muted small">{t('설치되어 있지 않습니다.')}</p>
          )}
          {view.jails.length > 0 && (
            <p className="muted small">
              {t('설정 파일이 켜 둔 jail')}: <span className="mono">{view.jails.join(' · ')}</span>
            </p>
          )}
          {f2b?.installed && view.jails.length === 0 && (
            <p className="muted small">
              {t('jail.local 을 찾지 못했습니다 — 실제로 도는 jail 은 잠금을 열어야 보입니다.')}
            </p>
          )}
        </section>

        <section className="panel security-detail">
          <h3>{t('규칙')}</h3>
          {view.unlocked ? (
            <>
              {view.firewall ? (
                <RuleTable status={view.firewall} listening={listening} raw={view.rules} />
              ) : (
                view.rules && <pre className="mono small security-pre">{view.rules}</pre>
              )}
              {view.bans && <pre className="mono small security-pre">{view.bans}</pre>}
            </>
          ) : (
            <p className="muted small">
              {view.rulesError
                ? view.rulesError
                : t('규칙과 차단 목록은 관리자 권한이 필요합니다. 위 자물쇠를 여세요.')}
            </p>
          )}
        </section>
      </div>
    </div>
  )
}

/** The rules, folded and checked against what is actually listening.
 *
 *  ufw prints every rule twice, once per address family, so seven rules arrive
 *  as fourteen lines — the adapter folds those. What is left is a list of open
 *  ports, and the question a reader actually has about it is which of them lead
 *  anywhere. A port allowed with nothing behind it is not dangerous today; it
 *  is a door that opens the moment something binds to that number. */
function RuleTable({
  status,
  listening,
  raw,
}: {
  status: NonNullable<View['firewall']>
  listening: Listener[]
  raw?: string
}) {
  const open = new Set(listening.filter((l) => l.exposed).map((l) => l.port))
  const heard = (r: FirewallRule) => (r.ports ?? []).some((p) => open.has(p))
  const idle = status.rules.filter((r) => r.action.includes('ALLOW') && !heard(r))

  return (
    <>
      {/* The default policy first: a rule list under `allow (incoming)` is
          decoration, and reading the rules without it tells you nothing. */}
      <p className="security-policy small">
        {t('들어오는 것')} <b data-deny={status.incoming === 'deny' || undefined}>{status.incoming}</b>
        {' · '}
        {t('나가는 것')} <b>{status.outgoing}</b>
        {status.routed ? ` · ${t('경유')} ${status.routed}` : ''}
      </p>

      <div className="security-rules">
        {status.rules.map((r, i) => (
          <div className="security-rule" key={`${r.to}-${i}`} data-idle={!heard(r) || undefined}>
            <span className="mono security-rule-to">{r.to}</span>
            <span className="small">{r.action}</span>
            <span className="muted small">{r.from}</span>
            <span className="muted small">
              {r.comment && <span className="security-profile">{r.comment}</span>}
              {r.v4 && r.v6 ? ' v4·v6' : r.v6 ? ' v6' : ' v4'}
            </span>
            <span className="small security-heard">
              {heard(r) ? t('듣는 중') : t('아무것도 안 듣는 중')}
            </span>
          </div>
        ))}
      </div>

      {idle.length > 0 && (
        <p className="muted small">
          {t('허용은 됐지만 지금 아무것도 듣지 않는 규칙 {n}개 — 지금 위험하지는 않지만, 무엇이든 그 포트를 잡는 순간 열립니다.', {
            n: idle.length,
          })}
        </p>
      )}

      {/* The text it was parsed from, so a reader who doubts the table above
          can check it rather than take it on faith. */}
      {raw && (
        <details>
          <summary className="muted small">{t('원본 보기')}</summary>
          <pre className="mono small security-pre">{raw}</pre>
        </details>
      )}
    </>
  )
}

/** One tool, and the two things that can disagree about it. */
function ToolRow({
  unit,
  override,
}: {
  unit: SecurityUnit
  /** What the tool's own config says, where that can be read without root. */
  override?: { on: boolean; from: string }
}) {
  const name = unit.name.replace(/\.service$/, '')
  const disagrees = override !== undefined && override.on !== unit.active
  const state = !unit.installed
    ? t('설치 안 됨')
    : override
      ? override.on
        ? t('켜짐')
        : t('꺼짐')
      : unit.active
        ? t('활성')
        : t('비활성')

  return (
    <div className="security-row" data-off={unit.installed && !(override?.on ?? unit.active) || undefined}>
      <span className="mono security-tool">{name}</span>
      <span className="security-state">{state}</span>
      {unit.installed && (
        <span className="muted small security-src" title={override ? override.from : 'systemd'}>
          {override ? override.from : t('유닛 {state}', { state: unit.subState || '' })}
          {disagrees && <span className="security-warn"> ⚠</span>}
        </span>
      )}
    </div>
  )
}

/** Three states, because "cannot" and "have not" are different answers. */
function LockButton({
  view,
  onUnlock,
  onLock,
}: {
  view: View
  onUnlock: () => void
  onLock: () => void
}) {
  if (!view.canElevate) {
    return (
      <span className="muted small" title={t('이 계정에는 sudo 가 없습니다')}>
        🚫 {t('잠김')}
      </span>
    )
  }
  if (view.unlocked) {
    return (
      <button className="ghost small-btn" onClick={onLock} title={t('연결이 끊기면 자동으로 잠깁니다')}>
        🔓 {t('잠그기')}
      </button>
    )
  }
  return (
    <button
      className="ghost small-btn"
      onClick={onUnlock}
      title={
        view.freeElevation
          ? t('이 서버는 비밀번호 없이 열립니다')
          : t('sudo 비밀번호를 한 번 묻고, 연결이 끊길 때까지 유지합니다')
      }
    >
      🔒 {t('잠금 해제')}
    </button>
  )
}

export const SECURITY_TAB_LABEL = k('보안')
