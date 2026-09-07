// Turning a list of commands into the shape people look for them in (T-23).
//
// Two questions get asked in order, and the second one only ever gets asked
// about an answer to the first:
//
//   1. which directory was I working in?
//   2. what did I run there?
//
// A flat list of full paths answers neither well. `/home/deploy/monitoring` and
// `/home/deploy/monitoring/vector` share four fifths of their text, so scanning
// twenty of them is reading the same prefix twenty times to find the two
// characters that differ. A tree puts the shared part on one line and the
// differences underneath, which is why file explorers are trees.
//
// Directories with no commands of their own are folded into their child —
// `/home/deploy/monitoring` is one row, not four empty ones — so the depth on
// screen is the depth that carries information.

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

export interface TreeNode {
  /** The full path this node stands for. */
  path: string
  /** What to draw: the segments this node collapsed, joined. */
  label: string
  depth: number
  children: TreeNode[]
  /** Commands run in this exact directory, newest first. */
  rows: HistoryRow[]
  /** Rows here and everywhere below, for the count on the row. */
  total: number
  /** The newest activity anywhere in the subtree, which is what orders it.
   *  Empty for rows out of an untimed file — see `rank`. */
  latest: string
  /** Position of the newest row in the subtree, counting from the top of the
   *  merged list. Lower is more recent.
   *
   *  This is what actually orders the tree. Most of a real history has no
   *  timestamps at all — bash does not write them unless HISTTIMEFORMAT is set
   *  — so ordering on the time would fall back to alphabetical, and "which
   *  directory was I in last" would be answered with "etc, then home". The
   *  merged list arrives newest-first, so its own order is the recency the
   *  file does carry. */
  rank: number
  /** False when anything in the subtree had a path the replay was unsure of. */
  certain: boolean
}

interface Building {
  path: string
  seg: string
  kids: Map<string, Building>
  rows: HistoryRow[]
  /** Smallest input index seen here or below. */
  rank: number
}

function node(path: string, seg: string): Building {
  return { path, seg, kids: new Map(), rows: [], rank: Number.MAX_SAFE_INTEGER }
}

/** Splits a path into the segments a tree nests by. */
function segments(p: string): string[] {
  if (p === '/' || p === '') return []
  const body = p.startsWith('/') ? p.slice(1) : p
  return body.split('/').filter(Boolean)
}

/**
 * Builds the directory tree.
 *
 * Ordering is by most recent activity, not alphabetical, at every level. The
 * question starts with "I was here a while ago", so the place worked in last is
 * the place to show first — and that has to hold inside a directory as well as
 * between directories, or the answer to "which one" moves around.
 */
export function buildTree(rows: HistoryRow[]): TreeNode[] {
  const roots = new Map<string, Building>()

  rows.forEach((r, index) => {
    const path = r.pwd || '?'
    const segs = segments(path)
    if (segs.length === 0) {
      // The root itself, or a path nothing is known about.
      const key = path === '?' ? '?' : '/'
      const b = roots.get(key) ?? node(key, key)
      roots.set(key, b)
      b.rows.push(r)
      b.rank = Math.min(b.rank, index)
      return
    }
    const absolute = path.startsWith('/')
    let here = ''
    let level = roots
    let b: Building | undefined
    for (const seg of segs) {
      here = absolute ? `${here}/${seg}` : here ? `${here}/${seg}` : seg
      b = level.get(seg)
      if (!b) {
        b = node(here, seg)
        level.set(seg, b)
      }
      // Every ancestor inherits the rank, so a directory sorts by the newest
      // thing anywhere beneath it rather than by its own last visit.
      b.rank = Math.min(b.rank, index)
      level = b.kids
    }
    b!.rows.push(r)
  })

  const built = [...roots.values()].map((b) => finish(b, 0))
  return sortNodes(built)
}

/** Folds a chain of childless directories into one row and computes the totals. */
function finish(b: Building, depth: number): TreeNode {
  // A directory with nothing of its own and exactly one child is not a place
  // anybody worked — it is punctuation between two that are.
  let label = b.seg
  let cur = b
  while (cur.rows.length === 0 && cur.kids.size === 1) {
    const only = [...cur.kids.values()][0]
    label = `${label}/${only.seg}`
    cur = only
  }

  // A top-level absolute path keeps its leading slash. Without it `/etc/nginx`
  // renders as `etc/nginx`, which is a different path — and the one place the
  // panel must not be casual is where a command ran.
  if (depth === 0 && b.path.startsWith('/')) label = '/' + label

  const children = sortNodes([...cur.kids.values()].map((k) => finish(k, depth + 1)))
  const rows = [...cur.rows].sort(byNewest)

  let total = rows.length
  let latest = rows[0]?.at ?? ''
  let certain = rows.every((r) => r.certain)
  for (const c of children) {
    total += c.total
    if (c.latest > latest) latest = c.latest
    if (!c.certain) certain = false
  }

  return { path: cur.path, label, depth, children, rows, total, latest, rank: cur.rank, certain }
}

function byNewest(a: HistoryRow, b: HistoryRow): number {
  // Untimed rows keep the order the file gave them and fall in behind the timed
  // ones. Sorting them by a time they do not have would be inventing one.
  if (a.timed !== b.timed) return a.timed ? -1 : 1
  if (!a.timed) return 0
  return a.at < b.at ? 1 : a.at > b.at ? -1 : 0
}

function sortNodes(ns: TreeNode[]): TreeNode[] {
  return ns.sort((a, b) => {
    if (a.rank !== b.rank) return a.rank - b.rank
    return a.path < b.path ? -1 : 1
  })
}

/** Every path from the tree's root down to `target`, for opening it. */
export function pathTo(nodes: TreeNode[], target: string): string[] {
  const out: string[] = []
  const walk = (ns: TreeNode[]): boolean => {
    for (const n of ns) {
      if (n.path === target || target.startsWith(n.path + '/')) {
        out.push(n.path)
        if (n.path === target) return true
        if (walk(n.children)) return true
        // The exact directory is not in the history; opening its nearest
        // ancestor is still the right place to land.
        return true
      }
    }
    return false
  }
  walk(nodes)
  return out
}
