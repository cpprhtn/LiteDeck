import { Suspense, lazy, useEffect, useRef, useState, type ReactNode } from 'react'
import { CodeEditor } from './CodeEditor'
import { detectLanguage } from './editorLanguage'
import { ReadTextFile, SaveTextFile, type TextFile } from './ipc'
import {
  closeFile,
  isDirty,
  markSaved,
  rebase,
  resetTo,
  setActive,
  setDoc,
  useOpenFiles,
  type OpenFile,
} from './openFiles'

// The editor half of the file view (§4.7-3): tabs across the top, one document
// below, and a save that shows its work first.
//
// Saving goes through a diff every time. It costs one keypress — the dialog
// confirms on Enter — and it is the difference between "I changed the port" and
// "I changed the port and dropped a block I did not mean to touch", which look
// the same in an editor and very different in a diff. The Command Log makes the
// same trade for commands.
const DiffView = lazy(() => import('./DiffView').then((m) => ({ default: m.DiffView })))

type Confirm =
  /** `thenClose` carries the intent through the diff and, on a clash, the
   *  conflict dialog after it — "save and close" has to still close. */
  | { kind: 'save'; file: OpenFile; thenClose?: boolean }
  /** The server's copy moved while this tab was open. `server` is what is there now. */
  | { kind: 'conflict'; file: OpenFile; server: TextFile; thenClose?: boolean }
  | { kind: 'close'; file: OpenFile }

export function EditorPane({
  hostID,
  onError,
  onSaved,
}: {
  hostID: string
  onError: (msg: string) => void
  /** Called with the saved path so the tree can reread just that directory. */
  onSaved: (path: string) => void
}) {
  const { files, active } = useOpenFiles(hostID)
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [cursor, setCursor] = useState({ line: 1, col: 1 })

  const file = files.find((f) => f.path === active) ?? files[0] ?? null

  // Cmd+S reaches here through CodeMirror's keymap, and the handler it was
  // built with is captured once — so it reads the current file from a ref
  // rather than from a stale closure.
  const latest = useRef(file)
  latest.current = file

  useEffect(() => setNotice(null), [file?.path])

  if (!file) return null

  const requestSave = (f: OpenFile) => {
    if (!isDirty(f)) {
      setNotice('바뀐 내용이 없습니다.')
      return
    }
    setConfirm({ kind: 'save', file: f })
  }

  const commitSave = async (f: OpenFile, force: boolean, thenClose?: boolean) => {
    setBusy(true)
    try {
      const res = await SaveTextFile(hostID, {
        path: f.path,
        content: f.doc,
        baseModTime: f.baseModTime,
        baseSize: f.baseSize,
        force,
      })

      if (res.conflict) {
        // Reread rather than describe: the useful answer to "it changed" is
        // *what* it now says, and the dialog can diff against it.
        const server = await ReadTextFile(hostID, f.path).catch(() => null)
        if (!server || server.binary || server.tooLarge) {
          onError(res.error ?? '파일이 서버에서 바뀌었습니다')
          setConfirm(null)
          return
        }
        setConfirm({ kind: 'conflict', file: f, server, thenClose })
        return
      }

      if (!res.ok) {
        onError(res.error ?? '저장하지 못했습니다')
        setConfirm(null)
        return
      }

      markSaved(hostID, f.path, res.modTime, res.size)
      setConfirm(null)
      setNotice(
        res.inPlace
          ? '저장했습니다 — 다만 이 디렉터리에 임시 파일을 만들 수 없어 파일에 직접 썼습니다. 저장 도중 연결이 끊기면 파일이 잘릴 수 있습니다.'
          : '저장했습니다.',
      )
      onSaved(f.path)
      // Only after the write landed. Closing on the way to a failed save is how
      // "save and close" quietly becomes "close".
      if (thenClose) closeFile(hostID, f.path)
    } catch (e) {
      onError(String(e))
      setConfirm(null)
    } finally {
      setBusy(false)
    }
  }

  const revertToServer = (f: OpenFile, server: TextFile) => {
    resetTo(hostID, f.path, server)
    setConfirm(null)
    setNotice('서버의 내용으로 되돌렸습니다.')
  }

  const requestClose = (f: OpenFile) => {
    if (isDirty(f)) {
      setConfirm({ kind: 'close', file: f })
      return
    }
    closeFile(hostID, f.path)
  }

  const lang = detectLanguage(file.path)

  return (
    <div className="editor-pane">
      <div className="editor-tabs" role="tablist">
        {files.map((f) => (
          <button
            key={f.path}
            role="tab"
            className="editor-tab"
            data-on={f.path === file.path || undefined}
            data-dirty={isDirty(f) || undefined}
            title={f.path}
            onClick={() => setActive(hostID, f.path)}
            onAuxClick={(e) => {
              // Middle click closes, as it does everywhere else with tabs.
              if (e.button === 1) requestClose(f)
            }}
          >
            <span className="ellipsis">{f.name}</span>
            <span
              className="editor-tab-close"
              role="button"
              aria-label={`${f.name} 닫기`}
              onClick={(e) => {
                e.stopPropagation()
                requestClose(f)
              }}
            >
              {isDirty(f) ? '●' : '×'}
            </span>
          </button>
        ))}
      </div>

      <CodeEditor
        path={file.path}
        value={file.doc}
        onChange={(doc) => setDoc(hostID, file.path, doc)}
        onSave={() => latest.current && requestSave(latest.current)}
        onCursor={(line, col) => setCursor({ line, col })}
      />

      <div className="editor-status">
        <span className="mono ellipsis" title={file.path}>
          {file.path}
        </span>
        <span className="muted small">{lang?.label ?? '일반 텍스트'}</span>
        <span className="muted small mono">
          {cursor.line}:{cursor.col}
        </span>
        <span className="grow" />
        {notice && <span className="muted small ellipsis">{notice}</span>}
        <button disabled={busy || !isDirty(file)} onClick={() => requestSave(file)}>
          저장{isDirty(file) ? ' •' : ''}
        </button>
      </div>

      {confirm?.kind === 'save' && (
        <DiffDialog
          title="저장하기 전에"
          path={confirm.file.path}
          before={confirm.file.base}
          after={confirm.file.doc}
          busy={busy}
          confirmLabel="저장"
          onCancel={() => setConfirm(null)}
          onConfirm={() => void commitSave(confirm.file, false, confirm.thenClose)}
        />
      )}

      {confirm?.kind === 'conflict' && (
        <DiffDialog
          title="이 파일은 연 뒤에 서버에서 바뀌었습니다"
          detail="왼쪽이 지금 서버에 있는 내용, 오른쪽이 저장하려는 내용입니다. 덮어쓰면 왼쪽의 변경은 사라집니다."
          path={confirm.file.path}
          before={confirm.server.content}
          after={confirm.file.doc}
          busy={busy}
          confirmLabel="덮어쓰기"
          danger
          onCancel={() => {
            // Nothing was written, but the tab now knows what the server holds —
            // without this the next save conflicts against the same stale mtime
            // forever.
            rebase(hostID, confirm.file.path, confirm.server)
            setConfirm(null)
          }}
          onConfirm={() => void commitSave(confirm.file, true, confirm.thenClose)}
          extra={
            <button onClick={() => revertToServer(confirm.file, confirm.server)}>
              서버 내용으로 되돌리기
            </button>
          }
        />
      )}

      {confirm?.kind === 'close' && (
        <div className="scrim">
          <div className="dialog">
            <h2>저장하지 않고 닫으시겠습니까?</h2>
            <p className="mono muted ellipsis">{confirm.file.path}</p>
            <p className="muted">저장하지 않은 변경이 사라집니다.</p>
            <div className="dialog-actions">
              <button onClick={() => setConfirm(null)}>취소</button>
              <button
                className="danger"
                onClick={() => {
                  closeFile(hostID, confirm.file.path)
                  setConfirm(null)
                }}
              >
                닫기
              </button>
              <button
                className="primary"
                disabled={busy}
                onClick={() =>
                  setConfirm({ kind: 'save', file: confirm.file, thenClose: true })
                }
              >
                저장하고 닫기…
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function DiffDialog({
  title,
  detail,
  path,
  before,
  after,
  busy,
  confirmLabel,
  danger,
  extra,
  onCancel,
  onConfirm,
}: {
  title: string
  detail?: string
  path: string
  before: string
  after: string
  busy: boolean
  confirmLabel: string
  danger?: boolean
  extra?: ReactNode
  onCancel: () => void
  onConfirm: () => void
}) {
  const ok = useRef<HTMLButtonElement>(null)
  // Focused on open so the whole gesture is Cmd+S, Enter. A diff nobody can
  // dismiss quickly is a diff people learn to stop reading.
  useEffect(() => ok.current?.focus(), [])

  return (
    <div className="scrim">
      <div
        className="dialog diff-dialog"
        onKeyDown={(e) => {
          if (e.key === 'Escape') onCancel()
        }}
      >
        <h2>{title}</h2>
        <p className="mono muted ellipsis">{path}</p>
        {detail && <p className="warn-text">{detail}</p>}
        <Suspense fallback={<div className="placeholder">차이를 계산하는 중…</div>}>
          <DiffView path={path} before={before} after={after} />
        </Suspense>
        <div className="dialog-actions">
          <button onClick={onCancel}>취소</button>
          {extra}
          <button
            ref={ok}
            className={danger ? 'danger' : 'primary'}
            disabled={busy}
            onClick={onConfirm}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
