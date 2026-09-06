import { DEFAULTS, getPref, setPref, type NumericPref } from './prefs'

// The grab strip on the edge of a panel (§4.7-1).
//
// Out of flow on purpose: the panels that use it are grids or flex containers
// with a fixed set of children, and a handle in flow would take a row or a
// column of its own. It straddles the border so the target is 6px while the
// line the user sees stays 1px.
//
// One component for both axes rather than two. The only difference is which
// coordinate is read and which way the delta points, and a second copy would
// be a second place for the pointer-capture handling to go subtly wrong.

export function ResizeHandle({
  pref,
  label,
  axis = 'y',
}: {
  /** Which stored size this drag adjusts. */
  pref: NumericPref
  label: string
  /** 'y' grows the panel upward from its top edge; 'x' grows it leftward from
   *  its left edge. Both are "drag away from the content". */
  axis?: 'x' | 'y'
}) {
  return (
    <div
      className="resize-handle"
      data-axis={axis}
      role="separator"
      aria-orientation={axis === 'x' ? 'vertical' : 'horizontal'}
      aria-label={label}
      onPointerDown={(e) => {
        // setPointerCapture keeps the drag alive once the cursor outruns the
        // strip, which it does immediately.
        e.currentTarget.setPointerCapture(e.pointerId)
        const start = axis === 'x' ? e.clientX : e.clientY
        const startSize = getPref(pref)
        // Dragging toward the content makes the panel bigger, so the delta is
        // negated on both axes.
        const move = (ev: PointerEvent) =>
          setPref(pref, startSize + start - (axis === 'x' ? ev.clientX : ev.clientY))
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
