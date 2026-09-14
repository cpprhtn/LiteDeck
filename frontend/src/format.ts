import { t } from './i18n'

// One place for the shapes numbers take on screen.
//
// There were five `fmtBytes`, three `fmtElapsed` and two `fmtUptime`, and they
// did not agree: the same gigabyte read `1.5G` in the summary bar, `1.5GB` in
// the transfer panel and `1.5 GB` in the image list, and one of them took
// kilobytes while the rest took bytes. Two screens side by side then disagreed
// about the size of the same file.
//
// Two byte functions rather than one, and the reason is not indecision. The
// kernel reports memory, disk and RSS in binary units; Docker reports image and
// volume sizes in decimal ones. Showing Docker's number in binary would make
// this app disagree with `docker images` about a figure the user can check.

/** Binary units, compact. For anything the kernel measured: memory, disk, RSS. */
export function bytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0B'
  const units = ['B', 'K', 'M', 'G', 'T', 'P']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  // One decimal below ten, none above: "9.4G" carries information, "947.3M"
  // carries a digit nobody reads.
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)}${units[i]}`
}

/** Decimal units, spelled out. For sizes Docker reported, so the two agree. */
export function siBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)} GB`
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)} MB`
  if (n >= 1e3) return `${(n / 1e3).toFixed(0)} kB`
  return `${Math.round(n)} B`
}

/** A process's runtime, clock-style. Not translated: `2:07` is the shape `ps`
 *  and every task manager print, and it reads the same in any language. */
export function runtime(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}`
  return `${m}:${String(Math.floor(sec % 60)).padStart(2, '0')}`
}

/** How long the machine has been up, in words. Translated, because it is prose
 *  rather than a reading. */
export function uptime(sec: number): string {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return t('{d}일 {h}시간', { d, h })
  if (h > 0) return t('{h}시간 {m}분', { h, m })
  return t('{m}분', { m })
}
