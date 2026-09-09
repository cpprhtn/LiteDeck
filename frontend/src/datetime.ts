// Timestamps.
//
// # Why these are not locale-formatted
//
// They used to be. Four components each called `toLocaleString()` with their
// own options and no locale, which means the *webview's* locale — not the app's.
// A Korean UI on a machine set to en-US showed `8/24/2026, 1:45:00 AM` next to
// Korean labels, and switching the app to English changed nothing, because the
// language setting was never consulted.
//
// The obvious repair is to pass `getLanguage()`. This does something better for
// this particular app: it pins the shape.
//
//   2026-09-08 08:12
//
// That is what `journalctl`, `last`, `fail2ban.log` and every other thing this
// app reads already print. It is unambiguous — 08-09 is a different day in
// London and in Chicago, 2026-09-08 is not — it sorts, and it lines up in a
// column with `tabular-nums`. A person reading a server's logs beside this app
// should not have to translate between two date formats to match a row to a
// line.
//
// So the language chooses the words in this app, and the machine's own
// convention chooses the numbers. Anything with words in it — "3분 전",
// "아직 열려 있음" — still goes through t().
//
// Everything here renders in the reader's own timezone. They are reading it
// here, not there; the host's timezone is shown separately in the system panel.

/** What a date that is not a date renders as. One em dash, everywhere. */
const NOT_A_TIME = '—'

function parse(at: string | number | Date): Date | null {
  const d = at instanceof Date ? at : new Date(at)
  return Number.isFinite(d.getTime()) ? d : null
}

const pad = (n: number) => String(n).padStart(2, '0')

/**
 * `2026-09-08 08:12`, or with seconds `2026-09-08 08:12:34`.
 *
 * The full form. Use it where the row could be from any year — ban logs,
 * boot times, anything a person might read months later.
 */
export function stamp(at: string | number | Date, seconds = false): string {
  const d = parse(at)
  if (!d) return NOT_A_TIME
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${clockOf(d, seconds)}`
}

/**
 * `09-08 08:12` this year, `2025-09-08 08:12` otherwise.
 *
 * For lists dense enough that the year is noise on every row and news on one.
 */
export function shortStamp(at: string | number | Date, seconds = false): string {
  const d = parse(at)
  if (!d) return NOT_A_TIME
  if (d.getFullYear() !== new Date().getFullYear()) return stamp(d, seconds)
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${clockOf(d, seconds)}`
}

/** `08:12` or `08:12:34`. For axes and for rows whose day is already known. */
export function clock(at: string | number | Date, seconds = false): string {
  const d = parse(at)
  return d ? clockOf(d, seconds) : NOT_A_TIME
}

function clockOf(d: Date, seconds: boolean): string {
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  return seconds ? `${hm}:${pad(d.getSeconds())}` : hm
}
