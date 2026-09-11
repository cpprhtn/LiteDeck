import { useState } from 'react'
import { shortStamp } from './datetime'
import { FailureChart } from './SecurityView'
import { t } from './i18n'
import type {
  Attacker,
  Listener,
  WindowsFirewallProfile,
  WindowsSecurity,
} from './ipc'

// The security screen on a Windows host.
//
// A separate screen rather than the Linux one with the missing parts blanked
// out. Windows has a firewall and none of the other four things that screen is
// built around: no fail2ban, no jail, no ban list, and no dropped-packet count
// unless somebody switched firewall logging on. Four empty panels read as a
// broken page, not as "this platform does not work that way".
//
// The four questions are the same, and only the first has the same answer:
//
//	is anything in front?      three firewall profiles, one of them live
//	what is open?              inbound allow rules that name a port
//	what stops guessing?       account lockout — and it locks accounts, not
//	                           addresses, which is the thing to say out loud
//	who is trying?             the OpenSSH log, same as the Linux journal

export function WindowsSecurityView({
  win,
  listening,
}: {
  win: WindowsSecurity
  listening: Listener[]
}) {
  // Which of the allowed ports something is actually behind. A rule with
  // nothing listening is not dangerous today; it is a door that opens the
  // moment something binds to that number.
  const live = new Set(listening.filter((l) => l.exposed).map((l) => l.port))

  return (
    <>
      <FirewallProfiles win={win} />
      <LoginDefence win={win} />
      <OpenPorts win={win} live={live} />
      <Knocking win={win} />
    </>
  )
}

function FirewallProfiles({ win }: { win: WindowsSecurity }) {
  if (!win.profilesRead) {
    return (
      <div className="events-notice">
        <p>{t('방화벽 상태를 읽지 못했습니다.')}</p>
        <p className="muted small">
          {t('Windows에는 sudo가 없습니다. 이 계정으로는 읽을 수 없으니, 방화벽을 볼 수 있는 계정으로 접속해야 합니다.')}
        </p>
      </div>
    )
  }

  const profiles = win.profiles ?? []
  const off = profiles.filter((p) => !p.enabled)
  const openInbound = profiles.filter((p) => p.enabled && !p.inboundBlocked)

  return (
    <section className="win-sec-block">
      <h3>{t('방화벽')}</h3>
      <div className="win-profiles">
        {profiles.map((p) => (
          <ProfileTile key={p.name} p={p} />
        ))}
      </div>
      {off.length > 0 && (
        <p className="win-sec-warn">
          {t('{names} 프로필이 꺼져 있습니다.', { names: off.map((p) => p.name).join(', ') })}
        </p>
      )}
      {openInbound.length > 0 && (
        <p className="win-sec-warn">
          {t('{names} 프로필이 들어오는 연결을 기본 허용으로 두고 있습니다. 누군가 그렇게 바꾼 것입니다.', {
            names: openInbound.map((p) => p.name).join(', '),
          })}
        </p>
      )}
    </section>
  )
}

function ProfileTile({ p }: { p: WindowsFirewallProfile }) {
  return (
    <div className="win-profile" data-on={p.enabled || undefined} data-active={p.active || undefined}>
      <div className="win-profile-name">
        {p.name}
        {/* Two of the three are switched on and deciding nothing: only the
            category the live network is in has any effect right now. */}
        {p.active && <span className="badge">{t('이 네트워크')}</span>}
      </div>
      <div className="win-profile-state">{p.enabled ? t('켜짐') : t('꺼짐')}</div>
      <div className="muted small">
        {p.inboundBlocked
          ? p.inboundExplicit
            ? t('들어오는 연결 차단')
            : /* "NotConfigured" is the stock state and means the Windows
                 default, which is block. Saying "not set" instead would leave
                 the reader to guess which way it falls. */
              t('들어오는 연결 차단 (기본값)')
          : t('들어오는 연결 허용')}
      </div>
    </div>
  )
}

/** What happens when somebody guesses passwords — the fail2ban-shaped hole.
 *
 *  Account lockout is the only thing a stock Windows box does about repeated
 *  failures, and it is weaker than fail2ban in a way the screen has to say:
 *  it locks the account, not the address. The measured server was taking 712
 *  failures across 39 different account names in 77 minutes, and lockout does
 *  nothing at all about that shape of attack. */
function LoginDefence({ win }: { win: WindowsSecurity }) {
  const lock = win.lockout
  return (
    <section className="win-sec-block">
      <h3>{t('로그인 방어')}</h3>
      {!lock ? (
        <p className="muted small">{t('계정 잠금 정책을 읽지 못했습니다.')}</p>
      ) : lock.threshold === 0 ? (
        <p className="win-sec-warn">
          {t('계정 잠금이 꺼져 있습니다. 비밀번호를 몇 번 틀리든 아무 일도 일어나지 않습니다.')}
        </p>
      ) : (
        <>
          <p className="small">
            {t('{n}번 틀리면 {d}분 동안 계정이 잠깁니다. 실패 횟수는 {w}분 뒤 초기화됩니다.', {
              n: lock.threshold,
              d: lock.duration,
              w: lock.window,
            })}
          </p>
          <p className="muted small">
            {t('잠기는 것은 계정이지 주소가 아닙니다. 계정 이름을 바꿔 가며 시도하는 공격은 이 정책에 걸리지 않습니다.')}
          </p>
        </>
      )}
      {win.defender && (
        <p className="muted small">
          {win.defender.realTime
            ? t('Defender 실시간 보호 켜짐 · 정의 파일 {n}일 전', { n: win.defender.signatureAge })
            : t('Defender 실시간 보호가 꺼져 있습니다.')}
        </p>
      )}
    </section>
  )
}

function OpenPorts({ win, live }: { win: WindowsSecurity; live: Set<string> }) {
  if (!win.rulesRead) {
    return (
      <section className="win-sec-block">
        <h3>{t('열린 포트')}</h3>
        <p className="muted small">{t('방화벽 규칙을 읽지 못했습니다.')}</p>
      </section>
    )
  }
  // Ordered by whether the door leads anywhere. A stock Windows 10 carries 56
  // rules that open a port and most of them are its own plumbing — network
  // discovery, DHCP, delivery optimisation. The two somebody is looking for on
  // a server, SSH and RDP, were at rows 30 and 45 in name order. A port with
  // something bound to it is the one worth reading first; the rest are a door
  // that opens the moment something binds to that number, which is a smaller
  // worry and belongs lower down.
  const rules = [...(win.rules ?? [])].sort((a, b) => {
    const la = live.has(a.port) ? 0 : 1
    const lb = live.has(b.port) ? 0 : 1
    if (la !== lb) return la - lb
    return Number(a.port) - Number(b.port)
  })
  return (
    <section className="win-sec-block">
      <h3>
        {t('열린 포트')}{' '}
        <span className="muted small">
          {rules.length < win.ruleTotal
            ? t('{shown}/{total}', { shown: rules.length, total: win.ruleTotal })
            : t('{n}개', { n: rules.length })}
        </span>
      </h3>
      {rules.length === 0 ? (
        <p className="muted small">{t('포트를 여는 인바운드 규칙이 없습니다.')}</p>
      ) : (
        <div className="table win-rules">
          <div className="thead">
            <div>{t('포트')}</div>
            <div>{t('프로필')}</div>
            <div>{t('규칙')}</div>
            <div>{t('듣고 있음')}</div>
          </div>
          <div className="tbody">
            {rules.map((r, i) => (
              <div key={`${r.protocol}-${r.port}-${i}`} className="trow">
                <div className="mono">
                  {r.protocol}/{r.port}
                </div>
                <div className="muted small">{r.profile}</div>
                <div className="ellipsis small" title={r.name}>
                  {r.name}
                </div>
                {/* A port allowed with nothing behind it is a door that opens
                    the moment something binds to that number. */}
                <div className="small muted">{live.has(r.port) ? t('예') : '—'}</div>
              </div>
            ))}
          </div>
        </div>
      )}
    </section>
  )
}

function Knocking({ win }: { win: WindowsSecurity }) {
  if (!win.hasLog) {
    return (
      <section className="win-sec-block">
        <h3>{t('접속 시도')}</h3>
        <p>{t('OpenSSH 로그가 비어 있습니다. 지금 이 연결도 SSH 이므로, 기록이 없는 것이 아니라 로그가 꺼져 있거나 지워진 것입니다.')}</p>
      </section>
    )
  }
  const failures = win.failures ?? []
  const attackers = win.attackers ?? []
  const clusters = win.clusters ?? []

  return (
    <section className="win-sec-block">
      <h3>
        {t('접속 시도')}{' '}
        <span className="muted small">
          {/* The log is circular and 1 MB. On the measured server that was 77
              minutes, and a chart labelled "24 hours" over it would make an
              attack that has run all day look like it had just started. */}
          {win.logSince
            ? t('{t} 이후 — 로그가 그 앞을 덮어썼습니다', { t: shortStamp(new Date(win.logSince)) })
            : t('최근 24시간')}
        </span>
      </h3>
      <p className="small">
        {win.failed > 0
          ? t('실패 {n}건', { n: win.failed })
          : t('실패한 로그인이 없습니다.')}
      </p>
      {/* Two bars is a direction, one bar is a rectangle. The Linux screen
          always has a day of journal behind it; here the log may hold an hour. */}
      {failures.length > 1 && <FailureChart buckets={failures} bans={[]} />}
      {attackers.length > 0 && (
        <>
          <p className="muted small">{t('아직 아무것도 막고 있지 않은 주소')}</p>
          {attackers.map((a) => (
            <WindowsAttacker key={a.address} attacker={a} />
          ))}
        </>
      )}
      {clusters.length > 0 && (
        <p className="security-cluster small">
          {clusters.map((c) => t('{cidr} 에서 {n}개 주소', { cidr: c.cidr, n: c.hosts })).join(' · ')}
        </p>
      )}
      {win.blockedRemote && win.blockedRemote.length > 0 && (
        <p className="muted small">
          {t('차단 규칙이 막고 있는 주소 {n}개', { n: win.blockedRemote.length })}
        </p>
      )}
    </section>
  )
}

/** One attacker, and the command that would block it.
 *
 *  Copied rather than run. The Linux screen offers an nft line for the same
 *  reason: a rule written from this app is a rule nobody reviewed, and the one
 *  thing a firewall rule can do is cut off the person writing it. */
function WindowsAttacker({ attacker }: { attacker: Attacker }) {
  const [copied, setCopied] = useState(false)
  const cmd =
    `New-NetFirewallRule -DisplayName "LiteDeck block ${attacker.address}" ` +
    `-Direction Inbound -Action Block -RemoteAddress ${attacker.address}`
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
