import { useCallback, useEffect, useState } from 'react'
import {
  HostNetwork,
  HostSecurity,
  LockSecurity,
  RememberSecurityLogins,
  SecurityLogins,
  UnlockSecurity,
  type Attacker,
  type FirewallRule,
  type Listener,
  type Login,
  type NftCounter,
  type NftSet,
  type SecurityUnit,
  type SecurityView as View,
} from './ipc'
import { AccessNotice } from './EventTimeline'
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
  const [logins, setLogins] = useState<Login[]>([])
  const [fresh, setFresh] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)

  const load = useCallback(
    async (elevate: boolean) => {
      setBusy(true)
      try {
        // The same listener list the network tab shows, rather than a second
        // read of `ss` from inside the security script: two reads of the same
        // thing can disagree, and a security screen disagreeing with the
        // network screen about which ports are open is worse than a round trip.
        const [sec, net, who] = await Promise.all([
          HostSecurity(hostID, elevate),
          HostNetwork(hostID).catch(() => null),
          SecurityLogins(hostID).catch(() => null),
        ])
        setView(sec)
        setListening(net?.listeners ?? [])
        if (who) {
          const [view, unseen] = who
          setLogins(view.logins ?? [])
          setFresh(new Set(unseen ?? []))
          // Marked only now, after the list is on screen. Doing it inside the
          // read would spend the surprise before anybody had it.
          if (unseen?.length) void RememberSecurityLogins(hostID, unseen).catch(() => {})
        }
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

  const f2b = view.units.find((u) => u.name === 'fail2ban.service')
  const ufwOff = view.ufwConfFound && !view.ufwEnabled

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
        <span className="security-verdict" data-verdict={view.verdict}>
          {view.verdict === 'on'
            ? t('방화벽 켜짐')
            : view.verdict === 'none'
              ? t('방화벽 없음')
              : t('방화벽 확인 필요')}
        </span>
        <span className="spacer" />
        {busy && <span className="muted small">{t('읽는 중…')}</span>}
        <LockButton view={view} onUnlock={() => void unlock()} onLock={() => void lock()} />
        <button className="ghost small-btn" disabled={busy} onClick={() => void load(view.unlocked)}>
          {t('다시 읽기')}
        </button>
      </div>

      <div className="security-body">
        <Logins logins={logins} fresh={fresh} />

        <section className="panel">
          <h3>{t('방화벽')}</h3>
          <FirewallSummary view={view} />
          {ufwOff && (
            <p className="security-warn small">
              {t('ufw 유닛은 활성이지만 ufw 자체는 꺼져 있습니다 — 규칙을 싣지 않고 끝난 것입니다.')}
            </p>
          )}
          {view.verdict === 'unknown' && (
            <p className="muted small">
              {t('무언가 커널 패킷 필터를 쓰고 있는데 어느 도구인지 알 수 없습니다 — 도커도 이렇게 보입니다. 잠금을 열면 규칙을 셀 수 있습니다.')}
            </p>
          )}
        </section>

        <section className="panel">
          <h3>fail2ban</h3>
          {view.mismatches && view.mismatches.length > 0 && (
            <div className="security-mismatch">
              <p className="small">
                {t('설정 파일과 실제로 도는 값이 다릅니다 — fail2ban 을 다시 시작해야 파일이 읽힙니다.')}
              </p>
              {view.mismatches.map((m) => (
                <p key={m.key} className="mono small">
                  {m.key}: {t('설정값')} <b>{m.declared}</b> · {t('실제 동작')}{' '}
                  <b>{m.key === 'maxretry' ? m.running : `${m.running} (${seconds(m.running)})`}</b>
                </p>
              ))}
            </div>
          )}
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

        <Blocking view={view} />

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

/** fail2ban reports its durations in seconds and the file is written in its own
 *  syntax, so the two halves of a mismatch read as different kinds of thing.
 *  Spelling the seconds out is what makes `1d` against `600` legible as the
 *  disagreement it is. */
function seconds(v: string): string {
  const n = Number(v)
  if (!Number.isFinite(n) || n < 60) return v
  if (n % 86400 === 0) return t('{n}일', { n: n / 86400 })
  if (n % 3600 === 0) return t('{n}시간', { n: n / 3600 })
  if (n % 60 === 0) return t('{n}분', { n: n / 60 })
  return v
}

/** Who actually got in, and which of them came from somewhere new.
 *
 *  First on the screen, because it is the only thing here that can be urgent.
 *  Failed passwords arrive by the thousand on any box facing the internet — two
 *  to twenty thousand a day on the servers this was built against — and mean
 *  nothing on their own. One success from an address nobody recognises means
 *  something whatever the failure count says.
 *
 *  Reboots are left out. `last` puts the kernel version where the address goes
 *  on those rows, and marking them would flag every restart as a stranger. */
function Logins({ logins, fresh }: { logins: Login[]; fresh: Set<string> }) {
  const rows = logins.filter((l) => !l.boot).slice(0, 8)
  const news = rows.filter((l) => l.from && fresh.has(l.from))

  return (
    <section className="panel">
      <h3>{t('최근 접속 성공')}</h3>
      {news.length > 0 && (
        <p className="security-warn small">
          {t('처음 보는 주소에서 접속에 성공한 기록이 {n}건 있습니다.', { n: news.length })}
        </p>
      )}
      {rows.length === 0 && <p className="muted small">{t('기록이 없습니다.')}</p>}
      {rows.map((l, i) => (
        <div className="security-login" key={`${l.at}-${i}`} data-new={l.from && fresh.has(l.from) ? true : undefined}>
          <span className="mono security-login-user">{l.user}</span>
          <span className="mono small">{l.from || t('콘솔')}</span>
          <span className="muted small">{new Date(l.at).toLocaleString()}</span>
          <span className="small">
            {l.from && fresh.has(l.from) ? (
              <span className="security-warn">{t('처음 보는 주소')}</span>
            ) : l.open ? (
              t('접속 중')
            ) : (
              ''
            )}
          </span>
        </div>
      ))}
    </section>
  )
}

/** The firewall, as one statement rather than a row per tool.
 *
 *  Listing ufw and nftables side by side is what produced "ufw 켜짐 · nftables
 *  비활성", which is a contradiction: on a modern Ubuntu ufw *runs on*
 *  nftables through iptables-nft. They are a front end and its back end, not
 *  two firewalls, and the screen now says so. */
function FirewallSummary({ view }: { view: View }) {
  const front = view.ufwConfFound
    ? { name: 'ufw', on: view.ufwEnabled, from: '/etc/ufw/ufw.conf' }
    : view.units.find((u) => u.name === 'firewalld.service' && u.active)
      ? { name: 'firewalld', on: true, from: 'systemd' }
      : null

  const backend = view.kernel.nftables
    ? { name: 'nftables', refs: view.kernel.nftablesRefs }
    : view.kernel.iptables
      ? { name: 'iptables', refs: view.kernel.iptablesRefs }
      : null

  return (
    <>
      <div className="security-row" data-off={front !== null && !front.on ? true : undefined}>
        <span className="mono security-tool">{front ? front.name : t('전면부 없음')}</span>
        <span className="security-state">
          {front ? (front.on ? t('켜짐') : t('꺼짐')) : t('알 수 없음')}
        </span>
        <span className="muted small security-src">{front ? front.from : ''}</span>
      </div>
      {backend && (
        <p className="muted small">
          {t('커널 백엔드')}: <span className="mono">{backend.name}</span>{' '}
          {backend.refs > 0
            ? t('사용 중 (참조 {n})', { n: backend.refs })
            : t('올라와 있지만 참조 없음')}
        </p>
      )}
    </>
  )
}

/** What is being blocked, whether it is working, and who is still getting in.
 *
 *  The counter is the only number in this tab that says a thing is *working*
 *  rather than configured. Everything else — a unit that is enabled, a file that
 *  says yes — describes an arrangement; the packet count describes an effect.
 *
 *  The attacker list has already-blocked addresses removed, including the ones
 *  inside a blocked network. Leaving them in gives a list nobody can act on,
 *  and it misleads twice over: a banned address goes on appearing in the log
 *  for as long as the window reaches back past the ban. */
function Blocking({ view }: { view: View }) {
  const sets = view.sets ?? []
  const counters = (view.counters ?? []).filter((c) => c.packets > 0)
  const attackers = view.attackers ?? []
  const clusters = view.clusters ?? []
  const jail = view.jail

  return (
    <section className="panel">
      <h3>{t('차단 현황')}</h3>

      {jail && (
        <div className="security-tiles">
          <Tile label={t('지금 차단 중')} value={jail.currentlyBanned} />
          <Tile label={t('누적 차단')} value={jail.totalBanned} />
          <Tile label={t('누적 실패')} value={jail.totalFailed} />
          {/* No repeat rate here, deliberately. It is bans divided by the
              *distinct addresses ever banned*, and `fail2ban-client status`
              does not report that — it gives the total, and how many are
              banned right now. Dividing by the second produced 143.5 on a
              server whose real figure was about 4, which is worse than no
              number: this tab's whole argument is that a wrong reading is
              worse than a missing one. It needs the ban log, which is a
              separate read. */}
        </div>
      )}

      {counters.length > 0 && (
        <div className="security-counters">
          {counters.map((c, i) => (
            <p key={i} className="small">
              <span className="mono">{c.table}</span>
              {c.fail2ban && <span className="security-profile">fail2ban</span>}{' '}
              {t('{verdict} {n}개 패킷', { verdict: c.verdict, n: c.packets.toLocaleString() })}
            </p>
          ))}
        </div>
      )}

      {sets.map((s) => (
        <BlockedSet key={`${s.table}-${s.name}`} set={s} />
      ))}

      {/* Access before contents, always. An empty list where the journal could
          not be read reads as "nobody is knocking", which is the one thing
          this screen must never say by accident. */}
      {view.attackersAccess !== 'ok' ? (
        <p className="muted small">
          {t('공격 시도를 읽으려면 저널 권한이 필요합니다 — 목록이 비어 있는 것과 다릅니다.')}
        </p>
      ) : attackers.length === 0 ? (
        <p className="muted small">{t('최근 15분 동안 막히지 않은 시도는 없습니다.')}</p>
      ) : (
        <>
          <p className="muted small">
            {t('최근 15분, 아직 차단되지 않은 시도')}
          </p>
          {attackers.map((a) => (
            <AttackerRow key={a.address} attacker={a} />
          ))}
          {clusters.map((c) => (
            <p key={c.cidr} className="small security-cluster">
              {t('{cidr} 에서 {hosts}대가 {n}회 — 대역째 막는 편이 낫습니다', {
                cidr: c.cidr, hosts: c.hosts, n: c.count,
              })}
            </p>
          ))}
        </>
      )}
    </section>
  )
}

function Tile({ label, value, hint }: { label: string; value: number | string; hint?: string }) {
  return (
    <div className="security-tile" title={hint}>
      <div className="num">{value}</div>
      <div className="muted small">{label}</div>
    </div>
  )
}

/** One block list. fail2ban's and a hand-made one are marked apart: its entries
 *  come and go on their own as bans expire, and a hand-made table stays until
 *  somebody takes it out. Mixing them loses the only question worth asking —
 *  which of these did I put there. */
function BlockedSet({ set }: { set: NftSet }) {
  return (
    <div className="security-set">
      <p className="small">
        <span className="mono">{set.table}</span>
        <span className="security-profile">{set.fail2ban ? t('자동') : t('직접 추가')}</span>{' '}
        <span className="muted">
          {t('{n}개', { n: set.elements.length })}
          {set.ranges > 0 && ` · ${t('대역 {n}개', { n: set.ranges })}`}
        </span>
      </p>
      <div className="security-chips">
        {set.elements.map((e) => (
          <span key={e} className="mono security-chip" data-range={e.includes('/') || undefined}>
            {e}
          </span>
        ))}
      </div>
    </div>
  )
}

/** One address still getting through, and the command that would stop it.
 *
 *  Text, not a button. A wrong firewall rule ends the session it was typed
 *  from, and unlike every other write this app offers there is no copy to
 *  restore from — see T-38. Copying is the same answer the command history
 *  gives, for the same reason. */
function AttackerRow({ attacker }: { attacker: Attacker }) {
  const [copied, setCopied] = useState(false)
  const cmd = `sudo nft add element inet blackhole banned { ${attacker.address} }`
  return (
    <button
      className="security-attacker"
      onClick={() => {
        void navigator.clipboard?.writeText(cmd).catch(() => {})
        setCopied(true)
        setTimeout(() => setCopied(false), 1200)
      }}
      title={`${cmd}\n\n${t('클릭하면 복사')}`}
    >
      <span className="mono">{attacker.address}</span>
      <span className="muted small">{t('{n}회', { n: attacker.count })}</span>
      {copied && <span className="small history-copied">{t('복사됨')}</span>}
    </button>
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
        {t('인바운드')} <b data-deny={status.incoming === 'deny' || undefined}>{status.incoming}</b>
        {' · '}
        {t('아웃바운드')} <b>{status.outgoing}</b>
        {status.routed ? ` · ${t('라우팅')} ${status.routed}` : ''}
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
              {heard(r) ? t('사용 중') : t('미사용')}
            </span>
          </div>
        ))}
      </div>

      {idle.length > 0 && (
        <p className="muted small">
          {t('열어 뒀지만 지금 아무 서비스도 안 쓰는 포트 {n}개 — 지금 위험하지는 않지만, 무엇이든 그 포트를 잡는 순간 외부에 열립니다.', {
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
