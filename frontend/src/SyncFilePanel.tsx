import { useState } from 'react'
import {
  SyncExportFile,
  SyncImportFile,
  SyncPickFile,
  SyncPreviewFile,
  type SyncFilePreview,
} from './ipc'
import { t } from './i18n'
import { stamp } from './datetime'

// Settings as a file, for people who do not have a git repository (§9.1).
//
// # Why this is here at all
//
// The repository path asks somebody to make a private repository and register a
// deploy key with write access. That is four sentences of vocabulary before the
// feature does anything, and most people who run two servers have never done it.
// A file has none of that: they already have somewhere that syncs itself —
// Drive, Dropbox, iCloud, a stick — and this produces something to put in it.
//
// # Why it still asks for a passphrase
//
// Because of where the file goes. A cloud folder is indexed by somebody else's
// software and reachable by somebody else's account recovery. The file holds no
// passwords and no keys, but it holds every server's address and account name,
// and that is a map of what to attack.
//
// # Why importing is two steps
//
// Choose the file, then say what is in it, then apply. A single "import" button
// would merge somebody's whole host list into yours on one click, and the thing
// people most want to know first is "what is in this, and what will it change".

export function SyncFilePanel({ onError }: { onError: (msg: string) => void }) {
  const [mode, setMode] = useState<'export' | 'import'>('export')
  const [pass, setPass] = useState('')
  const [pass2, setPass2] = useState('')
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState<{ path: string; hosts: number } | null>(null)
  const [path, setPath] = useState('')
  const [preview, setPreview] = useState<SyncFilePreview | null>(null)
  const [done, setDone] = useState<{ received: number; pending: number } | null>(null)

  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    try {
      await fn()
    } catch (e) {
      onError(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mcp-tabbody">
      <div className="sync-choices sync-choices-row">
        <button
          data-on={mode === 'export' || undefined}
          onClick={() => {
            setMode('export')
            setDone(null)
          }}
        >
          <strong>{t('파일로 내보내기')}</strong>
          <span className="muted small">{t('이 기기의 호스트를 파일 하나로')}</span>
        </button>
        <button
          data-on={mode === 'import' || undefined}
          onClick={() => {
            setMode('import')
            setSaved(null)
          }}
        >
          <strong>{t('파일에서 가져오기')}</strong>
          <span className="muted small">{t('다른 기기에서 내보낸 파일을 읽습니다')}</span>
        </button>
      </div>

      {mode === 'export' && (
        <>
          <p className="muted small">
            {t('호스트 목록과 승인 정책을 암호화된 파일 하나로 저장합니다. Google Drive·Dropbox·USB 어디에 두어도 됩니다 — 파일만으로는 아무도 읽을 수 없습니다.')}
          </p>
          <div className="form-grid">
            <label>{t('패스프레이즈 (12자 이상)')}</label>
            <input type="password" value={pass} onChange={(e) => setPass(e.target.value)} />
            <label>{t('한 번 더')}</label>
            <input type="password" value={pass2} onChange={(e) => setPass2(e.target.value)} />
          </div>
          {pass.length > 0 && pass.length < 12 && (
            <span className="badge warn">{t('12자 이상이어야 합니다')}</span>
          )}
          {pass2.length > 0 && pass !== pass2 && (
            <span className="badge warn">{t('두 값이 다릅니다')}</span>
          )}
          <p className="warn-text">
            {t('이 패스프레이즈를 잃으면 파일을 열 수 없습니다. LiteDeck 도 열지 못합니다.')}
          </p>
          <div className="sync-copy">
            <button
              className="primary small-btn"
              disabled={busy || pass.length < 12 || pass !== pass2}
              onClick={() =>
                void run(async () => {
                  const res = await SyncExportFile(pass)
                  // An empty path means the save dialog was closed. Nothing
                  // happened, and saying "saved" would be a lie about a file that
                  // does not exist.
                  if (res.path) setSaved(res)
                })
              }
            >
              {busy ? t('저장 중…') : t('파일로 저장')}
            </button>
            {saved && (
              <span className="badge ok">
                {t('호스트 {n}개를 저장했습니다', { n: saved.hosts })}
              </span>
            )}
          </div>
          {saved && <code className="mono selectable">{saved.path}</code>}
        </>
      )}

      {mode === 'import' && (
        <>
          <p className="muted small">
            {t('다른 기기에서 내보낸 파일을 읽습니다. 먼저 무엇이 들어 있는지 보여 주고, 적용은 그 다음입니다.')}
          </p>
          <div className="sync-copy">
            <button
              className="ghost small-btn"
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  const picked = await SyncPickFile()
                  if (picked) {
                    setPath(picked)
                    setPreview(null)
                    setDone(null)
                  }
                })
              }
            >
              {t('파일 고르기')}
            </button>
            {path && <code className="mono selectable">{path}</code>}
          </div>

          {path && (
            <>
              <div className="form-grid">
                <label>{t('패스프레이즈')}</label>
                <input
                  type="password"
                  value={pass}
                  onChange={(e) => {
                    setPass(e.target.value)
                    setPreview(null)
                  }}
                />
              </div>
              <div className="sync-copy">
                <button
                  className="small-btn"
                  disabled={busy || pass.length < 12}
                  onClick={() => void run(async () => setPreview(await SyncPreviewFile(path, pass)))}
                >
                  {t('열어 보기')}
                </button>
              </div>
            </>
          )}

          {preview && (
            <>
              <p className="muted small">
                {t('{when} 에 기기 {device} 에서 만든 파일입니다', {
                  when: stamp(preview.createdAt * 1000),
                  device: (preview.device ?? '').slice(0, 8) || '—',
                })}
              </p>
              <div className="sync-filelist">
                {preview.hosts.map((h) => (
                  <div key={h.id}>
                    <strong>{h.name}</strong>
                    <code className="mono">{h.addr}</code>
                    <span className="badge">
                      {h.state === 'new'
                        ? t('새 호스트')
                        : h.state === 'same'
                          ? t('같음')
                          : t('덮어씀')}
                    </span>
                    {h.loosens && <span className="badge warn">{t('정책은 확인 대기')}</span>}
                  </div>
                ))}
              </div>
              <div className="sync-copy">
                <button
                  className="primary small-btn"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      const res = await SyncImportFile(path, pass)
                      setDone({ received: res.received, pending: res.pending })
                      setPreview(null)
                    })
                  }
                >
                  {t('가져오기')}
                </button>
              </div>
            </>
          )}

          {done && (
            <span className="badge ok">
              {t('호스트 {n}개를 받았습니다 · 확인 필요 {p}', {
                n: done.received,
                p: done.pending,
              })}
            </span>
          )}
        </>
      )}
    </div>
  )
}
