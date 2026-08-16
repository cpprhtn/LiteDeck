import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { TransferPanel } from './TransferPanel'
import {
  Chmod,
  DeletePaths,
  HomeDir,
  ListDir,
  MakeDir,
  PickLocalDir,
  PickLocalFiles,
  PickLocalUploadDir,
  ReadTextFile,
  RenamePath,
  PreviewFile,
  StartDownload,
  StartUpload,
  type DirListing,
  type FileEntry,
} from './ipc'
import { on } from './ipc'
import {
  clearReveal,
  forgetPaths,
  openFile,
  openPreview,
  renameOpen,
  setTreeWidth,
  unsavedUnder,
  useOpenFiles,
  useReveal,
  useTreeWidth,
  type OpenFile,
} from './openFiles'
import { isTyping, matches, shortcutLabel } from './platform'
import { t } from './i18n'

// CodeMirror and its grammars are the largest thing in the app after the
// terminal, and a session spent looking at a directory listing never needs
// them. Loaded when the first file is opened, the same way xterm.js is.
const EditorPane = lazy(() =>
  import('./EditorPane').then((m) => ({ default: m.EditorPane })),
)

// The file explorer (§4.2).
//
// No polling: a directory listing does not change under you often enough to
// justify it, and the server pays for every poll (§3.2d). Refresh happens on
// navigation, after an action, or on demand.

const ROW_HEIGHT = 26

// The listing's columns, by how much room the tree has (§4.7-3).
//
// Beside the editor the tree is a few hundred pixels wide, and a fixed set of
// five columns does not shrink — it takes 448px of fixed width and gaps, so the
// name column, the only one that is `1fr`, collapses to nothing and every row
// goes blank. Columns are dropped instead, least useful first: MODE goes before
// MODIFIED because permissions are something you look up, and a date is
// something you scan.
// The twisty and the icon live inside the name cell rather than in a column of
// their own, so indenting a nested row moves only the name — the size and date
// columns stay in line at every depth.
const LAYOUTS = [
  { min: 560, columns: '1fr 100px 110px 150px', mode: true, modified: true },
  { min: 400, columns: '1fr 90px 150px', mode: false, modified: true },
  { min: 0, columns: '1fr 80px', mode: false, modified: false },
]

/** How far one level of nesting shifts a row. */
const INDENT = 14

function layoutFor(width: number) {
  return LAYOUTS.find((l) => width >= l.min) ?? LAYOUTS[LAYOUTS.length - 1]
}

function fmtSize(bytes: number, isDir: boolean): string {
  if (isDir) return '—'
  if (bytes >= 1 << 30) return `${(bytes / (1 << 30)).toFixed(1)} GB`
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`
  if (bytes >= 1 << 10) return `${(bytes / (1 << 10)).toFixed(1)} KB`
  return `${bytes} B`
}

function fmtTime(unix: number): string {
  if (!unix) return ''
  const d = new Date(unix * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/**
 * The one glyph that says what a row *is*.
 *
 * Kept separate from the twisty, which says whether a directory is open. Both
 * were the same character before, so a folder and a file differed by ▸ against
 * · — a distinction nobody finds while scanning a listing for a filename.
 */
function icon(e: FileEntry, open: boolean): string {
  if (e.broken) return '⚠'
  if (e.isSymlink) return '↗'
  if (e.isDir) return open ? '📂' : '📁'
  return '📄'
}

/**
 * Permissions, editable both ways (§4.2).
 *
 * Checkboxes are easier to reason about, but anyone who has run `chmod 755`
 * a thousand times thinks in octal and will type it faster than they can click
 * nine boxes. Both views edit the same number, so neither is a second-class
 * path — the octal field is not a read-out.
 */
function PermissionEditor({
  perm,
  onChange,
}: {
  perm: number
  onChange: (perm: number) => void
}) {
  const canonical = perm.toString(8).padStart(3, '0')
  // Kept separate from perm so a half-typed value ("7") does not immediately
  // snap the checkboxes to 007 while the user is still typing "755".
  const [text, setText] = useState(canonical)

  useEffect(() => setText(canonical), [canonical])

  const commit = (raw: string) => {
    const cleaned = raw.trim().replace(/^0o/i, '')
    if (!/^[0-7]{3,4}$/.test(cleaned)) {
      setText(canonical) // reject silently: the checkboxes still show the truth
      return
    }
    onChange(parseInt(cleaned, 8))
  }

  const bit = (i: number) => (perm >> i) & 1
  const toggle = (i: number) => onChange(perm ^ (1 << i))
  const rows = [
    { label: t('소유자'), base: 6 },
    { label: t('그룹'), base: 3 },
    { label: t('기타'), base: 0 },
  ]

  return (
    <div className="perms">
      <div className="perms-grid">
        <span />
        <span>r</span>
        <span>w</span>
        <span>x</span>
        {rows.map((r) => (
          <Row key={r.label} label={r.label} base={r.base} bit={bit} toggle={toggle} />
        ))}
      </div>

      <div className="perms-octal-edit">
        <label className="muted small">chmod</label>
        <input
          className="perms-octal-input mono"
          value={text}
          inputMode="numeric"
          maxLength={4}
          spellCheck={false}
          onChange={(e) => setText(e.target.value)}
          onBlur={(e) => commit(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              commit((e.target as HTMLInputElement).value)
            }
          }}
        />
        <code className="muted small">{symbolic(perm)}</code>
      </div>
    </div>
  )
}

/** "rwxr-xr-x", the form ls prints — the other way people read permissions. */
function symbolic(perm: number): string {
  const set = 'rwx'
  let out = ''
  for (let group = 2; group >= 0; group--) {
    for (let b = 2; b >= 0; b--) {
      out += (perm >> (group * 3 + b)) & 1 ? set[2 - b] : '-'
    }
  }
  return out
}

function Row({
  label,
  base,
  bit,
  toggle,
}: {
  label: string
  base: number
  bit: (i: number) => number
  toggle: (i: number) => void
}) {
  return (
    <>
      <span className="muted">{label}</span>
      {[2, 1, 0].map((off) => (
        <input
          key={off}
          type="checkbox"
          checked={bit(base + off) === 1}
          onChange={() => toggle(base + off)}
        />
      ))}
    </>
  )
}

type Dialog =
  | { kind: 'newFolder' }
  | { kind: 'rename'; entry: FileEntry }
  | {
      kind: 'delete'
      entries: FileEntry[]
      protectedPath: string | null
      /** Open tabs these paths would take with them. */
      unsaved: OpenFile[]
    }
  | { kind: 'perms'; entry: FileEntry }

export function FileExplorer({
  hostID,
  onError,
}: {
  hostID: string
  onError: (msg: string) => void
}) {
  const [listing, setListing] = useState<DirListing | null>(null)
  const [cwd, setCwd] = useState<string>('')
  const [addressBar, setAddressBar] = useState('')
  const [showHidden, setShowHidden] = useState(false)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [input, setInput] = useState('')
  const [perm, setPerm] = useState(0o644)
  const [busy, setBusy] = useState(false)
  const [history, setHistory] = useState<string[]>([])
  // Where "back" came from, so it can be undone. Cleared by any fresh
  // navigation, which is what every browser and file manager does.
  const [forward, setForward] = useState<string[]>([])
  const [dropping, setDropping] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)
  const tableRef = useRef<HTMLDivElement>(null)
  // Measured rather than derived from the splitter: the window can also be
  // narrow, and a listing that only adapts when the editor is open would still
  // go blank on a small screen.
  const [tableWidth, setTableWidth] = useState(9999)

  useEffect(() => {
    const el = tableRef.current
    if (!el) return
    const ro = new ResizeObserver(([entry]) =>
      setTableWidth(entry.contentRect.width),
    )
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const layout = layoutFor(tableWidth)

  // Open documents outlive this component: switching to the terminal tab and
  // back unmounts the explorer, and unsaved edits must not go with it (§4.7-3).
  const openDocs = useOpenFiles(hostID)
  const treeWidth = useTreeWidth()
  const editing = openDocs.files.length > 0
  // So the listing can mark the rows that are open in a tab — otherwise the
  // tree and the editor look like two unrelated panels sharing a window.
  const openPaths = useMemo(
    () => new Set(openDocs.files.map((f) => f.path)),
    [openDocs.files],
  )

  // The tree (§4.7-3). A directory opens in place rather than replacing the
  // listing, so the path you came from stays on screen — the thing a flat
  // listing cannot do and the reason people keep a tree open beside an editor.
  //
  // Children are fetched the first time a directory is opened and kept
  // afterwards: a folder nobody expands costs nothing, which matters when the
  // server is paying for every readdir.
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [children, setChildren] = useState<Map<string, FileEntry[]>>(new Map())
  const [expanding, setExpanding] = useState<Set<string>>(new Set())
  // Filtering by name, over what has already been read (§4.7-3).
  //
  // Deliberately not a server-side find: this narrows the listings the app is
  // already holding, so typing costs nothing on the server no matter how fast.
  // The trade is that it cannot see into folders nobody has opened, which the
  // placeholder says out loud rather than leaving the user to wonder why a file
  // they know exists is missing.
  const [filter, setFilter] = useState('')
  const filterRef = useRef<HTMLInputElement>(null)
  const needle = filter.trim().toLowerCase()
  // Read by refresh, which must not be rebuilt every time a folder opens.
  const expandedRef = useRef(expanded)
  expandedRef.current = expanded

  const readDir = useCallback(
    async (dir: string) => {
      setExpanding((s) => new Set(s).add(dir))
      try {
        const l = await ListDir(hostID, dir)
        setChildren((m) => new Map(m).set(dir, l.entries))
      } catch (e) {
        onError(String(e))
        // Leaving it "open" but empty would look like an empty folder rather
        // than one that could not be read.
        setExpanded((s) => {
          const next = new Set(s)
          next.delete(dir)
          return next
        })
      } finally {
        setExpanding((s) => {
          const next = new Set(s)
          next.delete(dir)
          return next
        })
      }
    },
    [hostID, onError],
  )

  const toggle = useCallback(
    (e: FileEntry) => {
      setExpanded((prev) => {
        const next = new Set(prev)
        if (next.has(e.path)) {
          next.delete(e.path)
        } else {
          next.add(e.path)
          if (!children.has(e.path)) void readDir(e.path)
        }
        return next
      })
    },
    [children, readDir],
  )

  // Navigating changes the root, so everything opened under the old one is gone.
  const load = useCallback(
    async (dir: string) => {
      setBusy(true)
      try {
        const l = await ListDir(hostID, dir)
        setListing(l)
        setCwd(l.path)
        setAddressBar(l.path)
        setSelected(new Set())
        setExpanded(new Set())
        setChildren(new Map())
        // The filter narrowed a tree that no longer exists. Keeping it would
        // land the user in a new directory that looks empty.
        setFilter('')
      } catch (e) {
        onError(String(e))
      } finally {
        setBusy(false)
      }
    },
    [hostID, onError],
  )

  /** Rereads one directory in place — after a save, or a rename inside it. */
  const refreshDir = useCallback(
    async (dir: string) => {
      try {
        const l = await ListDir(hostID, dir)
        if (l.path === cwd) setListing(l)
        else if (expandedRef.current.has(l.path)) {
          setChildren((m) => new Map(m).set(l.path, l.entries))
        }
      } catch {
        // A refresh that fails leaves the previous listing, which is better than
        // emptying the tree because one readdir lost a race with a delete.
      }
    },
    [hostID, cwd],
  )

  // Where the tree is, readable from a callback without making it depend on a
  // value that changes on every navigation.
  const cwdRef = useRef(cwd)
  cwdRef.current = cwd
  // The host whose landing spot has already been decided.
  //
  // Asking the server for the home directory costs a round trip, and a
  // `code /etc` arriving during it used to be overwritten by the answer to a
  // question nobody was still asking — which is why `code` and `open` always
  // ended up on home while `vi` looked fine (its editor tab does not depend on
  // where the tree is, so only the tree was wrong).
  //
  // It holds the host rather than a bare flag because it must survive the effect
  // running twice: React remounts effects in development, and a flag reset at
  // the top of the second pass reopened the same race it was added to close.
  const landed = useRef<string | null>(null)

  const navigate = useCallback(
    (dir: string) => {
      landed.current = hostID
      const from = cwdRef.current
      if (from) setHistory((h) => [...h.slice(-30), from])
      setForward([])
      void load(dir)
    },
    [load, hostID],
  )

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      if (landed.current === hostID) return
      try {
        const home = await HomeDir(hostID)
        // Somewhere better was chosen while this was in flight.
        if (cancelled || landed.current === hostID) return
        await load(home)
      } catch (e) {
        if (!cancelled) onError(String(e))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [hostID, load, onError])

  // A path the terminal handed over (§4.6a). A directory becomes the root; a
  // file opens in the editor with its directory as the root, which is what
  // `code src/main.go` means in every editor that has the phrase.
  const reveal = useReveal(hostID)
  // Acted on once per request. React runs mount effects twice in development,
  // and the second pass still sees the value this one is about to clear.
  const revealed = useRef(0)
  useEffect(() => {
    if (!reveal || revealed.current === reveal.nonce) return
    revealed.current = reveal.nonce
    clearReveal(hostID, reveal.nonce)
    if (reveal.isDir) {
      navigate(reveal.path)
      return
    }
    const dir = parentOf(reveal.path)
    ;(async () => {
      // Sequenced, not raced: the listing has to be the one holding the file
      // before the tab that opens it appears beside it.
      if (dir !== cwdRef.current) navigate(dir)
      // `vi test.cpp` on a file that is not there yet opens an empty buffer;
      // baseModTime 0 tells the save path there is nothing to compare against,
      // so the first save creates it.
      if (reveal.isNew) {
        openFile(hostID, {
          path: reveal.path,
          content: '',
          size: 0,
          perm: 0o644,
          modTime: 0,
          tooLarge: false,
          binary: false,
        })
        return
      }
      try {
        const file = await ReadTextFile(hostID, reveal.path)
        if (file.tooLarge || file.binary) {
          onError(
            file.binary
              ? t('{path}: 바이너리 파일이라 열 수 없습니다', { path: reveal.path })
              : t('{path}: 편집기 한도(2MB)를 넘어 열 수 없습니다', { path: reveal.path }),
          )
          return
        }
        openFile(hostID, file)
      } catch (e) {
        onError(String(e))
      }
    })()
  }, [reveal, hostID, navigate, onError])

  const back = () => {
    setHistory((h) => {
      if (h.length === 0) return h
      if (cwdRef.current) setForward((f) => [...f.slice(-30), cwdRef.current])
      void load(h[h.length - 1])
      return h.slice(0, -1)
    })
  }

  const forth = () => {
    setForward((f) => {
      if (f.length === 0) return f
      // Straight to load, not navigate: navigate clears the forward stack, and
      // going forward is the one move that must not.
      if (cwdRef.current) setHistory((h) => [...h.slice(-30), cwdRef.current])
      void load(f[f.length - 1])
      return f.slice(0, -1)
    })
  }

  /** Rereads the root and everything currently open, keeping the shape. */
  const refresh = useCallback(async () => {
    setBusy(true)
    try {
      const l = await ListDir(hostID, cwd)
      setListing(l)
      const dirs = [...expandedRef.current]
      const loaded = await Promise.all(
        dirs.map((d) =>
          ListDir(hostID, d)
            .then((x) => [d, x.entries] as const)
            .catch(() => null),
        ),
      )
      setChildren(new Map(loaded.filter((x) => x !== null)))
    } catch (e) {
      onError(String(e))
    } finally {
      setBusy(false)
    }
  }, [hostID, cwd, onError])

  /** The open parts of the tree, flattened — one array the virtualiser can size. */
  const rows = useMemo(() => {
    const out: { entry: FileEntry; depth: number; open: boolean }[] = []
    const visible = (list: FileEntry[]) =>
      showHidden ? list : list.filter((e) => !e.name.startsWith('.'))

    if (!needle) {
      const walk = (list: FileEntry[], depth: number) => {
        for (const entry of visible(list)) {
          const open = entry.isDir && expanded.has(entry.path)
          out.push({ entry, depth, open })
          if (open) {
            const kids = children.get(entry.path)
            if (kids) walk(kids, depth + 1)
          }
        }
      }
      walk(listing?.entries ?? [], 0)
      return out
    }

    // Filtering descends into every directory that has been read, open or not:
    // a filter exists to find what is *not* on screen, and closed-but-read
    // folders are already in memory. A row survives if its own name matches or
    // if something under it did — otherwise the match would appear with no
    // path leading to it.
    const walk = (list: FileEntry[], depth: number): boolean => {
      let kept = false
      for (const entry of visible(list)) {
        const at = out.length
        const hit = entry.name.toLowerCase().includes(needle)
        const row = { entry, depth, open: false }
        out.push(row)
        if (entry.isDir) {
          const kids = children.get(entry.path)
          if (kids) row.open = walk(kids, depth + 1)
        }
        if (hit || row.open) kept = true
        // Nothing under it was kept either, so this truncation drops exactly
        // this one row.
        else out.length = at
      }
      return kept
    }
    walk(listing?.entries ?? [], 0)
    return out
  }, [listing, showHidden, expanded, children, needle])

  // Only the rows that matched, not the parents dragged along to show where
  // they live — "3 matches" has to mean three files.
  const matchCount = useMemo(
    () =>
      needle === ''
        ? 0
        : rows.filter((r) => r.entry.name.toLowerCase().includes(needle)).length,
    [rows, needle],
  )

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
  })

  const selectedEntries = rows
    .map((r) => r.entry)
    .filter((e) => selected.has(e.path))

  const openEntry = async (e: FileEntry) => {
    // Double-clicking a directory moves the root there. Single-clicking opens it
    // in place — this is the escape hatch for a tree that has got too deep to
    // scroll, and it is what the ↑ button undoes.
    if (e.isDir) {
      navigate(e.path)
      return
    }
    try {
      const file = await ReadTextFile(hostID, e.path)
      if (file.tooLarge) {
        onError(t('{name}: {size} — 편집기 한도(2MB)를 넘어 읽기 전용으로 엽니다', {
          name: e.name,
          size: fmtSize(file.size, false),
        }))
        openPreview(hostID, await PreviewFile(hostID, e.path))
        return
      }
      if (file.binary) {
        // Not editable is not the same as not viewable. A refusal here was the
        // whole answer until now, which left no way to look at the file at all.
        openPreview(hostID, await PreviewFile(hostID, e.path))
        return
      }
      openFile(hostID, file)
    } catch (err) {
      onError(String(err))
    }
  }

  const act = async (fn: () => Promise<{ ok: boolean; error?: string }>) => {
    setBusy(true)
    try {
      const res = await fn()
      if (!res.ok) {
        onError(res.error ?? t('실패했습니다'))
        return false
      }
      setDialog(null)
      await load(cwd)
      return true
    } catch (e) {
      onError(String(e))
      return false
    } finally {
      setBusy(false)
    }
  }

  // Keyboard shortcuts follow the platform, not one hard-coded convention (§8).
  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      // Not while the user is typing. These bindings are bare keys on both
      // platforms — Enter renames on macOS, Delete deletes on Windows — so
      // without this, a keystroke meant for the editor or the address bar acts
      // on whatever happens to be selected in the tree.
      if (dialog || isTyping(ev)) return
      const one = selectedEntries[0]
      if (matches(ev, 'find')) {
        // isTyping already gave the editor first claim on this: CodeMirror's
        // document is a contenteditable, so ⌘F there never reaches this handler.
        ev.preventDefault()
        filterRef.current?.select()
      } else if (matches(ev, 'refresh')) {
        ev.preventDefault()
        void refresh()
      } else if (matches(ev, 'parentDir') && listing) {
        ev.preventDefault()
        navigate(listing.parent)
      } else if (one && matches(ev, 'rename')) {
        ev.preventDefault()
        setInput(one.name)
        setDialog({ kind: 'rename', entry: one })
      } else if (selectedEntries.length > 0 && matches(ev, 'delete')) {
        ev.preventDefault()
        void askDelete()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  const askDelete = async () => {
    if (selectedEntries.length === 0) return
    // The Go side is the actual guard; this only decides what to show.
    const prot = selectedEntries.find((e) => e.isDir && isProtectedLike(e.path))
    setInput('')
    setDialog({
      kind: 'delete',
      entries: selectedEntries,
      protectedPath: prot?.path ?? null,
      unsaved: unsavedUnder(hostID, selectedEntries.map((e) => e.path)),
    })
  }

  const upload = async (paths?: string[]) => {
    try {
      const files = paths ?? (await PickLocalFiles())
      if (!files?.length) return
      await StartUpload(hostID, files, cwd)
    } catch (e) {
      onError(String(e))
    }
  }

  // Its own button because no OS file chooser picks files and folders in one
  // pass. Dragging a folder in already worked; this is for the people who never
  // find out that it does.
  const uploadFolder = async () => {
    try {
      const dir = await PickLocalUploadDir()
      if (!dir) return
      await StartUpload(hostID, [dir], cwd)
    } catch (e) {
      onError(String(e))
    }
  }

  // Wails delivers absolute paths, not file contents — which is what makes
  // dropping a multi-gigabyte folder cost nothing until the transfer starts.
  // The drop target is the whole explorer, and the destination is whatever
  // directory is on screen.
  useEffect(() => {
    if (!cwd) return
    return on<string[]>('files:dropped', (paths) => {
      setDropping(false)
      void upload(paths)
    })
    // upload closes over cwd, so the subscription is refreshed on navigation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cwd, hostID])

  /** Download exactly one path — what a read-only tab offers. */
  const downloadOne = async (p: string) => {
    try {
      const dir = await PickLocalDir()
      if (!dir) return
      await StartDownload(hostID, [p], dir)
    } catch (e) {
      onError(String(e))
    }
  }

  const download = async () => {
    if (selectedEntries.length === 0) {
      onError(t('다운로드할 항목을 선택하세요'))
      return
    }
    try {
      const dir = await PickLocalDir()
      if (!dir) return
      // Directories are walked server-side and transferred recursively; the
      // queue shows one row per tree rather than one per file.
      await StartDownload(hostID, selectedEntries.map((f) => f.path), dir)
    } catch (e) {
      onError(String(e))
    }
  }

  return (
    <div
      className="view file-view"
      data-dropping={dropping || undefined}
      onDragOver={(e) => {
        e.preventDefault()
        setDropping(true)
      }}
      onDragLeave={(e) => {
        // Only when the pointer actually leaves the panel, not when it crosses
        // a child element — otherwise the highlight flickers on every row.
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setDropping(false)
      }}
      onDrop={() => setDropping(false)}
    >
      {dropping && (
        <div className="drop-hint">
          {t('{dir} 에 업로드합니다', { dir: cwd })}
        </div>
      )}

      {/* The mouse's side buttons, where a browser puts back and forward.
          mousedown rather than auxclick: Chromium fires auxclick for buttons 3
          and 4 but WebKit does not, and mousedown is the one both deliver.
          preventDefault matters on WebView2, which otherwise takes them as
          webview history navigation and leaves the SPA. */}
      <div
        className="file-split"
        onMouseDown={(e) => {
          if (e.button !== 3 && e.button !== 4) return
          e.preventDefault()
          if (e.button === 3) back()
          else forth()
        }}
      >
        <div
          className="file-tree"
          style={editing ? { flex: `0 0 ${treeWidth}px`, width: treeWidth } : undefined}
        >
          <div className="view-toolbar wrap">
            <button
              className="ghost"
              disabled={history.length === 0}
              onClick={back}
              title={t('뒤로')}
            >
              ←
            </button>
            <button
              className="ghost"
              disabled={forward.length === 0}
              onClick={forth}
              title={t('앞으로')}
            >
              →
            </button>
            <button
              className="ghost"
              disabled={!listing || cwd === '/'}
              onClick={() => listing && navigate(listing.parent)}
              title={t('상위 폴더 ({key})', { key: shortcutLabel('parentDir') })}
            >
              ↑
            </button>
            <form
              style={{ flex: 1, display: 'flex' }}
              onSubmit={(e) => {
                e.preventDefault()
                navigate(addressBar)
              }}
            >
              <input
                className="search"
                style={{ flex: 1, maxWidth: 'none' }}
                value={addressBar}
                onChange={(e) => setAddressBar(e.target.value)}
                spellCheck={false}
              />
            </form>
            <div className="tree-filter">
              <input
                ref={filterRef}
                className="search"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder={t('연 폴더에서 이름 거르기 ({key})', {
                  key: shortcutLabel('find'),
                })}
                title={t('이미 읽어 온 목록에서만 거릅니다 — 서버에 아무것도 묻지 않습니다.')}
                spellCheck={false}
                onKeyDown={(e) => {
                  if (e.key === 'Escape') {
                    e.preventDefault()
                    setFilter('')
                    e.currentTarget.blur()
                  }
                }}
              />
              {needle !== '' && (
                <button
                  className="ghost tree-filter-clear"
                  onClick={() => setFilter('')}
                  title={t('지우기')}
                >
                  ✕
                </button>
              )}
            </div>
            {needle !== '' && (
              <span className="badge">{t('{n}개 일치', { n: matchCount })}</span>
            )}
            <label className="checkbox" style={{ margin: 0 }}>
              <input
                type="checkbox"
                checked={showHidden}
                onChange={(e) => setShowHidden(e.target.checked)}
              />
              {t('숨김')}
            </label>
            <button className="ghost" onClick={() => void refresh()} title={shortcutLabel('refresh')}>
              {t('새로고침')}
            </button>
          </div>

          <div className="view-toolbar wrap">
            <button
              onClick={() => {
                setInput('')
                setDialog({ kind: 'newFolder' })
              }}
            >
              {t('새 폴더')}
            </button>
            <button onClick={() => void upload()}>{t('업로드…')}</button>
            <button onClick={() => void uploadFolder()}>{t('폴더 업로드…')}</button>
            <button disabled={selectedEntries.length === 0} onClick={() => void download()}>
              {t('다운로드…')}
            </button>
            <button
              disabled={selectedEntries.length !== 1}
              onClick={() => {
                const e = selectedEntries[0]
                setInput(e.name)
                setDialog({ kind: 'rename', entry: e })
              }}
            >
              {t('이름 변경')}
            </button>
            <button
              disabled={selectedEntries.length !== 1}
              onClick={() => {
                const e = selectedEntries[0]
                setPerm(e.perm)
                setDialog({ kind: 'perms', entry: e })
              }}
            >
              {t('권한')}
            </button>
            <button
              className="danger"
              disabled={selectedEntries.length === 0}
              onClick={() => void askDelete()}
            >
              {t('삭제')}
            </button>
            {listing?.protected && (
              <span className="badge warn" title={t('루트 바로 아래 디렉터리 — 하위까지 지우려면 경로를 직접 입력해야 합니다')}>
                {t('보호된 경로')}
              </span>
            )}
            {listing?.truncated && (
              <span className="badge warn">{t('{n}개 중 일부만 표시', { n: listing.total })}</span>
            )}
          </div>

          <div className="table" ref={tableRef}>
            <div className="thead" style={{ gridTemplateColumns: layout.columns }}>
              <div>NAME</div>
              <div className="num">SIZE</div>
              {layout.mode && <div>MODE</div>}
              {layout.modified && <div>MODIFIED</div>}
            </div>
            <div className="tbody" ref={scrollRef}>
              {busy && !listing && <div className="placeholder">{t('읽는 중…')}</div>}
              {listing && rows.length === 0 && (
                <div className="placeholder">
                  {needle === ''
                    ? t('비어 있는 디렉터리입니다.')
                    : t('일치하는 항목이 없습니다. 아직 열어 보지 않은 폴더는 검색 대상이 아닙니다.')}
                </div>
              )}
              <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
                {virtualizer.getVirtualItems().map((item) => {
                  const { entry: e, depth, open: isOpen } = rows[item.index]
                  return (
                    <div
                      key={e.path}
                      className="trow selectable-row"
                      data-selected={selected.has(e.path) || undefined}
                      data-open={openPaths.has(e.path) || undefined}
                      style={{
                        gridTemplateColumns: layout.columns,
                        height: ROW_HEIGHT,
                        transform: `translateY(${item.start}px)`,
                      }}
                      onClick={(ev) => {
                        setSelected((prev) => {
                          const next =
                            ev.metaKey || ev.ctrlKey ? new Set(prev) : new Set<string>()
                          next.has(e.path) ? next.delete(e.path) : next.add(e.path)
                          return next
                        })
                        // One click opens a folder, the way a tree is expected to
                        // behave. Multi-select with a modifier still just selects.
                        if (e.isDir && !ev.metaKey && !ev.ctrlKey) toggle(e)
                      }}
                      onDoubleClick={() => void openEntry(e)}
                    >
                      <div
                        className="tree-name ellipsis"
                        data-dir={e.isDir || undefined}
                        style={{
                          paddingLeft: depth * INDENT,
                          // One vertical rule per level of ancestry, drawn in
                          // the indent itself. Without them a row six levels
                          // deep is just a row that starts further right, and
                          // there is nothing to follow back up to its parent.
                          backgroundSize: `${depth * INDENT}px 100%`,
                        }}
                      >
                        <span className="tree-twisty">
                          {e.isDir
                            ? expanding.has(e.path)
                              ? '⋯'
                              : isOpen
                                ? '▾'
                                : '▸'
                            : ''}
                        </span>
                        <span className="tree-icon">{icon(e, isOpen)}</span>
                        <span className="ellipsis">{e.name}</span>
                        {e.isDir && <span className="tree-slash">/</span>}
                        {e.isSymlink && e.linkTarget && (
                          <span className="muted"> → {e.linkTarget}</span>
                        )}
                        {e.broken && <span className="badge danger">{t('끊긴 링크')}</span>}
                      </div>
                      <div className="num mono">{fmtSize(e.size, e.isDir)}</div>
                      {layout.mode && <div className="mono muted">{e.mode}</div>}
                      {layout.modified && (
                        <div className="mono muted">{fmtTime(e.modTime)}</div>
                      )}
                    </div>
                  )
                })}
              </div>
            </div>
          </div>
        </div>

        {editing && (
          <>
            {/* Pointer events rather than mouse events: the same handler then
                covers a trackpad drag and a touch drag, and setPointerCapture
                keeps the drag alive when the cursor outruns the 6px handle. */}
            <div
              className="split-handle"
              role="separator"
              aria-orientation="vertical"
              onPointerDown={(e) => {
                e.currentTarget.setPointerCapture(e.pointerId)
                const startX = e.clientX
                const startW = treeWidth
                const move = (ev: PointerEvent) =>
                  setTreeWidth(startW + ev.clientX - startX)
                const up = () => {
                  window.removeEventListener('pointermove', move)
                  window.removeEventListener('pointerup', up)
                }
                window.addEventListener('pointermove', move)
                window.addEventListener('pointerup', up)
              }}
              onDoubleClick={() => setTreeWidth(360)}
            />
            <Suspense fallback={<div className="placeholder">{t('편집기를 불러오는 중…')}</div>}>
              {/* Only the directory the file lives in — rereading every open folder
                  on every save would put a burst of readdir on the server for
                  one changed size. */}
              <EditorPane
                hostID={hostID}
                onError={onError}
                onSaved={(path) => void refreshDir(parentOf(path))}
                onDownload={(path) => void downloadOne(path)}
              />
            </Suspense>
          </>
        )}
      </div>

      <TransferPanel onError={onError} />

      {dialog?.kind === 'newFolder' && (
        <Prompt
          title={t('새 폴더')}
          value={input}
          onChange={setInput}
          busy={busy}
          onCancel={() => setDialog(null)}
          onSubmit={() => void act(() => MakeDir(hostID, joinPath(cwd, input)))}
        />
      )}

      {dialog?.kind === 'rename' && (
        <Prompt
          title={t('이름 변경')}
          value={input}
          onChange={setInput}
          busy={busy}
          onCancel={() => setDialog(null)}
          onSubmit={() => {
            const to = joinPath(cwd, input)
            void act(async () => {
              const res = await RenamePath(hostID, dialog.entry.path, to)
              // An open tab follows the file rather than being closed under the
              // user — closing it would drop unsaved edits, and saving it
              // afterwards would recreate the old path.
              if (res.ok) renameOpen(hostID, dialog.entry.path, to)
              return res
            })
          }}
        />
      )}

      {dialog?.kind === 'perms' && (
        <div className="scrim">
          <div className="dialog">
            <h2>{t('권한')}</h2>
            <p className="mono muted ellipsis">{dialog.entry.path}</p>
            <PermissionEditor perm={perm} onChange={setPerm} />
            <div className="dialog-actions">
              <button onClick={() => setDialog(null)}>{t('취소')}</button>
              <button
                className="primary"
                disabled={busy}
                onClick={() => void act(() => Chmod(hostID, dialog.entry.path, perm))}
              >
                {t('적용')}
              </button>
            </div>
          </div>
        </div>
      )}

      {dialog?.kind === 'delete' && (
        <div className="scrim">
          <div className="dialog">
            <h2>{t('삭제하시겠습니까?')}</h2>
            <p className="muted">{t('{n}개 항목이 영구히 삭제됩니다.', { n: dialog.entries.length })}</p>
            <div className="delete-list mono">
              {dialog.entries.map((e) => (
                <div key={e.path} className="ellipsis">
                  {e.isDir ? '▸ ' : '· '}
                  {e.path}
                </div>
              ))}
            </div>
            {/* The tabs are the one thing this dialog cannot show by listing
                paths: an unsaved edit lives only in the app, so deleting the
                file it belongs to destroys work that was never on the server. */}
            {dialog.unsaved.length > 0 && (
              <p className="warn-text">
                {t('{n}개 파일에 저장하지 않은 변경이 있습니다 — 삭제하면 그 편집본도 함께 사라집니다.', { n: dialog.unsaved.length })}
              </p>
            )}
            {dialog.protectedPath && (
              <>
                <p className="warn-text">
                  <strong>{dialog.protectedPath}</strong>{' '}
                  {t('는 보호된 경로입니다. 계속하려면 경로를 정확히 입력하세요.')}
                </p>
                <input
                  value={input}
                  onChange={(e) => setInput(e.target.value)}
                  placeholder={dialog.protectedPath}
                  spellCheck={false}
                />
              </>
            )}
            <div className="dialog-actions">
              <button onClick={() => setDialog(null)}>{t('취소')}</button>
              <button
                className="danger"
                disabled={busy}
                onClick={() => {
                  const paths = dialog.entries.map((e) => e.path)
                  void act(async () => {
                    const res = await DeletePaths(hostID, paths, true, input)
                    if (res.ok) forgetPaths(hostID, paths)
                    return res
                  })
                }}
              >
                {t('삭제')}
              </button>
            </div>
          </div>
        </div>
      )}

    </div>
  )
}

function Prompt({
  title,
  value,
  onChange,
  busy,
  onCancel,
  onSubmit,
}: {
  title: string
  value: string
  onChange: (v: string) => void
  busy: boolean
  onCancel: () => void
  onSubmit: () => void
}) {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => ref.current?.select(), [])
  return (
    <div className="scrim">
      <form
        className="dialog"
        onSubmit={(e) => {
          e.preventDefault()
          onSubmit()
        }}
      >
        <h2>{title}</h2>
        <input
          ref={ref}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          spellCheck={false}
        />
        <div className="dialog-actions">
          <button type="button" onClick={onCancel}>
            {t('취소')}
          </button>
          <button type="submit" className="primary" disabled={busy || !value.trim()}>
            {t('확인')}
          </button>
        </div>
      </form>
    </div>
  )
}

function parentOf(p: string): string {
  const i = p.lastIndexOf('/')
  return i <= 0 ? '/' : p.slice(0, i)
}

function joinPath(dir: string, name: string): string {
  const clean = name.trim().replace(/\/+$/, '')
  return dir === '/' ? `/${clean}` : `${dir}/${clean}`
}

/** Mirrors the Go guard closely enough to decide what the dialog shows. The
 *  authoritative check runs in Go — this only picks the wording. */
function isProtectedLike(p: string): boolean {
  const depth = p === '/' ? 0 : p.replace(/\/$/, '').split('/').length - 1
  if (depth <= 1) return true
  return /^\/(home|Users)\/[^/]+$/.test(p)
}
