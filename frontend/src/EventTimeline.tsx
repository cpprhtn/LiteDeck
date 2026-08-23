import { useCallback, useEffect, useState } from 'react'
import { HostEvents, type EventsView, type ServerEvent } from './ipc'
import { k, t } from './i18n'

// The event timeline (§4.7) — see arch/07.
//
// Not a log viewer. There is already one, and pouring `journalctl -p warning`
// onto the screen would be a second one with a worse filter. What earns a row
// here is the handful of things that explain "the service died overnight":
// the OOM killer, a unit that failed, a process that dumped core, a restart
// that keeps being scheduled.
//
// One read per open, not a poll. The past does not change.

/** Oldest first is wrong here: the question is what happened, and the answer is
 *  usually the most recent thing. The backend already sorts newest-first. */
const RANGES: { id: '1h' | '24h' | '7d'; label: string }[] = [
  { id: '1h', label: k('1시간') },
  { id: '24h', label: k('24시간') },
  { id: '7d', label: k('7일') },
]

/** Severity of the row, which decides its colour. Kind first, because an OOM
 *  kill logged at notice is still an OOM kill. */
function tone(e: ServerEvent): 'bad' | 'warn' | 'plain' {
  switch (e.kind) {
    case 'oom':
    case 'unit-failed':
    case 'start-failed':
    case 'coredump':
      return 'bad'
    case 'restart':
      return 'warn'
    case 'boot':
    case 'shutdown':
      return 'plain'
  }
  return e.severity <= 3 ? 'bad' : e.severity <= 4 ? 'warn' : 'plain'
}

const KIND_LABEL: Record<ServerEvent['kind'], string> = {
  oom: 'OOM',
  'unit-failed': k('유닛 실패'),
  'start-failed': k('시작 실패'),
  coredump: k('코어 덤프'),
  restart: k('재시작 예약'),
  boot: k('부팅'),
  shutdown: k('종료'),
  session: k('세션 시작'),
  other: '',
}

export function EventTimeline({
  hostID,
  onError,
  onOpenService,
}: {
  hostID: string
  onError: (msg: string) => void
  /** Jump to this unit's log. The timeline says what happened; the log says why. */
  onOpenService?: (unit: string) => void
}) {
  const [range, setRange] = useState<'1h' | '24h' | '7d'>('24h')
  const [view, setView] = useState<EventsView | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(
    async (elevate: boolean) => {
      setBusy(true)
      try {
        setView(await HostEvents(hostID, range, elevate))
      } catch (e) {
        onError(String(e))
      } finally {
        setBusy(false)
      }
    },
    [hostID, range, onError],
  )

  useEffect(() => {
    void load(false)
  }, [load])

  return (
    <div className="events-pane">
      <div className="events-bar">
        <div className="seg">
          {RANGES.map((r) => (
            <button
              key={r.id}
              className="ghost"
              data-on={r.id === range || undefined}
              onClick={() => setRange(r.id)}
            >
              {t(r.label)}
            </button>
          ))}
        </div>
        <span className="grow" />
        <button className="ghost" disabled={busy} onClick={() => void load(false)}>
          {t('다시 읽기')}
        </button>
      </div>

      {view && <AccessNotice view={view} busy={busy} onElevate={() => void load(true)} />}

      {view?.truncated && (
        <div className="muted small events-note">
          {t('가장 오래된 줄이 이 기간의 시작이 아닙니다 — 한 번에 읽는 최대 줄 수에 걸렸습니다.')}
        </div>
      )}

      <div className="events-list">
        {view?.access === 'ok' && view.events.length === 0 && (
          <div className="placeholder small">
            {t('이 기간에는 기록할 만한 사건이 없었습니다.')}
          </div>
        )}
        {view?.events.map((e, i) => {
          // A reboot between two rows is the one piece of context that makes
          // "it stopped answering" and "it was restarted" tell themselves apart.
          const rebooted = i > 0 && e.bootId !== view.events[i - 1].bootId
          return (
            <div key={`${e.at}-${i}`}>
              {rebooted && (
                <div className="events-boot">
                  <span>{t('재부팅')}</span>
                </div>
              )}
              <div className="events-row" data-tone={tone(e)}>
                <span className="mono small events-at">{when(e.at)}</span>
                {KIND_LABEL[e.kind] && (
                  <span className="events-kind">{t(KIND_LABEL[e.kind])}</span>
                )}
                {e.unit &&
                  (onOpenService ? (
                    <button
                      className="ghost events-unit ellipsis"
                      title={t('이 서비스의 로그 보기')}
                      onClick={() => onOpenService(e.unit!)}
                    >
                      {e.unit}
                    </button>
                  ) : (
                    <span className="events-unit ellipsis">{e.unit}</span>
                  ))}
                <span className="events-msg">{e.message}</span>
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

/** Emptiness always arrives labelled. An unreadable journal and a quiet server
 *  look identical on screen, and they need opposite things said about them. */
function AccessNotice({
  view,
  busy,
  onElevate,
}: {
  view: EventsView
  busy: boolean
  onElevate: () => void
}) {
  if (view.access === 'ok') return null

  if (view.access === 'needs-sudo') {
    return (
      <div className="events-notice">
        <p>
          {t('이 사용자는 systemd-journal·adm 그룹에 없어 시스템 저널이 비어 보입니다. 사건이 없는 것이 아니라 보이지 않는 것입니다.')}
        </p>
        <div className="row">
          <button className="primary" disabled={busy} onClick={onElevate}>
            {t('관리자 권한으로 읽기')}
          </button>
          <span className="muted small">
            {t('서버에서 그룹에 추가하면 매번 묻지 않아도 됩니다.')}
          </span>
        </div>
      </div>
    )
  }

  if (view.access === 'denied') {
    return (
      <div className="events-notice">
        <p>
          {t('이 사용자는 시스템 저널을 읽을 수 없고 sudo 도 없습니다. 목록이 비어 있는 것은 사건이 없어서가 아닙니다.')}
        </p>
        <p className="muted small">
          {t('서버에서 이 계정을 systemd-journal 또는 adm 그룹에 추가하세요.')}
        </p>
      </div>
    )
  }

  return (
    <div className="events-notice">
      <p>{t('이 서버에는 systemd 저널이 없습니다.')}</p>
    </div>
  )
}

/** Local time, seconds included — "at 03:14" is the whole point of the row. */
function when(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}
