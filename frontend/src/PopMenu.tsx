import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

// A menu anchored to a button but rendered outside it (§4.7-1).
//
// Absolute positioning inside the card was not enough. The card list is a
// scroll container, so it clips on both axes: a menu anchored to the right edge
// of a button on the leftmost card extends left, off the container, and arrives
// with a slice missing. A card near the bottom loses the menu's lower half the
// same way.
//
// So: a portal to the body, position:fixed, and coordinates measured from the
// trigger. Nothing between the two can clip it, and the menu is free to flip up
// or slide sideways to stay on screen.

export function PopMenu({
  anchor,
  onClose,
  children,
}: {
  anchor: HTMLElement | null
  onClose: () => void
  children: ReactNode
}) {
  const ref = useRef<HTMLDivElement>(null)
  // Off-screen until measured: one frame at the wrong coordinates reads as the
  // menu jumping.
  const [at, setAt] = useState<{ top: number; left: number } | null>(null)

  useLayoutEffect(() => {
    const el = ref.current
    if (!anchor || !el) return
    const a = anchor.getBoundingClientRect()
    const m = el.getBoundingClientRect()
    const gap = 6
    const edge = 8

    // Right-aligned to the trigger, as a menu under a caret should be, then
    // pulled back inside the window if that put it off either side.
    let left = a.right - m.width
    left = Math.max(edge, Math.min(left, window.innerWidth - m.width - edge))

    // Below unless there is no room, in which case above.
    let top = a.bottom + gap
    if (top + m.height > window.innerHeight - edge) {
      const above = a.top - gap - m.height
      top = above >= edge ? above : Math.max(edge, window.innerHeight - m.height - edge)
    }
    setAt({ top, left })
  }, [anchor])

  useEffect(() => {
    const away = (e: MouseEvent) => {
      // The trigger handles its own toggle; closing here too would reopen it.
      if (ref.current?.contains(e.target as Node)) return
      if (anchor?.contains(e.target as Node)) return
      onClose()
    }
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    // The list underneath scrolls and the window resizes; either leaves the
    // menu pointing at nothing, and moving it would be chasing the pointer.
    const gone = () => onClose()
    document.addEventListener('mousedown', away, true)
    document.addEventListener('keydown', esc)
    window.addEventListener('resize', gone)
    window.addEventListener('scroll', gone, true)
    return () => {
      document.removeEventListener('mousedown', away, true)
      document.removeEventListener('keydown', esc)
      window.removeEventListener('resize', gone)
      window.removeEventListener('scroll', gone, true)
    }
  }, [anchor, onClose])

  return createPortal(
    <div
      ref={ref}
      className="pop-menu"
      role="menu"
      style={at ? { top: at.top, left: at.left } : { top: -9999, left: -9999 }}
    >
      {children}
    </div>,
    document.body,
  )
}
