// Web transport shim (arch/08).
//
// Under Wails this file does nothing: window.go and window.runtime are injected
// by the runtime before the bundle runs. Served over HTTP by cmd/litedeck-server
// it fills those two globals, so the *unchanged* ipc.ts — api() reads
// window.go.app.App, on() reads window.runtime.EventsOn — drives the same Go
// core over POST /rpc/<Method> and one WebSocket, instead of the in-process
// bridge. Those are the only two seams between the frontend and the backend, so
// filling them carries the whole UI.

import { t } from './i18n'

type Handler = (payload: unknown) => void

// A deployment that binds beyond loopback requires a token (see arch/08 / the
// server refusing 0.0.0.0 without --token). The browser cannot set an
// Authorization header on a WebSocket handshake and has no other channel, so it
// arrives in the URL: open http://host:port/?token=SECRET. Read once; the query
// string is not used for anything else. Behind a reverse proxy that does the
// auth there is no token and this stays empty.
const authToken = new URLSearchParams(window.location.search).get('token') ?? ''

function authHeaders(): Record<string, string> {
  return authToken ? { Authorization: `Bearer ${authToken}` } : {}
}

/** Appends the token to a URL for channels that cannot carry a header (WS). */
function withToken(url: string): string {
  if (!authToken) return url
  return url + (url.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(authToken)
}

// The path the app is served under, so a reverse proxy mounting it at
// `/litedeck/` still resolves `/litedeck/rpc/...` (the proxy must not strip the
// prefix, or bind the app at the root — see arch/08).
function basePath(): string {
  const p = window.location.pathname
  return p.endsWith('/') ? p : p.slice(0, p.lastIndexOf('/') + 1)
}

async function rpc(method: string, args: unknown[]): Promise<unknown> {
  const res = await fetch(`${basePath()}rpc/${method}`, {
    method: 'POST',
    // application/json is load-bearing: it forces a CORS preflight for any
    // cross-origin caller, which the server's Origin check then fails. A
    // same-origin call (the app itself) needs no preflight.
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(args),
  })

  let body: { result?: unknown; error?: string } = {}
  try {
    body = await res.json()
  } catch {
    /* empty or non-JSON body */
  }
  if (res.status === 401) {
    // The session expired (or was never established). Send the browser to the
    // login page rather than surfacing a raw 401 in the UI.
    window.location.href = `${basePath()}login`
    throw new Error(t('로그인이 필요합니다'))
  }
  if (!res.ok) {
    throw new Error(body.error ?? `RPC ${method}: HTTP ${res.status}`)
  }
  // A method that returned an error comes back 200 with an error key; reject the
  // promise exactly as Wails does when a bound method returns a non-nil error,
  // so a failed ConnectHost does not resolve as success.
  if (body.error !== undefined) {
    throw new Error(body.error)
  }
  return body.result
}

// EventBus turns the single event WebSocket into the per-event subscriptions
// window.runtime.EventsOn expects.
class EventBus {
  private handlers = new Map<string, Set<Handler>>()
  private retry = 0

  connect() {
    const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(withToken(`${scheme}://${window.location.host}${basePath()}ws`))

    ws.onopen = () => {
      this.retry = 0
    }
    ws.onmessage = (e) => {
      try {
        const { event, payload } = JSON.parse(e.data as string)
        this.handlers.get(event)?.forEach((h) => h(payload))
      } catch {
        /* ignore a malformed frame */
      }
    }
    ws.onclose = () => {
      // Events emitted while disconnected are lost; the UI re-fetches state on
      // reconnect, which it already tolerates on desktop (it re-lists after any
      // gap). Backoff caps so a down server does not become a busy loop.
      const delay = Math.min(1000 * 2 ** this.retry++, 15000)
      window.setTimeout(() => this.connect(), delay)
    }
    ws.onerror = () => ws.close()
  }

  on(event: string, cb: Handler): () => void {
    let set = this.handlers.get(event)
    if (!set) {
      set = new Set()
      this.handlers.set(event, set)
    }
    set.add(cb)
    return () => {
      set!.delete(cb)
    }
  }

  off(event: string) {
    this.handlers.delete(event)
  }
}

/**
 * Fill window.go / window.runtime when running as a served web page. A no-op
 * under Wails, which injected them already.
 */
export function installWebTransport() {
  if (window.go?.app?.App) return

  const app = new Proxy(
    {},
    {
      get(_t, prop) {
        if (typeof prop !== 'string') return undefined
        return (...args: unknown[]) => rpc(prop, args)
      },
    },
  )

  ;(window as unknown as { __litedeckWeb?: boolean }).__litedeckWeb = true

  const bus = new EventBus()
  bus.connect()

  ;(window as unknown as { go: unknown; runtime: unknown }).go = { app: { App: app } }
  ;(window as unknown as { go: unknown; runtime: unknown }).runtime = {
    EventsOn: (event: string, cb: (...data: unknown[]) => void) => bus.on(event, (p) => cb(p)),
    EventsOff: (event: string) => bus.off(event),
  }
}


/**
 * True when the UI is served over HTTP by cmd/litedeck-server rather than hosted
 * in a Wails webview. The upload flow branches on this: the desktop reaches the
 * local disk through a native file dialog; the web sends the browser's own File
 * objects to POST /upload.
 */
export function isWebMode(): boolean {
  return !!(window as unknown as { __litedeckWeb?: boolean }).__litedeckWeb
}

/** Streams picked files to the server's /upload, which SFTPs them to remoteDir. */
export async function uploadFiles(
  hostId: string,
  remoteDir: string,
  files: FileList | File[],
): Promise<void> {
  const form = new FormData()
  for (const f of Array.from(files)) form.append('file', f, f.name)
  const url = `${basePath()}upload?hostId=${encodeURIComponent(hostId)}&dir=${encodeURIComponent(remoteDir)}`
  const res = await fetch(url, { method: 'POST', body: form, headers: authHeaders() })
  if (!res.ok) {
    let msg = t('업로드 실패: HTTP {status}', { status: res.status })
    try {
      const j = await res.json()
      if (j.error) msg = j.error
    } catch {
      /* non-JSON */
    }
    throw new Error(msg)
  }
}
