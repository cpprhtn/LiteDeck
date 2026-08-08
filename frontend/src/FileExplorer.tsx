import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
  ReadTextFile,
  RenamePath,
  StartDownload,
  StartUpload,
  WriteTextFile,
  type DirListing,
  type FileEntry,
  type TextFile,
} from './ipc'
import { on } from './ipc'
import { matches, shortcutLabel } from './platform'

// The file explorer (§4.2).
//
// No polling: a directory listing does not change under you often enough to
// justify it, and the server pays for every poll (§3.2d). Refresh happens on
// navigation, after an action, or on demand.

const ROW_HEIGHT = 26
const COLUMNS = '24px 1fr 100px 110px 150px'

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

function icon(e: FileEntry): string {
  if (e.broken) return '⚠'
  if (e.isSymlink) return '↗'
  if (e.isDir) return '▸'
  return '·'
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
    { label: '소유자', base: 6 },
    { label: '그룹', base: 3 },
    { label: '기타', base: 0 },
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
  | { kind: 'delete'; entries: FileEntry[]; protectedPath: string | null }
  | { kind: 'perms'; entry: FileEntry }
  | { kind: 'editor'; file: TextFile }

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
  const [editorText, setEditorText] = useState('')
  const [busy, setBusy] = useState(false)
  const [history, setHistory] = useState<string[]>([])
  const [dropping, setDropping] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)

  const load = useCallback(
    async (dir: string) => {
      setBusy(true)
      try {
        const l = await ListDir(hostID, dir)
        setListing(l)
        setCwd(l.path)
        setAddressBar(l.path)
        setSelected(new Set())
      } catch (e) {
        onError(String(e))
      } finally {
        setBusy(false)
      }
    },
    [hostID, onError],
  )

  useEffect(() => {
    ;(async () => {
      try {
        await load(await HomeDir(hostID))
      } catch (e) {
        onError(String(e))
      }
    })()
  }, [hostID, load, onError])

  const navigate = (dir: string) => {
    if (cwd) setHistory((h) => [...h.slice(-30), cwd])
    void load(dir)
  }

  const back = () => {
    setHistory((h) => {
      if (h.length === 0) return h
      void load(h[h.length - 1])
      return h.slice(0, -1)
    })
  }

  const refresh = () => void load(cwd)

  const rows = useMemo(() => {
    const all = listing?.entries ?? []
    return showHidden ? all : all.filter((e) => !e.name.startsWith('.'))
  }, [listing, showHidden])

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
  })

  const selectedEntries = rows.filter((e) => selected.has(e.path))

  const open = async (e: FileEntry) => {
    if (e.isDir) {
      navigate(e.path)
      return
    }
    try {
      const file = await ReadTextFile(hostID, e.path)
      if (file.tooLarge) {
        onError(`${e.name}: ${fmtSize(file.size, false)} — 편집기 한도(2MB)를 넘습니다`)
        return
      }
      if (file.binary) {
        onError(`${e.name}: 바이너리 파일이라 편집할 수 없습니다`)
        return
      }
      setEditorText(file.content)
      setDialog({ kind: 'editor', file })
    } catch (err) {
      onError(String(err))
    }
  }

  const act = async (fn: () => Promise<{ ok: boolean; error?: string }>) => {
    setBusy(true)
    try {
      const res = await fn()
      if (!res.ok) {
        onError(res.error ?? '실패했습니다')
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
      if (dialog) return
      const one = selectedEntries[0]
      if (matches(ev, 'refresh')) {
        ev.preventDefault()
        refresh()
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

  const download = async () => {
    if (selectedEntries.length === 0) {
      onError('다운로드할 항목을 선택하세요')
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
          <strong>{cwd}</strong> 에 업로드합니다
        </div>
      )}
      <div className="view-toolbar">
        <button className="ghost" disabled={history.length === 0} onClick={back}>
          ←
        </button>
        <button
          className="ghost"
          disabled={!listing || cwd === '/'}
          onClick={() => listing && navigate(listing.parent)}
          title={`상위 폴더 (${shortcutLabel('parentDir')})`}
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
        <label className="checkbox" style={{ margin: 0 }}>
          <input
            type="checkbox"
            checked={showHidden}
            onChange={(e) => setShowHidden(e.target.checked)}
          />
          숨김
        </label>
        <button className="ghost" onClick={refresh} title={shortcutLabel('refresh')}>
          새로고침
        </button>
      </div>

      <div className="view-toolbar">
        <button
          onClick={() => {
            setInput('')
            setDialog({ kind: 'newFolder' })
          }}
        >
          새 폴더
        </button>
        <button onClick={() => void upload()}>업로드…</button>
        <button disabled={selectedEntries.length === 0} onClick={() => void download()}>
          다운로드…
        </button>
        <button
          disabled={selectedEntries.length !== 1}
          onClick={() => {
            const e = selectedEntries[0]
            setInput(e.name)
            setDialog({ kind: 'rename', entry: e })
          }}
        >
          이름 변경
        </button>
        <button
          disabled={selectedEntries.length !== 1}
          onClick={() => {
            const e = selectedEntries[0]
            setPerm(e.perm)
            setDialog({ kind: 'perms', entry: e })
          }}
        >
          권한
        </button>
        <button
          className="danger"
          disabled={selectedEntries.length === 0}
          onClick={() => void askDelete()}
        >
          삭제
        </button>
        {listing?.protected && (
          <span className="badge warn" title="루트 직계 디렉터리 — 재귀 삭제 시 경로 입력이 필요합니다">
            보호된 경로
          </span>
        )}
        {listing?.truncated && (
          <span className="badge warn">{listing.total}개 중 일부만 표시</span>
        )}
      </div>

      <div className="table">
        <div className="thead" style={{ gridTemplateColumns: COLUMNS }}>
          <div />
          <div>NAME</div>
          <div className="num">SIZE</div>
          <div>MODE</div>
          <div>MODIFIED</div>
        </div>
        <div className="tbody" ref={scrollRef}>
          {busy && !listing && <div className="placeholder">읽는 중…</div>}
          {listing && rows.length === 0 && (
            <div className="placeholder">비어 있는 디렉터리입니다.</div>
          )}
          <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
            {virtualizer.getVirtualItems().map((item) => {
              const e = rows[item.index]
              return (
                <div
                  key={e.path}
                  className="trow selectable-row"
                  data-selected={selected.has(e.path) || undefined}
                  style={{
                    gridTemplateColumns: COLUMNS,
                    height: ROW_HEIGHT,
                    transform: `translateY(${item.start}px)`,
                  }}
                  onClick={(ev) => {
                    setSelected((prev) => {
                      const next = ev.metaKey || ev.ctrlKey ? new Set(prev) : new Set<string>()
                      next.has(e.path) ? next.delete(e.path) : next.add(e.path)
                      return next
                    })
                  }}
                  onDoubleClick={() => void open(e)}
                >
                  <div className="muted">{icon(e)}</div>
                  <div className="ellipsis">
                    {e.name}
                    {e.isSymlink && e.linkTarget && (
                      <span className="muted"> → {e.linkTarget}</span>
                    )}
                    {e.broken && <span className="badge danger">끊긴 링크</span>}
                  </div>
                  <div className="num mono">{fmtSize(e.size, e.isDir)}</div>
                  <div className="mono muted">{e.mode}</div>
                  <div className="mono muted">{fmtTime(e.modTime)}</div>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      <TransferPanel onError={onError} />

      {dialog?.kind === 'newFolder' && (
        <Prompt
          title="새 폴더"
          value={input}
          onChange={setInput}
          busy={busy}
          onCancel={() => setDialog(null)}
          onSubmit={() => void act(() => MakeDir(hostID, joinPath(cwd, input)))}
        />
      )}

      {dialog?.kind === 'rename' && (
        <Prompt
          title="이름 변경"
          value={input}
          onChange={setInput}
          busy={busy}
          onCancel={() => setDialog(null)}
          onSubmit={() =>
            void act(() => RenamePath(hostID, dialog.entry.path, joinPath(cwd, input)))
          }
        />
      )}

      {dialog?.kind === 'perms' && (
        <div className="scrim">
          <div className="dialog">
            <h2>권한</h2>
            <p className="mono muted ellipsis">{dialog.entry.path}</p>
            <PermissionEditor perm={perm} onChange={setPerm} />
            <div className="dialog-actions">
              <button onClick={() => setDialog(null)}>취소</button>
              <button
                className="primary"
                disabled={busy}
                onClick={() => void act(() => Chmod(hostID, dialog.entry.path, perm))}
              >
                적용
              </button>
            </div>
          </div>
        </div>
      )}

      {dialog?.kind === 'delete' && (
        <div className="scrim">
          <div className="dialog">
            <h2>삭제하시겠습니까?</h2>
            <p className="muted">{dialog.entries.length}개 항목이 영구히 삭제됩니다.</p>
            <div className="delete-list mono">
              {dialog.entries.map((e) => (
                <div key={e.path} className="ellipsis">
                  {e.isDir ? '▸ ' : '· '}
                  {e.path}
                </div>
              ))}
            </div>
            {dialog.protectedPath && (
              <>
                <p className="warn-text">
                  <strong>{dialog.protectedPath}</strong> 는 보호된 경로입니다. 계속하려면
                  경로를 정확히 입력하세요.
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
              <button onClick={() => setDialog(null)}>취소</button>
              <button
                className="danger"
                disabled={busy}
                onClick={() =>
                  void act(() =>
                    DeletePaths(
                      hostID,
                      dialog.entries.map((e) => e.path),
                      true,
                      input,
                    ),
                  )
                }
              >
                삭제
              </button>
            </div>
          </div>
        </div>
      )}

      {dialog?.kind === 'editor' && (
        <div className="scrim">
          <div className="dialog wide">
            <h2 className="ellipsis mono">{dialog.file.path}</h2>
            <textarea
              className="editor mono"
              value={editorText}
              onChange={(e) => setEditorText(e.target.value)}
              spellCheck={false}
            />
            <div className="dialog-actions">
              <button onClick={() => setDialog(null)}>닫기</button>
              <button
                className="primary"
                disabled={busy}
                onClick={() =>
                  void act(() => WriteTextFile(hostID, dialog.file.path, editorText))
                }
              >
                저장
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
            취소
          </button>
          <button type="submit" className="primary" disabled={busy || !value.trim()}>
            확인
          </button>
        </div>
      </form>
    </div>
  )
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
