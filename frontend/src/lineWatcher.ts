// Watching what is being typed into a terminal, so `code .` can be answered by
// the app before it is sent anywhere (§4.6a).
//
// In its own module because the wiring around it is where the bug was, not the
// matching: the first version fed it everything xterm produced, and xterm
// produces more than keystrokes.

/** The commands the app answers itself. `vim` is deliberately not among them. */
const CAUGHT = /^\s*(code|vi)(?:\s+(.*))?$/

export type CaughtCommand = { command: string; arg: string }

/**
 * Reconstructs the line being typed, and claims it only when it is certain.
 *
 * Two things it must never do. It must not swallow a line the user meant to
 * run — so anything the shell interprets for itself (an arrow key recalling
 * history, a Tab completing a name) makes it give up on that line. And it must
 * not mistake the terminal's own traffic for typing: when a shell asks where
 * the cursor is, xterm answers `\x1b[1;1R` down the very same channel as
 * keystrokes, and reading that as "the user pressed something odd" made it
 * abandon every line. bash asks at every prompt, so `code .` never once worked.
 */
export class LineWatcher {
  private buf = ''
  private blind = false

  /**
   * @param fromKeyboard false for anything xterm generated on its own — a
   *   cursor-position report, a device-attributes reply. Those never reach the
   *   shell's input line, so they say nothing about what is on it.
   * @returns the command to handle, or null to send the input on as usual.
   */
  feed(data: string, atPrompt: boolean, fromKeyboard: boolean): CaughtCommand | null {
    if (!fromKeyboard) return null

    if (data === '\r' || data === '\n') {
      const line = this.buf
      const blind = this.blind
      this.buf = ''
      this.blind = false
      if (blind || !atPrompt) return null
      const m = CAUGHT.exec(line)
      return m ? { command: m[1], arg: m[2] ?? '' } : null
    }
    // Backspace, the one edit that can be tracked exactly.
    if (data === '\x7f' || data === '\b') {
      this.buf = this.buf.slice(0, -1)
      return null
    }
    // Anything the shell will act on rather than insert. Escape sequences carry
    // history and completion; C0 controls carry Ctrl-C, Ctrl-R and the rest.
    if (/[\x00-\x1f]/.test(data)) {
      this.blind = true
      return null
    }
    this.buf += data
    return null
  }
}
