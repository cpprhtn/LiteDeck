import { useEffect, useRef, type ReactNode } from 'react'

// The backdrop every dialog sits on.
//
// # Why it is a component and not a div
//
// It was a div — sixteen of them, hand-written in nine files, and three of them
// handled Escape. So closing a dialog depended on which dialog it was, and the
// rename box in the file list was one of the ones you could only leave by
// finding the Cancel button with the mouse.
//
// Escape closing a dialog is not a feature, it is the floor. Making it a
// component is what makes it true everywhere at once and true for the next
// dialog somebody adds.
//
// # Why a stack
//
// Dialogs nest — the editor opens a confirm over its own sheet, the host editor
// opens one over its form. A plain window listener would close both at once,
// so each backdrop registers on mount and only the topmost answers.

const stack: symbol[] = []

export function Scrim({
  onClose,
  children,
  /** Clicking the backdrop closes it too. Off for anything destructive: a
   *  stray click outside a delete confirmation should not be an answer. */
  clickAway = true,
}: {
  /** Undefined for a dialog with nothing to cancel to — a prompt that must be
   *  answered. Then Escape does nothing, deliberately. */
  onClose?: () => void
  children: ReactNode
  clickAway?: boolean
}) {
  const id = useRef(Symbol('scrim'))

  useEffect(() => {
    const me = id.current
    stack.push(me)
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (stack[stack.length - 1] !== me) return
      e.preventDefault()
      e.stopPropagation()
      onClose?.()
    }
    // Capture, so a text field inside the dialog cannot swallow it first.
    window.addEventListener('keydown', onKey, true)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      const i = stack.lastIndexOf(me)
      if (i >= 0) stack.splice(i, 1)
    }
  }, [onClose])

  return (
    <div
      className="scrim"
      role="presentation"
      onMouseDown={(e) => {
        // Only the backdrop itself. Without the target check, releasing a drag
        // that started inside the dialog closes it.
        if (clickAway && e.target === e.currentTarget) onClose?.()
      }}
    >
      {children}
    </div>
  )
}
