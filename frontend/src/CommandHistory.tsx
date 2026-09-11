import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { stamp } from './datetime'
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


// One control, not two.
//
// There used to be a time window (24시간 · 7일 · 최대) beside these, and the two
// were asking the same question twice — 최근 작업 and 24시간 are both "recently".
// Worse, the window barely worked: bash writes no timestamps unless
// HISTTIMEFORMAT is set, so most rows could not be judged by it and the panel
// carried a line apologising for that. A control that needs an apology is the
// wrong control. Recency is a count here, which is the one thing an untimed
// file can still be ordered by, and each card says when it was last touched.
const TABS: { id: HistoryTab; label: string }[] = [
  { id: 'recent', label: k('최근 작업') },
  { id: 'all', label: k('전체') },
  { id: 'raw', label: k('원본') },
]

type HistoryTab = 'recent' | 'all' | 'raw'

/** How many folders the recent tab shows before offering the rest. */
const RECENT_FOLDERS = 8

/** How many commands a folder card shows folded. Four is what fits without the
 *  card becoming a list of its own. */
const PREVIEW_COMMANDS = 4

/** A directory, its commands, and how much of its name was worked out. */
interface Folder {
  /** The resolved path, or the relative fragment when nothing anchored it. */
  path: string
  /** False for a fragment — `src` rather than `/srv/app/src`. */
  anchored: boolean
  /** True when nothing in this folder came from a replay that had lost track. */
  certain: boolean
  /** Newest first. */
  rows: HistoryRow[]
  /** Newest timestamp here, or '' when the file carried none. */
  latest: string
  /** Position of the newest row in the merged list. Lower is more recent, and
   *  it is what orders folders when there are no times to order by. */
  rank: number
}

/** Groups the merged rows by the directory they ran in.
 *
 *  Flat, not a tree. A tree has to invent the levels between two directories,
 *  and on a history where the paths are themselves worked out that meant the
 *  inference — `demo/LetsJandi/LetsJandi/LetsJandi` — became the loudest thing
 *  on screen. The question is "which folder, and what did I run there", and a
 *  folder is the answer to it whether or not its parent is also in the list. */
function groupByFolder(rows: HistoryRow[], changesOnly: boolean): Folder[] {
  const by = new Map<string, Folder>()
  rows.forEach((r, index) => {
    if (changesOnly && r.effect === 'read') return
    const path = r.pwd || '?'
    let f = by.get(path)
    if (!f) {
      f = {
        path,
        anchored: path.startsWith('/'),
        certain: true,
        rows: [],
        latest: '',
        rank: index,
      }
      by.set(path, f)
    }
    f.rows.push(r)
    if (!r.certain) f.certain = false
    if (r.timed && r.at > f.latest) f.latest = r.at
  })
  const out = [...by.values()]
  for (const f of out) {
    f.rows.sort((a, b) => {
      if (a.timed !== b.timed) return a.timed ? -1 : 1
      if (!a.timed) return 0
      return a.at < b.at ? 1 : a.at > b.at ? -1 : 0
    })
  }
  // Most recently worked in first. Dated folders sort by their newest command;
  // undated ones keep the order the file gave them, which is the only recency
  // an untimed history actually carries.
  return out.sort((a, b) => {
    if (!!a.latest !== !!b.latest) return a.latest ? -1 : 1
    if (a.latest && b.latest) return a.latest < b.latest ? 1 : a.latest > b.latest ? -1 : 0
    return a.rank - b.rank
  })
}

/** Whether a line only moved the shell.
 *
 *  `cd monitoring/vector` is not something anybody looks back for — the tree
 *  above already says they went there, and listing the walking with the work
 *  buries the work. Only a line that is *nothing but* a move is dropped:
 *  `cd build && make` did something and stays. */
function isNavigation(command: string): boolean {
  return /^(cd|pushd|popd)(\s+[^&|;]*)?$/.test(command.trim())
}

const EFFECT_MARK: Record<SudoRun['effect'], string> = {
  change: '●',
  edit: '✎',
  read: '·',
}

/** One row of history, whichever source it came from. */
export interface HistoryRow {
  at: string
  pwd: string
  certain: boolean
  command: string
  effect: 'change' | 'edit' | 'read'
  source: 'sudo' | 'typed' | 'shell'
  timed: boolean
  refused?: boolean
  reason?: string
}

/** One row, whichever source it came from.
 *
 *  The three are kept apart on screen rather than merged into an average.
 *  sudo's journal knows the directory as a fact; the other two know it as far
 *  as they could follow. Showing them all alike would make the weaker ones look
 *  like the stronger one. */
type Row = HistoryRow

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

/** The replay says per line whether it kept track. It followed every `cd` in
 *  the measured file, so most rows here are as good as the other sources' — and
 *  the ones after a `cd $VAR` say otherwise. */
function fromShell(c: ShellCommand): Row {
  return {
    at: c.at ?? '', pwd: c.pwd || '?', certain: c.pwdCertain, command: c.command,
    effect: c.effect, source: 'shell', timed: !!c.at,
  }
}

/** Rough and local. The exact minute is in the title attribute; the list is
 *  read for "when roughly", and "3개월 전" answers that faster than a date. */
function ago(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return ''
  // A date before this app could have seen anything is not a date, it is a
  // zero value that reached the screen. Rendering it gave "24662 months ago".
  if (then < Date.parse('2000-01-01')) return ''
  const mins = Math.floor((Date.now() - then) / 60000)
  if (mins < 1) return t('방금')
  if (mins < 60) return t('{n}분 전', { n: mins })
  const hours = Math.floor(mins / 60)
  if (hours < 24) return t('{n}시간 전', { n: hours })
  const days = Math.floor(hours / 24)
  if (days < 31) return t('{n}일 전', { n: days })
  return t('{n}개월 전', { n: Math.floor(days / 30) })
}

export function CommandHistory({
  hostID,
  cwd,
  onError,
  onGoTo,
}: {
  hostID: string
  /** Where the terminal beside this panel is standing, when it is known. */
  cwd?: string
  onError: (msg: string) => void
  /** Send the terminal to a folder. Absent when there is no terminal to send. */
  onGoTo?: (path: string) => void
}) {
  const [view, setView] = useState<CommandHistoryView | null>(null)
  const [typed, setTyped] = useState<TypedCommand[]>([])
  const [shell, setShell] = useState<ShellHistoryView | null>(null)
  const [tab, setTab] = useState<HistoryTab>('recent')
  const [query, setQuery] = useState('')
  const [busy, setBusy] = useState(false)
  const [changesOnly, setChangesOnly] = useState(true)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [copied, setCopied] = useState<string | null>(null)

  const load = useCallback(
    async (elevate: boolean) => {
      setBusy(true)
      try {
        // Read together. The local one cannot fail in a way worth reporting —
        // it is a file this app wrote — so a rejection there must not hide the
        // journal's answer.
        const [remote, local, file] = await Promise.all([
          // No window: the journal's own 500-line cap is the bound, the same
          // way the shell history's is HISTFILESIZE.
          HostCommandHistory(hostID, 'max', elevate),
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
    [hostID, onError],
  )

  // One read per open. The past does not change, so there is nothing here for a
  // poller to find.
  useEffect(() => {
    void load(false)
  }, [load])

  /** Everything, in the order the sources gave it. The raw tab shows this and
   *  nothing else: every other view here is a reading of the history, and the
   *  point of that one is to be the history somebody checks the reading
   *  against. */
  const allRows = useMemo(
    () => [
      ...(view?.runs ?? []).map(fromSudo),
      ...typed.map(fromTyped),
      ...(shell?.commands ?? []).map(fromShell),
    ],
    [view, typed, shell],
  )

  const rows = useMemo(() => allRows.filter((r) => !isNavigation(r.command)), [allRows])

  const folders = useMemo(() => groupByFolder(rows, changesOnly), [rows, changesOnly])

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase()
    const matched = q
      ? folders
          .map((f) => ({
            ...f,
            rows: f.rows.filter((r) => r.command.toLowerCase().includes(q)),
            hit: f.path.toLowerCase().includes(q),
          }))
          .filter((f) => f.hit || f.rows.length > 0)
      : folders
    return tab === 'recent' ? matched.slice(0, RECENT_FOLDERS) : matched
  }, [folders, query, tab])

  const sure = folders.filter((f) => f.certain).length

  const copy = (text: string, key: string) => {
    void navigator.clipboard?.writeText(text).catch(() => {})
    setCopied(key)
    setTimeout(() => setCopied(null), 1200)
  }

  return (
    <div className="history-pane">
      <div className="history-bar">
        <div className="segmented">
          {TABS.map((x) => (
            <button key={x.id} data-on={tab === x.id || undefined} onClick={() => setTab(x.id)}>
              {t(x.label)}
            </button>
          ))}
        </div>
        <span className="spacer" />
        {busy ? (
          <span className="muted small">{t('읽는 중…')}</span>
        ) : (
          folders.length > 0 && (
            <span className="muted small history-tally">
              {t('정확 {n}', { n: sure })} · {t('추정 {n}', { n: folders.length - sure })}
            </span>
          )
        )}
      </div>

      {tab !== 'raw' && (
        <>
          <div className="history-search">
            <input
              className="search"
              value={query}
              placeholder={t('폴더 또는 명령 검색…')}
              onChange={(e) => setQuery(e.target.value)}
            />
            <label className="history-toggle">
              <input
                type="checkbox"
                checked={changesOnly}
                onChange={(e) => setChangesOnly(e.target.checked)}
              />
              <span className="small">{t('바꾼 것만')}</span>
            </label>
          </div>
        </>
      )}

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

      {tab === 'raw' ? (
        <RawHistory rows={allRows} copied={copied} onCopy={copy} />
      ) : (
        <div className="history-folders">
          {shown.length === 0 && !busy && (
            <p className="muted small history-empty">
              {query.trim()
                ? t('검색과 맞는 것이 없습니다.')
                : changesOnly
                  ? t('이 기간에 바꾼 것이 없습니다. 조회까지 보려면 「바꾼 것만」을 끄세요.')
                  : t('이 기간에 기록된 명령이 없습니다.')}
            </p>
          )}
          {shown.map((f) => (
            <FolderCard
              key={f.path}
              folder={f}
              here={cwd === f.path}
              open={expanded.has(f.path)}
              copied={copied}
              onToggle={() =>
                setExpanded((prev) => {
                  const next = new Set(prev)
                  if (!next.delete(f.path)) next.add(f.path)
                  return next
                })
              }
              onGoTo={onGoTo}
              onCopy={copy}
            />
          ))}
          {tab === 'recent' && folders.length > shown.length && (
            <button className="ghost small-btn history-more" onClick={() => setTab('all')}>
              {t('나머지 {n}곳 보기', { n: folders.length - shown.length })}
            </button>
          )}
        </div>
      )}
    </div>
  )
}

/** One folder, its commands, and how sure the panel is about the name. */
function FolderCard({
  folder,
  here,
  open,
  copied,
  onToggle,
  onGoTo,
  onCopy,
}: {
  folder: Folder
  here: boolean
  open: boolean
  copied: string | null
  onToggle: () => void
  onGoTo?: (path: string) => void
  onCopy: (text: string, key: string) => void
}) {
  const rows = open ? folder.rows : folder.rows.slice(0, PREVIEW_COMMANDS)
  // Sending a shell to `…/src` would be sending it somewhere this does not know.
  const canGo = !!onGoTo && folder.anchored
  const more = folder.rows.length > PREVIEW_COMMANDS
  return (
    <section className="history-card" data-here={here || undefined}>
      <header className="history-card-head">
        <span className="mono history-card-path" title={folder.path}>
          {folder.anchored ? folder.path : `…/${folder.path}`}
          {!folder.anchored && (
            <span className="muted small"> ({t('시작 위치 불명')})</span>
          )}
        </span>
        <span className="history-badge" data-sure={folder.certain || undefined}>
          {folder.certain ? t('정확') : t('추정')}
        </span>
      </header>
      <p className="muted small history-card-meta">
        {folder.latest ? t('마지막 작업 {when}', { when: ago(folder.latest) }) + ' · ' : ''}
        {t('명령 {n}개', { n: folder.rows.length })}
      </p>

      <div className="history-runs">
        {rows.map((r, i) => (
          <button
            key={`${r.at}-${i}`}
            className="history-run"
            data-effect={r.effect}
            data-refused={r.refused || undefined}
            data-source={r.source}
            onClick={() => onCopy(r.command, r.at + r.command)}
            title={`${r.timed ? stamp(r.at, true) + ' · ' : ''}${
              r.source === 'sudo'
                ? t('sudo 저널')
                : r.source === 'typed'
                  ? t('이 앱의 터미널')
                  : t('셸 이력 파일')
            } · ${t('클릭하면 복사')}`}
          >
            <span className="history-mark">{EFFECT_MARK[r.effect]}</span>
            <span className="mono history-cmd">{r.command}</span>
            {r.source !== 'shell' && (
              <span className="history-source small">
                {r.source === 'sudo' ? t('sudo') : t('앱')}
              </span>
            )}
            {r.refused && (
              <span className="history-refused small" title={r.reason}>
                {t('실행 안 됨')}
              </span>
            )}
            {r.timed && <span className="muted small history-when">{ago(r.at)}</span>}
            {copied === r.at + r.command && (
              <span className="small history-copied">{t('복사됨')}</span>
            )}
          </button>
        ))}
      </div>

      {/* Omitted rather than emptied. A fragment path has nowhere to send a
          shell, and an empty bar still drew its border and its padding. */}
      {(canGo || more) && (
        <div className="history-card-foot">
          {canGo && (
            <button className="ghost small-btn" onClick={() => onGoTo!(folder.path)}>
              {t('이 위치로 이동')}
            </button>
          )}
          {more && (
            <button className="ghost small-btn" onClick={onToggle}>
              {open ? t('접기') : t('전체 명령 보기')}
            </button>
          )}
        </div>
      )}
    </section>
  )
}

/** The file as it stands, with nothing worked out from it.
 *
 *  Every other view here is a reading of the history. This one is the history,
 *  and it is what somebody checks when they doubt the reading. */
function RawHistory({
  rows,
  copied,
  onCopy,
}: {
  rows: HistoryRow[]
  copied: string | null
  onCopy: (text: string, key: string) => void
}) {
  return (
    <div className="history-raw">
      {rows.length === 0 && <p className="muted small history-empty">{t('기록이 없습니다.')}</p>}
      {rows.map((r, i) => (
        <button
          key={`${r.at}-${i}`}
          className="history-run"
          data-effect={r.effect}
          data-source={r.source}
          onClick={() => onCopy(r.command, 'raw' + i)}
          title={r.pwd || t('시작 위치 불명')}
        >
          <span className="history-mark">{EFFECT_MARK[r.effect]}</span>
          <span className="mono history-cmd">{r.command}</span>
          {r.timed && <span className="muted small history-when">{ago(r.at)}</span>}
          {copied === 'raw' + i && <span className="small history-copied">{t('복사됨')}</span>}
        </button>
      ))}
    </div>
  )
}
