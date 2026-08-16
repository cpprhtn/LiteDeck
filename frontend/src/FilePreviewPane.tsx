import type { FilePreview } from './ipc'
import { t } from './i18n'

// A read-only look at a file the editor will not open (§4.2).
//
// Until this existed, opening a binary raised a toast and stopped there — the
// file could not be seen at all. Somebody who opens a .png on a server wants to
// look at it; somebody who opens an unknown blob wants to know what it is.
// Neither needs an editor, and neither is served by a refusal.

/** Bytes per row, chosen so the ASCII column lands where a hex dump puts it. */
const COLUMNS = 16

function fmtSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let n = bytes / 1024
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n < 10 ? n.toFixed(1) : Math.round(n)} ${units[i]}`
}

function decode(b64: string): Uint8Array {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

/** One `offset  hex…  ascii` line per 16 bytes, as `xxd` would print it. */
function hexRows(bytes: Uint8Array) {
  const rows = []
  for (let at = 0; at < bytes.length; at += COLUMNS) {
    const slice = bytes.subarray(at, at + COLUMNS)
    const hex = Array.from(slice, (b) => b.toString(16).padStart(2, '0'))
    // Padded so the ASCII column stays put on a short final row.
    while (hex.length < COLUMNS) hex.push('  ')
    rows.push({
      offset: at.toString(16).padStart(8, '0'),
      hex: hex.join(' '),
      // Anything outside printable ASCII becomes a dot, which is what makes the
      // column readable at all.
      ascii: Array.from(slice, (b) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '·')).join(''),
    })
  }
  return rows
}

export function FilePreviewPane({
  preview,
  onDownload,
}: {
  preview: FilePreview
  onDownload: () => void
}) {
  if (preview.tooLarge) {
    return (
      <div className="preview-pane">
        <div className="placeholder">
          {t('{size} — 미리보기 한도(8MB)를 넘습니다. 받아서 여세요.', {
            size: fmtSize(preview.size),
          })}
        </div>
        <div className="preview-foot">
          <button onClick={onDownload}>{t('다운로드…')}</button>
        </div>
      </div>
    )
  }

  if (preview.kind === 'image') {
    return (
      <div className="preview-pane">
        {/* Checkerboard behind it so a transparent PNG does not look like a
            broken load on a dark background. */}
        <div className="preview-image">
          <img src={`data:${preview.mime};base64,${preview.data}`} alt={preview.path} />
        </div>
        <div className="preview-foot">
          <span className="muted small mono">
            {preview.mime} · {fmtSize(preview.size)}
          </span>
          <span className="grow" />
          <button onClick={onDownload}>{t('다운로드…')}</button>
        </div>
      </div>
    )
  }

  const rows = hexRows(decode(preview.data))
  return (
    <div className="preview-pane">
      <div className="preview-hex mono">
        {rows.map((r) => (
          <div key={r.offset} className="preview-hex-row">
            <span className="preview-hex-off">{r.offset}</span>
            <span className="preview-hex-bytes">{r.hex}</span>
            <span className="preview-hex-ascii">{r.ascii}</span>
          </div>
        ))}
      </div>
      <div className="preview-foot">
        <span className="muted small mono">
          {preview.mime} · {fmtSize(preview.size)}
        </span>
        {preview.truncated && (
          <span className="muted small">
            {t('앞부분 {n} 만 표시합니다.', { n: fmtSize(rows.length * COLUMNS) })}
          </span>
        )}
        <span className="grow" />
        <button onClick={onDownload}>{t('다운로드…')}</button>
      </div>
    </div>
  )
}
