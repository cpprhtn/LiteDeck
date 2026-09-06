import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  HostCommandHistory,
  HostShellHistory,
  SetShellHistoryAllowed,
  TypedHistory,
  type CommandHistoryView,
  type ShellCommand,
  type ShellHistoryView,
  type SudoRun,
  type TypedCommand,
} from './ipc'
import { AccessNotice } from './EventTimeline'
import { k, t } from './i18n'

// Command history (arch/07, 명령 이력).
//
// "I was on this box three months ago and I cannot remember what I did." The
// thing people actually do is scroll their shell history looking for the lines
// that changed something, and this is that, with the part the file cannot give
// them: where each command ran. sudo writes the directory down as it runs, so
// the grouping below is a record rather than a reconstruction.
//
// Two sources, shown together and marked apart. sudo's journal (C-2) knows the
// directory as a fact. This app's own terminal (B) knows it only as far as it
// could read the lines: a history recall or a Tab completion is a line nobody
// on this side saw, and it may have been a `cd`. Those rows say so — dimmed,
// with a question mark — rather than being dropped or, worse, shown as
// confidently as the ones that are certain.
//
// Not a log viewer and not a terminal. Nothing here executes — clicking a row
// copies it, and the person decides whether to run it. The history contains
// `rm -rf`, and a list that runs things on click is a list nobody can safely
// scroll.

const RANGES: { id: '1h' | '24h' | '7d'; label: string }[] = [
  { id: '1h', label: k('1시간') },
  { id: '24h', label: k('24시간') },
  { id: '7d', label: k('7일') },
]

const EFFECT_MARK: Record<SudoRun['effect'], string> = {
  change: '●',
  edit: '✎',
  read: '·',
}

/** One row, whichever source it came from.
 *
 *  The two are kept apart on screen rather than merged into an average. sudo's
 *  journal knows the directory as a fact; the terminal log knows it only as far
 *  as it could read the lines. Showing both as the same kind of thing would
 *  make the weaker one look like the stronger one. */
interface Row {
  at: string
  pwd: string
  /** False where the path is where the shell *was*, not necessarily where it
   *  is — see TypedCommand. sudo's rows are always true. */
  certain: boolean
  command: string
  effect: SudoRun['effect']
  source: 'sudo' | 'typed' | 'shell'
  /** Absent where the source has no times at all — the bash history default. */
  timed: boolean
  refused?: boolean
  reason?: string
}

function fromSudo(r: SudoRun): Row {
  return {
    at: r.at, pwd: r.pwd || '?', certain: true, command: r.command,
    effect: r.effect, source: 'sudo', refused: r.refused, reason: r.reason, timed: true,
  }
}

function fromTyped(c: TypedCommand): Row {
  return {
    at: c.at, pwd: c.pwd || '?', certain: c.pwdCertain, command: c.command,
    effect: c.effect, source: 'typed', timed: true,
  }
}

/** The shell file's path is always an estimate — see ShellCommand. Never
 *  `certain`, whatever the replay produced. */
function fromShell(c: ShellCommand): Row {
  return {
    at: c.at ?? '', pwd: c.pwd || '?', certain: false, command: c.command,
    effect: c.effect, source: 'shell', timed: !!c.at,
  }
}

/** Rough and local. The exact minute is in the title attribute; the list is
 *  read for "when roughly", and "3개월 전" answers that faster than a date. */
function ago(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return ''
  const mins = Math.floor((Date.now() - then) / 60000)
  if (mins < 1) return t('방금')
  if (mins < 60) return t('{n}분 전', { n: mins })
  const hours = Math.floor(mins / 60)
  if (hours < 24) return t('{n}시간 전', { n: hours })
  const days = Math.floor(hours / 24)
  if (days < 31) return t('{n}일 전', { n: days })
  return t('{n}개월 전', { n: Math.floor(days / 30) })
}

interface Group {
  pwd: string
  runs: Row[]
  /** The newest run in the group, which is what the list is ordered by. */
  latest: string
  /** True when every row in it knows where it was. One uncertain row is enough
   *  to mark the whole group, because the group *is* the claim about location. */
  certain: boolean
}

/** Grouped by directory, most recently used first.
 *
 *  Not alphabetical: the question starts with "I was here a while ago", so the
 *  place worked in last is the place to show first. */
function groupByPath(runs: Row[]): Group[] {
  const byPath = new Map<string, Row[]>()
  for (const r of runs) {
    const key = r.pwd || '?'
    const list = byPath.get(key)
    if (list) list.push(r)
    else byPath.set(key, [r])
  }
  const groups: Group[] = []
  for (const [pwd, list] of byPath) {
    groups.push({
      pwd,
      runs: list,
      latest: list[0]?.at ?? '',
      certain: list.every((r) => r.certain),
    })
  }
  groups.sort((a, b) => (a.latest < b.latest ? 1 : a.latest > b.latest ? -1 : 0))
  return groups
}

export function CommandHistory({
  hostID,
  onError,
}: {
  hostID: string
  onError: (msg: string) => void
}) {
  const [view, setView] = useState<CommandHistoryView | null>(null)
  const [typed, setTyped] = useState<TypedCommand[]>([])
  const [shell, setShell] = useState<ShellHistoryView | null>(null)
  const [range, setRange] = useState<'1h' | '24h' | '7d'>('24h')
  const [busy, setBusy] = useState(false)
  // Reads are folded away by default. Roughly eight lines in ten are somebody
  // looking rather than doing, and the question is what changed — that one
  // filter is most of what this pane is for.
  const [changesOnly, setChangesOnly] = useState(true)
  const [open, setOpen] = useState<Set<string>>(new Set())
  const [copied, setCopied] = useState<string | null>(null)

  const load = useCallback(
    async (elevate: boolean) => {
      setBusy(true)
      try {
        // Read together. The local one cannot fail in a way worth reporting —
        // it is a file this app wrote — so a rejection there must not hide the
        // journal's answer.
        const [remote, local, file] = await Promise.all([
          HostCommandHistory(hostID, range, elevate),
          TypedHistory(hostID).catch(() => [] as TypedCommand[]),
          HostShellHistory(hostID, elevate).catch(() => null),
        ])
        setView(remote)
        setTyped(local)
        setShell(file)
      } catch (e) {
        onError(String(e))
      } finally {
        setBusy(false)
      }
    },
    [hostID, range, onError],
  )

  // One read per open or range change. The past does not change, so there is
  // nothing here for a poller to find.
  useEffect(() => {
    void load(false)
  }, [load])

  const groups = useMemo(() => {
    const rows = [
      ...(view?.runs ?? []).map(fromSudo),
      ...typed.map(fromTyped),
      ...(shell?.commands ?? []).map(fromShell),
    ]
    // Timed rows sort by time. Untimed ones cannot, so they keep the order the
    // file gave them and fall in behind — inventing a position for them would
    // be inventing a time.
    rows.sort((a, b) => {
      if (a.timed !== b.timed) return a.timed ? -1 : 1
      if (!a.timed) return 0
      return a.at < b.at ? 1 : a.at > b.at ? -1 : 0
    })
    return groupByPath(changesOnly ? rows.filter((r) => r.effect !== 'read') : rows)
  }, [view, typed, shell, changesOnly])

  const copy = (r: Row) => {
    void navigator.clipboard?.writeText(r.command).catch(() => {})
    setCopied(r.at + r.command)
    setTimeout(() => setCopied(null), 1200)
  }

  const toggle = (pwd: string) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (!next.delete(pwd)) next.add(pwd)
      return next
    })

  return (
    <div className="history-pane">
      <div className="history-bar">
        <div className="segmented">
          {RANGES.map((r) => (
            <button key={r.id} data-on={range === r.id || undefined} onClick={() => setRange(r.id)}>
              {t(r.label)}
            </button>
          ))}
        </div>
        <label className="history-toggle">
          <input
            type="checkbox"
            checked={changesOnly}
            onChange={(e) => setChangesOnly(e.target.checked)}
          />
          <span className="small">{t('바꾼 것만')}</span>
        </label>
        <span className="spacer" />
        {busy && <span className="muted small">{t('읽는 중…')}</span>}
      </div>

      {view && <AccessNotice view={view} busy={busy} onElevate={() => void load(true)} />}

      {/* Off until asked for. An empty list here would read as "nobody has ever
          worked on this server", which is the opposite of the truth — the file
          is sitting there unread. */}
      {shell && !shell.allowed && (
        <div className="history-optin">
          <p className="small">
            {t('이 서버의 셸 이력 파일은 아직 읽지 않습니다. 켜면 지난 명령을 경로별로 볼 수 있습니다.')}
          </p>
          <p className="muted small">
            {t('셸 이력은 서버에서 자격증명이 가장 많이 들어 있는 파일입니다. 비밀번호처럼 보이는 것은 가려서 보여주지만, 켜기 전에 알고 계셔야 합니다.')}
          </p>
          <button
            className="primary"
            disabled={busy}
            onClick={() => {
              void SetShellHistoryAllowed(hostID, true)
                .then(() => load(false))
                .catch(() => {})
            }}
          >
            {t('이 서버에서 켜기')}
          </button>
        </div>
      )}

      {/* Said out loud, not printed. Somebody whose history has passwords in it
          wants to know that; nobody wants them on screen to find out. */}
      {(view?.secrets ?? 0) + (shell?.secrets ?? 0) > 0 && (
        <p className="history-secrets small">
          {t('비밀번호나 토큰으로 보이는 명령 {n}건을 가렸습니다.', {
            n: (view?.secrets ?? 0) + (shell?.secrets ?? 0),
          })}
        </p>
      )}

      {groups.length === 0 && !busy && (
        <p className="muted small history-empty">
          {changesOnly
            ? t('이 기간에 바꾼 것이 없습니다. 조회까지 보려면 「바꾼 것만」 을 끄세요.')
            : t('이 기간에 기록된 명령이 없습니다.')}
        </p>
      )}

      <div className="history-list">
        {groups.map((g) => {
          const expanded = open.has(g.pwd)
          return (
            <div key={g.pwd} className="history-group">
              <button className="history-head" onClick={() => toggle(g.pwd)}>
                <span className="history-twisty">{expanded ? '▾' : '▸'}</span>
                {/* An estimate is shown as one. The mark is the same idea as
                    breaking a chart line across a gap: the shape stays useful
                    and the claim stays true. */}
                <span
                  className="mono history-path"
                  data-guess={!g.certain || undefined}
                  title={g.certain ? g.pwd : t('추정 경로 — 읽지 못한 줄이 지나갔습니다')}
                >
                  {g.pwd}
                  {!g.certain && <span className="history-guess">?</span>}
                </span>
                <span className="muted small history-count">
                  {t('{n}회', { n: g.runs.length })}
                </span>
                <span className="muted small">{ago(g.latest)}</span>
              </button>

              {expanded && (
                <div className="history-runs">
                  {g.runs.map((r, i) => (
                    <button
                      key={`${r.at}-${i}`}
                      className="history-run"
                      data-effect={r.effect}
                      data-refused={r.refused || undefined}
                      data-source={r.source}
                      onClick={() => copy(r)}
                      title={`${new Date(r.at).toLocaleString()} · ${
                        r.source === 'sudo' ? t('sudo 저널') : t('이 앱의 터미널')
                      }`}
                    >
                      {/* The mark says what it did; which source knew about it
                          is in the title and in the dot's shade. Two symbols in
                          one column would be a legend to learn. */}
                      <span className="history-mark">{EFFECT_MARK[r.effect]}</span>
                      <span className="mono history-cmd">{r.command}</span>
                      {/* A refusal is part of what happened. On a host that always
                          asks for a password it is most of the file, and reading
                          it as "this ran" would be wrong. */}
                      {r.refused && (
                        <span className="history-refused small" title={r.reason}>
                          {t('실행 안 됨')}
                        </span>
                      )}
                      <span className="muted small history-when">{ago(r.at)}</span>
                      {copied === r.at + r.command && (
                        <span className="small history-copied">{t('복사됨')}</span>
                      )}
                    </button>
                  ))}
                </div>
              )}
            </div>
          )
        })}
      </div>

      {view?.truncated && (
        <p className="muted small history-empty">
          {t('읽기 한도에 걸렸습니다 — 이보다 오래된 명령이 더 있습니다.')}
        </p>
      )}
    </div>
  )
}
