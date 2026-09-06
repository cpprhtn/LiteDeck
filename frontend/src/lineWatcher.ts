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

export type CaughtCommand = {
  command: string
  arg: string
  /** Exactly what was on the input line, so it can be erased character by
   *  character. Cancelling it needs the count, not the text. */
  line: string
}

/**
 * A line the user pressed Enter on (T-22).
 *
 * `blind` is the whole point of this type. The watcher reconstructs a line from
 * keystrokes, and the moment the shell interprets one for itself — an arrow key
 * recalling history, Tab completing a name, Ctrl-R searching — what ends up on
 * the line is no longer knowable from this side. Those lines are reported with
 * `blind` set and **no text**, because a history recall is a very ordinary way
 * to run a command and pretending not to have seen it would be worse than
 * saying so: a recalled `cd` that goes unrecorded silently moves every path
 * recorded after it.
 */
export type EnteredLine = {
  line: string
  blind: boolean
}

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

  /**
   * Called for every completed line, whether or not it could be read.
   *
   * Set by whoever wants the history; unset it costs nothing. It fires before
   * the interception check, so a `code .` the app answers itself is still a
   * line the user ran.
   */
  onEnter: ((entered: EnteredLine) => void) | null = null

  /** Returns the command to handle, or null to send the input on as usual. */
  feed(data: string, atPrompt: boolean): CaughtCommand | null {
    // Not typing: the terminal talking to the shell on its own account.
    if (TERMINAL_REPLY.test(data)) return null

    if (data === '\r' || data === '\n') {
      const line = this.buf
      const blind = this.blind
      this.buf = ''
      this.blind = false
      // Reported even when it could not be read, and even when the shell was
      // not at a prompt — an answer typed into `less` is not a command, but
      // "something was entered here" is still true, and the blind flag is what
      // the caller uses to decide.
      this.onEnter?.({ line: blind ? '' : line, blind: blind || !atPrompt })
      if (blind || !atPrompt) return null
      const m = CAUGHT.exec(line)
      return m ? { command: m[1], arg: m[2] ?? '', line } : null
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
