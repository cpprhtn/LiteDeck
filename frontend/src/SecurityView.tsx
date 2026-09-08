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
  type Ban,
  type FailureBucket,
  type NftSet,
  type SecurityUnit,
  type SecurityView as View,
} from './ipc'
import { Panel } from './ResourceView'
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
  const jail = view.jail
  const front = view.ufwConfFound
    ? { name: 'ufw', on: view.ufwEnabled }
    : view.units.find((u) => u.name === 'firewalld.service' && u.active)
      ? { name: 'firewalld', on: true }
      : null
  const backendLine = view.kernel?.nftables
    ? `nftables ${view.kernel.nftablesRefs > 0 ? t('사용 중 (참조 {n})', { n: view.kernel.nftablesRefs }) : t('올라와 있지만 참조 없음')}`
    : view.kernel?.iptables
      ? 'iptables'
      : ''
  const dropCounters = (view.counters ?? []).filter((c) => c.packets > 0)
  const dropped = view.dropped || dropCounters.reduce((n, c) => n + c.packets, 0)
  // Bans divided by the addresses they landed on. Computed from the ban log,
  // which is the only place the second number exists — dividing by the count
  // banned right now once turned ten bans on five addresses into 143.
  const bans = view.banHistory
    ? { ...view.banHistory, repeats: view.banHistory.bans.length / view.banHistory.unique }
    : null
  const lastLogin = logins.find((l) => !l.boot)
  const news = logins.filter((l) => !l.boot && l.from && fresh.has(l.from))

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
        {/* Why it is unclear belongs on the verdict, not on a line of its own:
            it is read once, by whoever wonders, and a screen that explains
            itself in prose beside every state is a screen nobody finishes. */}
        <span
          className="security-verdict"
          data-verdict={view.verdict}
          title={view.verdict === 'unknown' ? t('도커도 커널 필터를 이렇게 씁니다') : undefined}
        >
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
        {/* The same panel grid the monitoring tab uses. Not a new language for
            a new tab: a screen that looks like the rest of the app is one
            people can read without learning it, and this one had drifted into
            a column of sentences while every other view had settled on a grid
            of panels with a label and a figure. */}
        <div className="res-grid">
          <Panel
            label={t('방화벽')}
            value={front ? (front.on ? front.name : t('꺼짐')) : t('알 수 없음')}
            warn={view.verdict !== 'on'}
            name
            sub={backendLine ? [backendLine] : undefined}
          />
          <Panel
            label="fail2ban"
            value={f2b?.active ? t('활성') : f2b?.installed ? t('비활성') : t('없음')}
            warn={!f2b?.active}
            name
            sub={view.jails.length > 0 ? [`jail: ${view.jails.join(' · ')}`] : undefined}
          />
          <Panel
            label={t('지금 차단 중')}
            value={jail ? String(jail.currentlyBanned) : '—'}
            sub={jail ? [t('누적 {n}회', { n: jail.totalBanned.toLocaleString() })] : undefined}
          />
          <Panel
            label={t('버린 패킷')}
            value={dropped > 0 ? dropped.toLocaleString() : '—'}
            sub={[
              // The rise is what says it is still happening. A total says it
              // happened at some point, which a switched-off rule also does.
              view.droppedSince
                ? t('마지막으로 본 뒤 +{n}', { n: view.droppedSince.toLocaleString() })
                : dropCounters.map((c) => `${c.table} ${c.verdict}`).join(' · '),
            ].filter(Boolean)}
          />
          <Panel
            label={t('최근 접속')}
            value={lastLogin ? lastLogin.from || t('콘솔') : '—'}
            warn={news.length > 0}
            name
            sub={
              lastLogin
                ? [`${lastLogin.user} · ${new Date(lastLogin.at).toLocaleString()}`]
                : undefined
            }
          />
          {bans && bans.unique > 0 && (
            <Panel
              label={t('재범률')}
              value={bans.repeats.toFixed(1)}
              warn={bans.repeats >= 3}
              sub={[t('{n}회를 {u}개 주소에', { n: bans.bans.length, u: bans.unique })]}
            />
          )}
          <Panel
            label={t('누적 실패')}
            value={jail ? jail.totalFailed.toLocaleString() : '—'}
            sub={
              view.attackersAccess === 'ok'
                ? [t('최근 15분 미차단 {n}건', { n: (view.attackers ?? []).length })]
                : [t('저널 권한 필요')]
            }
          />
        </div>

        {view.mismatches && view.mismatches.length > 0 && (
          <section className="panel">
            <h3>{t('설정 불일치')}</h3>
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
          </section>
        )}

        {view.failures && view.failures.length > 0 && (
          <section className="panel">
            <h3>{t('24시간 실패 추이')}</h3>
            <FailureChart buckets={view.failures} bans={bans?.bans ?? []} />
          </section>
        )}

        <Logins logins={logins} fresh={fresh} />
        <Blocking view={view} />

        {bans && bans.bans.length > 0 && (
          <section className="panel">
            <h3>{t('최근 차단')}</h3>
            {bans.bans.slice(0, 10).map((b, i) => (
              <div className="security-ban" key={`${b.at}-${i}`}>
                <span className="mono">{b.address}</span>
                <span className="muted small">{new Date(b.at).toLocaleString()}</span>
              </div>
            ))}
          </section>
        )}

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
                : t('관리자 권한이 필요합니다.')}
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

/** Failures over a day, with the moments a ban went on marked underneath.
 *
 *  The bars alone can mislead: an address blocked an hour ago still fills the
 *  bars from before that, which reads as "not handled". The ban marks are what
 *  turn a tall bar into a story — one that falls after a mark is the block
 *  working, and one that does not is somebody the block did not cover. */
function FailureChart({ buckets, bans }: { buckets: FailureBucket[]; bans: Ban[] }) {
  const peak = Math.max(...buckets.map((b) => b.count), 1)
  const from = new Date(buckets[0].at).getTime()
  const to = new Date(buckets[buckets.length - 1].at).getTime() + 3_600_000
  const span = Math.max(to - from, 1)

  return (
    <div className="security-chart">
      <div className="security-bars">
        {buckets.map((b) => (
          <div
            key={b.at}
            className="security-bar"
            style={{ height: `${Math.max((b.count / peak) * 100, 2)}%` }}
            title={`${new Date(b.at).toLocaleString()} · ${b.count}`}
          />
        ))}
        {bans.map((b, i) => {
          const at = new Date(b.at).getTime()
          if (at < from || at > to) return null
          return (
            <span
              key={`${b.at}-${i}`}
              className="security-banmark"
              style={{ left: `${((at - from) / span) * 100}%` }}
              title={`${t('차단')} ${b.address} · ${new Date(b.at).toLocaleString()}`}
            />
          )
        })}
      </div>
      <div className="security-axis muted small">
        <span>{new Date(buckets[0].at).toLocaleTimeString()}</span>
        <span>{t('최대 {n}', { n: peak.toLocaleString() })}</span>
        <span>{t('지금')}</span>
      </div>
    </div>
  )
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
  const attackers = view.attackers ?? []
  const clusters = view.clusters ?? []
  return (
    <section className="panel">
      {/* The figures live in the panel grid above. This is the part a grid
          cannot hold: who is on the list, and who is not on it yet. */}
      <h3>{t('차단 목록')}</h3>

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
              {t('{cidr} — {hosts}대 · {n}회', { cidr: c.cidr, hosts: c.hosts, n: c.count })}
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
