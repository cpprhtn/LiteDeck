import { DEFAULTS, getPref, setPref, type Prefs } from './prefs'

// The grab strip on the top edge of a bottom panel (§4.7-1).
//
// Out of flow on purpose: both panels that use it are grids or flex columns
// with a fixed set of children, and a handle in flow would take a row of its
// own. It straddles the border so the target is 6px tall while the line the
// user sees stays 1px.

export function ResizeHandle({
  pref,
  label,
}: {
  /** Which stored height this drag adjusts. */
  pref: keyof Prefs
  label: string
}) {
  return (
    <div
      className="resize-handle"
      role="separator"
      aria-orientation="horizontal"
      aria-label={label}
      onPointerDown={(e) => {
        // setPointerCapture keeps the drag alive once the cursor outruns the
        // strip, which it does immediately.
        e.currentTarget.setPointerCapture(e.pointerId)
        const startY = e.clientY
        const startH = getPref(pref)
        // Dragging up makes the panel taller, so the delta is negated.
        const move = (ev: PointerEvent) => setPref(pref, startH + startY - ev.clientY)
        const up = () => {
          window.removeEventListener('pointermove', move)
          window.removeEventListener('pointerup', up)
        }
        window.addEventListener('pointermove', move)
        window.addEventListener('pointerup', up)
      }}
      onDoubleClick={() => setPref(pref, DEFAULTS[pref])}
    />
  )
}
