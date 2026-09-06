import { useCallback, useEffect, useMemo, useState } from 'react'
import { HostCommandHistory, type CommandHistoryView, type SudoRun } from './ipc'
import { AccessNotice } from './EventTimeline'
import { k, t } from './i18n'

// Command history — the privileged half (arch/07, 명령 이력 C-2).
//
// "I was on this box three months ago and I cannot remember what I did." The
// thing people actually do is scroll their shell history looking for the lines
// that changed something, and this is that, with the part the file cannot give
// them: where each command ran. sudo writes the directory down as it runs, so
// the grouping below is a record rather than a reconstruction.
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
  runs: SudoRun[]
  /** The newest run in the group, which is what the list is ordered by. */
  latest: string
}

/** Grouped by directory, most recently used first.
 *
 *  Not alphabetical: the question starts with "I was here a while ago", so the
 *  place worked in last is the place to show first. */
function groupByPath(runs: SudoRun[]): Group[] {
  const byPath = new Map<string, SudoRun[]>()
  for (const r of runs) {
    const key = r.pwd || '?'
    const list = byPath.get(key)
    if (list) list.push(r)
    else byPath.set(key, [r])
  }
  const groups: Group[] = []
  for (const [pwd, list] of byPath) {
    groups.push({ pwd, runs: list, latest: list[0]?.at ?? '' })
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
        setView(await HostCommandHistory(hostID, range, elevate))
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
    const runs = view?.runs ?? []
    return groupByPath(changesOnly ? runs.filter((r) => r.effect !== 'read') : runs)
  }, [view, changesOnly])

  const copy = (r: SudoRun) => {
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

      {/* Said out loud, not printed. Somebody whose history has passwords in it
          wants to know that; nobody wants them on screen to find out. */}
      {view && view.secrets > 0 && (
        <p className="history-secrets small">
          {t('비밀번호나 토큰으로 보이는 명령 {n}건을 가렸습니다.', { n: view.secrets })}
        </p>
      )}

      {view?.access === 'ok' && groups.length === 0 && !busy && (
        <p className="muted small history-empty">
          {changesOnly
            ? t('이 기간에 sudo 로 바꾼 것이 없습니다. 조회까지 보려면 「바꾼 것만」 을 끄세요.')
            : t('이 기간에 sudo 로 실행된 명령이 없습니다.')}
        </p>
      )}

      <div className="history-list">
        {groups.map((g) => {
          const expanded = open.has(g.pwd)
          return (
            <div key={g.pwd} className="history-group">
              <button className="history-head" onClick={() => toggle(g.pwd)}>
                <span className="history-twisty">{expanded ? '▾' : '▸'}</span>
                <span className="mono history-path" title={g.pwd}>
                  {g.pwd}
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
                      onClick={() => copy(r)}
                      title={`${new Date(r.at).toLocaleString()} · ${r.user}${
                        r.runAs ? ` → ${r.runAs}` : ''
                      }`}
                    >
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
