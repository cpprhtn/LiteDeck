// Watching what is being typed into a terminal, so `code .` can be answered by
// the app before it is sent anywhere (§4.6a).
//
// In its own module because the wiring around it is where the bugs were, not
// the matching.

/** The commands the app answers itself. `vim` is deliberately not among them. */
const CAUGHT = /^\s*(code|vi)(?:\s+(.*))?$/

/**
 * Sequences the terminal sends *back* to the shell, answering questions the
 * shell asked it. These arrive on the same channel as keystrokes and are not
 * typing.
 *
 * The one that matters is the cursor position report: bash asks where the
 * cursor is at every prompt, and xterm replies `\x1b[1;1R`. Reading that as a
 * keypress is what stopped this from ever working.
 *
 * Recognised by shape rather than by asking xterm whether a key event was
 * involved. That question has a different answer in a browser than in the
 * webview the app actually ships in, and depending on it made the feature work
 * everywhere it was tested and nowhere it was used. A reply always ends in one
 * of these final bytes; no key on a keyboard produces them — arrows end in
 * A–D, navigation keys in `~`, function keys in P–S.
 */
const TERMINAL_REPLY =
  /^\x1b(?:\[[?>]?[0-9;]*[Rcnt]|P[^\x1b]*\x1b\\|\][^\x07]*\x07)$/

export type CaughtCommand = { command: string; arg: string }

/**
 * Reconstructs the line being typed, and claims it only when it is certain.
 *
 * It must never swallow a line the user meant to run, so anything the shell
 * interprets for itself — an arrow key recalling history, a Tab completing a
 * name — makes it give up on that line and pass everything through. Missing an
 * interception costs nothing; taking one wrongly costs the user their command.
 */
export class LineWatcher {
  private buf = ''
  private blind = false

  /** Returns the command to handle, or null to send the input on as usual. */
  feed(data: string, atPrompt: boolean): CaughtCommand | null {
    // Not typing: the terminal talking to the shell on its own account.
    if (TERMINAL_REPLY.test(data)) return null

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
    // Anything else the shell will act on rather than insert. Escape sequences
    // carry history and completion; C0 controls carry Ctrl-C, Ctrl-R and the
    // rest. Any of them and this line is no longer ours to reconstruct.
    if (/[\x00-\x1f]/.test(data)) {
      this.blind = true
      return null
    }
    this.buf += data
    return null
  }
}
