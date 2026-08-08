import { Component, type ErrorInfo, type ReactNode } from 'react'

/**
 * Keeps one broken view from taking down the window.
 *
 * React unmounts the entire tree when a render throws. Without a boundary the
 * user gets a blank window — indistinguishable from a crash, because it is one,
 * and with no way to tell which part failed or to get back to a working tab.
 *
 * This is not a substitute for fixing the bug. It is what makes the bug
 * reportable: the message and stack stay on screen, the rest of the app keeps
 * working, and the user can copy the text into an issue (§11).
 */
export class ErrorBoundary extends Component<
  { children: ReactNode; label: string; onReset?: () => void },
  { error: Error | null; stack: string }
> {
  state = { error: null as Error | null, stack: '' }

  static getDerivedStateFromError(error: Error) {
    return { error, stack: '' }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    this.setState({ error, stack: info.componentStack ?? '' })
    // Also to the webview console, where `wails dev` shows it.
    console.error(`[${this.props.label}]`, error, info.componentStack)
  }

  render() {
    const { error, stack } = this.state
    if (!error) return this.props.children

    return (
      <div className="crash">
        <h3>{this.props.label} 뷰에서 오류가 발생했습니다</h3>
        <p className="muted small">
          다른 탭은 정상 동작합니다. 아래 내용을 그대로 이슈에 붙여주세요.
        </p>
        <pre className="crash-detail mono">
          {error.message}
          {stack && `\n${stack}`}
        </pre>
        <div className="dialog-actions">
          <button
            onClick={() => {
              void navigator.clipboard.writeText(`${error.message}\n${stack}`)
            }}
          >
            복사
          </button>
          <button
            className="primary"
            onClick={() => {
              this.setState({ error: null, stack: '' })
              this.props.onReset?.()
            }}
          >
            다시 시도
          </button>
        </div>
      </div>
    )
  }
}
