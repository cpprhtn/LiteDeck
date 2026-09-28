import { useState } from 'react'
import { Scrim } from './Scrim'
import {
  SyncExportFile,
  SyncImportFile,
  SyncPickFile,
  SyncPreviewFile,
  type SyncFilePreview,
} from './ipc'
import { t } from './i18n'
import { stamp } from './datetime'

// Moving settings between machines, as one encrypted file.
//
// # Why a file
//
// Everybody already has somewhere that syncs itself — Drive, Dropbox, iCloud, a
// stick, an email to themselves. This produces something to put in it, and
// LiteDeck neither knows nor cares where it went.
//
// # Why it asks for a passphrase
//
// Because of where the file goes. A cloud folder is indexed by somebody else's
// software and reachable through somebody else's account recovery. The file holds
// no passwords and no keys, but it holds every server's address and account name,
// and that is a map of what to attack.
//
// # Why importing shows the contents first
//
// A single "import" button would merge somebody's whole host list into yours on
// one click. The thing people want to know first is what is in it and what it
// changes — and one line of that is a decision: whether the file's AI permissions
// come with it. That box is here, next to the list of what it would open up,
// rather than being a queue on another screen for somebody to find later.

export function SyncPanel({
  onClose,
  onError,
}: {
  onClose: () => void
  onError: (msg: string) => void
}) {
  const [mode, setMode] = useState<'export' | 'import'>('export')
  const [pass, setPass] = useState('')
  const [pass2, setPass2] = useState('')
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState<{ path: string; hosts: number } | null>(null)
  const [path, setPath] = useState('')
  const [preview, setPreview] = useState<SyncFilePreview | null>(null)
  const [withPermissions, setWithPermissions] = useState(false)
  const [done, setDone] = useState<{ received: number } | null>(null)

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

  const widening = (preview?.hosts ?? []).filter((h) => (h.widens?.length ?? 0) > 0)
  const clashes = (preview?.hosts ?? []).filter((h) => h.hostKeyClash)

  return (
    <Scrim onClose={onClose}>
      <div className="dialog mcp-dialog">
        <h2>{t('설정 동기화')}</h2>
        <p className="muted small">
          {t(
            '호스트 목록과 승인 정책을 암호화된 파일 하나로 내보내고, 다른 기기에서 그 파일을 읽습니다. 계정도 서버도 없습니다.',
          )}
        </p>
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
                    {h.hostKeyClash && (
                      <span className="badge warn" title={t('이 기기가 기억하는 키를 그대로 씁니다')}>
                        {t('호스트 키 다름')}
                      </span>
                    )}
                  </div>
                ))}
              </div>

              {/* The one decision in this screen, next to the list of what it
                  opens up. Off by default: a file can come from another person
                  as easily as from your own laptop, and the difference is not
                  something the app can see. */}
              {widening.length > 0 && (
                <label className="mcp-toggle">
                  <input
                    type="checkbox"
                    checked={withPermissions}
                    onChange={(e) => setWithPermissions(e.target.checked)}
                  />
                  <span>
                    {t('이 파일의 AI 권한 설정도 함께 적용 ({what})', {
                      what: Array.from(
                        new Set(widening.flatMap((h) => h.widens ?? [])),
                      ).join(' · '),
                    })}
                  </span>
                </label>
              )}
              {widening.length > 0 && !withPermissions && (
                <p className="muted small">
                  {t('체크하지 않으면 접속 정보만 들어오고, 권한은 이 기기의 설정을 그대로 둡니다.')}
                </p>
              )}
              {clashes.length > 0 && (
                <p className="muted small">
                  {t('호스트 키가 다른 서버가 있습니다. 파일의 키는 쓰지 않고 이 기기가 기억하는 키를 그대로 씁니다 — 서버를 다시 세운 것인지 중간에 누가 있는 것인지 파일로는 알 수 없습니다.')}
                </p>
              )}

              <div className="sync-copy">
                <button
                  className="primary small-btn"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      const res = await SyncImportFile(path, pass, withPermissions)
                      setDone({ received: res.received })
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
            <span className="badge ok">{t('호스트 {n}개를 받았습니다', { n: done.received })}</span>
          )}
        </>
      )}
        </div>

        <div className="dialog-actions">
          <button onClick={onClose}>{t('닫기')}</button>
        </div>
      </div>
    </Scrim>
  )
}
