import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
import { buildTree, pathTo, type HistoryRow, type TreeNode } from './historyTree'
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
}: {
  hostID: string
  /** Where the terminal beside this panel is standing, when it is known. The
   *  panel opens there: "what did I run here" is the half of the question that
   *  needs to know where "here" is. */
  cwd?: string
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
  // Which directory's commands are shown below the tree. The tree answers
  // "which directory"; this answers "what did I run there", and keeping them
  // apart is what makes the second question askable at all.
  const [picked, setPicked] = useState<string | null>(null)
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

  const tree = useMemo(() => {
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
    return buildTree(changesOnly ? rows.filter((r) => r.effect !== 'read') : rows)
  }, [view, typed, shell, changesOnly])

  // Opens where the terminal is standing, once, when the tree first arrives.
  // Not on every change: re-opening under somebody who has been clicking around
  // is the panel taking the wheel back.
  const opened = useRef(false)
  useEffect(() => {
    if (opened.current || tree.length === 0) return
    opened.current = true
    const target = cwd && findNode(tree, cwd) ? cwd : tree[0].path
    setOpen(new Set(pathTo(tree, target)))
    setPicked(target)
  }, [tree, cwd])

  const chosen = useMemo(() => (picked ? findNode(tree, picked) : null), [tree, picked])

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

      {tree.length === 0 && !busy && (
        <p className="muted small history-empty">
          {changesOnly
            ? t('이 기간에 바꾼 것이 없습니다. 조회까지 보려면 「바꾼 것만」 을 끄세요.')
            : t('이 기간에 기록된 명령이 없습니다.')}
        </p>
      )}

      {/* Top half answers "which directory". A tree rather than full paths: the
          twenty rows of a real history share most of their text, and reading
          the same prefix twenty times to find the two characters that differ is
          what a tree exists to stop. */}
      <div className="history-tree">
        {tree.map((n) => (
          <TreeRow
            key={n.path}
            node={n}
            open={open}
            picked={picked}
            here={cwd}
            onToggle={toggle}
            onPick={setPicked}
          />
        ))}
      </div>

      {/* Bottom half answers "what did I run there". Separate from the tree so
          picking a directory does not push everything below it down the
          screen, which is what an inline expander does in a panel this narrow. */}
      {chosen && (
        <div className="history-chosen">
          <div className="history-chosen-head">
            <span className="mono ellipsis" title={chosen.path}>
              {chosen.path}
            </span>
            <span className="muted small">{t('{n}회', { n: chosen.rows.length })}</span>
          </div>
          <div className="history-runs">
            {chosen.rows.length === 0 && (
              <p className="muted small history-empty">
                {t('이 폴더에서 직접 실행한 것은 없습니다 — 아래 폴더를 열어 보세요.')}
              </p>
            )}
            {chosen.rows.map((r, i) => (
              <button
                key={`${r.at}-${i}`}
                className="history-run"
                data-effect={r.effect}
                data-refused={r.refused || undefined}
                data-source={r.source}
                onClick={() => copy(r)}
                title={`${r.timed ? new Date(r.at).toLocaleString() + ' · ' : ''}${
                  r.source === 'sudo'
                    ? t('sudo 저널')
                    : r.source === 'typed'
                      ? t('이 앱의 터미널')
                      : t('셸 이력 파일')
                }`}
              >
                <span className="history-mark">{EFFECT_MARK[r.effect]}</span>
                <span className="mono history-cmd">{r.command}</span>
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
        </div>
      )}
    </div>
  )
}


/** One directory in the tree, and its children when it is open. */
function TreeRow({
  node,
  open,
  picked,
  here,
  onToggle,
  onPick,
}: {
  node: TreeNode
  open: Set<string>
  picked: string | null
  /** Where the terminal is standing. Marked rather than filtered: the point is
   *  to find it at a glance, not to hide everywhere else. */
  here?: string
  onToggle: (path: string) => void
  onPick: (path: string) => void
}) {
  const expanded = open.has(node.path)
  const hasKids = node.children.length > 0

  return (
    <div className="history-node">
      <div
        className="history-dir"
        data-picked={picked === node.path || undefined}
        data-here={here === node.path || undefined}
        style={{ paddingLeft: 8 + node.depth * 12 }}
      >
        <button
          className="history-twisty"
          disabled={!hasKids}
          onClick={() => onToggle(node.path)}
          aria-label={expanded ? t('접기') : t('펼치기')}
        >
          {hasKids ? (expanded ? '▾' : '▸') : '·'}
        </button>
        <button className="history-dir-name" onClick={() => onPick(node.path)}>
          <span
            className="mono ellipsis"
            data-guess={!node.certain || undefined}
            title={node.path}
          >
            {node.label}
            {!node.certain && <span className="history-guess">?</span>}
          </span>
        </button>
        <span className="muted small history-count">{node.total}</span>
      </div>

      {expanded &&
        node.children.map((c) => (
          <TreeRow
            key={c.path}
            node={c}
            open={open}
            picked={picked}
            here={here}
            onToggle={onToggle}
            onPick={onPick}
          />
        ))}
    </div>
  )
}

function findNode(ns: TreeNode[], path: string): TreeNode | null {
  for (const n of ns) {
    if (n.path === path) return n
    const hit = findNode(n.children, path)
    if (hit) return hit
  }
  return null
}
