import { useEffect, useRef, useState } from 'react'
import { Icon, type IconName } from './icons'
import { type ConnState, type HostView } from './ipc'
import { ShellControls } from './ShellControls'
import { k, t } from './i18n'
import { shortcutLabel } from './platform'

// The left rail (§4.1) — hosts and sections in one column.
//
// # Why the tabs moved here
//
// They were a horizontal strip of nine equal buttons above the content. Nine is
// too many for a strip: it gives every view the same weight, has no room to say
// anything about a view before it is opened, and spends a row of the window on
// nine words. A server tool is not a browser — the sections are a place you
// navigate, not documents you opened.
//
// Vertically there is room for grouping, so 파일 sits with 터미널 and not with
// 보안, and room for a view to report something about itself before you go
// there. It also gives the window a row back, which the file list and the
// editor both wanted.
//
// # Collapsed, it is still a rail
//
// Folding it to nothing meant the only way back to another host or another
// section was to unfold it again — so "give me the window" and "let me move
// around" became opposites. Collapsed it keeps a 52px strip: the sections as
// icons, and the current host as a button that drops the list. That is the
// whole reason the icons above exist.
//
// # Why the host list folds and the sections do not
//
// The list is worth a fifth of the window while choosing a server and nothing
// at all while reading code on it (T-34). The sections are how you get
// anywhere, so folding them away would be folding away the app.

const STATE_LABEL: Record<ConnState, string> = {
  disconnected: k('연결 안 됨'),
  connecting: k('연결 중'),
  connected: k('연결됨'),
  reconnecting: k('재연결 중'),
}

function StateDot({ state }: { state: ConnState }) {
  return <span className="dot" data-state={state} title={t(STATE_LABEL[state])} />
}

/** One navigable view. `unsupported` means the server cannot serve it — said
 *  here rather than by grey-out, because grey tells nobody why. */
export type Section = { id: string; label: string; unsupported?: boolean }
export type SectionGroup = { label: string; items: Section[] }

export function Rail({
  hosts,
  activeID,
  onSelect,
  onConnect,
  onDisconnect,
  onImport,
  onAdd,
  onEdit,
  busy,
  version,
  onOpenMCP,
  listOpen,
  onToggleList,
  groups,
  current,
  onNavigate,
  selfMode,
  onHide,
  onShow,
  collapsed,
}: {
  hosts: HostView[]
  activeID: string | null
  onSelect: (id: string) => void
  onConnect: (id: string) => void
  onDisconnect: (id: string) => void
  onImport: () => void
  onAdd: () => void
  onEdit: (h: HostView) => void
  busy: boolean
  // Undefined until Bootstrap returns. Rendered as a dash rather than hidden —
  // a missing version reads as "the app failed to start properly", which is
  // information too.
  version?: string
  onOpenMCP: () => void
  /** Whether the host list is unfolded. The sections below it are always shown. */
  listOpen: boolean
  onToggleList: () => void
  /** Empty until a host is connected — there is nowhere to navigate to yet. */
  groups: SectionGroup[]
  current: string | null
  onNavigate: (id: string) => void
  /** "This server" mode shows one machine and has no host list to give. */
  selfMode?: boolean
  /** Folds the rail down to the icon strip. */
  onHide: () => void
  onShow: () => void
  /** Showing the icon strip rather than the full column. */
  collapsed: boolean
}) {
  const active = hosts.find((h) => h.id === activeID)

  if (collapsed) {
    return (
      <CollapsedRail
        {...{ hosts, activeID, active, onSelect, onConnect, groups, current, onNavigate, onShow, selfMode }}
      />
    )
  }

  return (
    <aside className="rail">
      {/* Always rendered, self mode included: it carries the control that folds
          the rail away, and a control that vanishes with the thing it controls
          is a setting people cannot find their way out of. */}
      <div className="rail-head">
        {!selfMode && (
          <button
            className="rail-fold"
            onClick={onToggleList}
            aria-expanded={listOpen}
            title={listOpen ? t('호스트 목록 접기') : t('호스트 목록 펼치기')}
          >
            <span className="twisty" aria-hidden="true">
              {listOpen ? '▾' : '▸'}
            </span>
            {t('호스트')}
          </button>
        )}
        <span className="spacer" />
        {!selfMode && listOpen && (
          <>
            <button
              className="ghost small-btn"
              onClick={onImport}
              disabled={busy}
              title={t('~/.ssh/config 가져오기')}
            >
              {t('가져오기')}
            </button>
            <button className="ghost small-btn" onClick={onAdd} disabled={busy} title={t('호스트 추가')}>
              {t('+ 추가')}
            </button>
          </>
        )}
        <button
          className="ghost icon-btn"
          onClick={onHide}
          title={`${t('레일 접기')} (${shortcutLabel('toggleRail')})`}
          aria-label={t('레일 접기')}
        >
          «
        </button>
      </div>

      {!selfMode && (
        <>

          {/* Folded, the rail still says which machine every section below is
              about. Losing that was the one thing the old full-width collapse
              got wrong: the tab strip stayed but stopped saying whose it was. */}
          {!listOpen && active && (
            <button className="rail-current" onClick={onToggleList} title={t('호스트 목록 펼치기')}>
              <StateDot state={active.state} />
              <span className="ellipsis">{active.name || active.hostname}</span>
            </button>
          )}

          {listOpen && hosts.length === 0 && (
            <div className="empty">
              <p>{t('등록된 호스트가 없습니다.')}</p>
              <p className="muted small">
                <code>~/.ssh/config</code> {t('를 가져오거나 직접 추가하세요.')}
              </p>
            </div>
          )}

          {listOpen && <HostList {...{ hosts, activeID, onSelect, onConnect, onDisconnect, onEdit, busy }} />}
        </>
      )}

      {groups.length > 0 && (
        <nav className="rail-nav" aria-label={t('화면')}>
          {groups.map((g) => (
            <div className="rail-group" key={g.label}>
              <div className="rail-group-label">{t(g.label)}</div>
              {g.items.map((s) => (
                <button
                  key={s.id}
                  className="rail-item"
                  data-on={s.id === current || undefined}
                  data-unsupported={s.unsupported || undefined}
                  aria-current={s.id === current ? 'page' : undefined}
                  onClick={() => onNavigate(s.id)}
                >
                  <Icon name={s.id as IconName} />
                  {t(s.label)}
                </button>
              ))}
            </div>
          ))}
        </nav>
      )}

      {!selfMode && (
        <div className="rail-foot">
          <ShellControls version={version} onOpenMCP={onOpenMCP} />
        </div>
      )}
    </aside>
  )
}

function HostList({
  hosts,
  activeID,
  onSelect,
  onConnect,
  onDisconnect,
  onEdit,
  busy,
}: {
  hosts: HostView[]
  activeID: string | null
  onSelect: (id: string) => void
  onConnect: (id: string) => void
  onDisconnect: (id: string) => void
  onEdit: (h: HostView) => void
  busy: boolean
}) {
  const groups = new Map<string, HostView[]>()
  for (const h of hosts) {
    const key = h.group || ''
    const list = groups.get(key) ?? []
    list.push(h)
    groups.set(key, list)
  }

  return (
    <div className="host-list">
      {[...groups.entries()].map(([group, list]) => (
        <div key={group || '_'}>
          {group && <div className="group-label">{group}</div>}
          {list.map((h) => {
            const connected = h.state === 'connected'
            return (
              <div
                key={h.id}
                className="host-row"
                data-active={h.id === activeID || undefined}
                onClick={() => onSelect(h.id)}
              >
                <StateDot state={h.state} />
                <div className="host-text">
                  <div className="host-name ellipsis">{h.name || h.hostname}</div>
                  <div className="host-addr ellipsis muted">
                    {h.user}@{h.hostname}
                    {h.port && h.port !== 22 ? `:${h.port}` : ''}
                  </div>
                </div>
                <div className="host-actions">
                  <button
                    className="ghost small-btn"
                    disabled={busy}
                    title={t('편집')}
                    onClick={(e) => {
                      e.stopPropagation()
                      onEdit(h)
                    }}
                  >
                    ⋯
                  </button>
                  <button
                    className="ghost small-btn"
                    disabled={busy || h.state === 'connecting'}
                    onClick={(e) => {
                      e.stopPropagation()
                      connected ? onDisconnect(h.id) : onConnect(h.id)
                    }}
                  >
                    {connected ? t('끊기') : t('접속')}
                  </button>
                </div>
              </div>
            )
          })}
        </div>
      ))}
    </div>
  )
}

/**
 * The rail folded down to 52px.
 *
 * Everything here is one of two things: where am I, and where can I go. No
 * import, no add, no version — those are errands, and somebody who folded the
 * rail away to read code is not running errands.
 */
function CollapsedRail({
  hosts,
  activeID,
  active,
  onSelect,
  onConnect,
  groups,
  current,
  onNavigate,
  onShow,
  selfMode,
}: {
  hosts: HostView[]
  activeID: string | null
  active?: HostView
  onSelect: (id: string) => void
  onConnect: (id: string) => void
  groups: SectionGroup[]
  current: string | null
  onNavigate: (id: string) => void
  onShow: () => void
  selfMode?: boolean
}) {
  const [open, setOpen] = useState(false)
  const pop = useRef<HTMLDivElement>(null)

  // Closing on an outside click and on Escape, because a menu that only closes
  // by picking something is a menu you cannot back out of.
  useEffect(() => {
    if (!open) return
    const away = (e: MouseEvent) => {
      if (!pop.current?.contains(e.target as Node)) setOpen(false)
    }
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    document.addEventListener('mousedown', away)
    document.addEventListener('keydown', esc)
    return () => {
      document.removeEventListener('mousedown', away)
      document.removeEventListener('keydown', esc)
    }
  }, [open])

  return (
    <aside className="rail rail-mini">
      <button
        className="ghost icon-btn"
        onClick={onShow}
        title={`${t('레일 펼치기')} (${shortcutLabel('toggleRail')})`}
        aria-label={t('레일 펼치기')}
      >
        »
      </button>

      {!selfMode && (
        <div className="rail-mini-host" ref={pop}>
          <button
            className="rail-mini-btn"
            data-on={open || undefined}
            onClick={() => setOpen((v) => !v)}
            aria-haspopup="menu"
            aria-expanded={open}
            title={active ? `${active.name || active.hostname} — ${t('호스트 바꾸기')}` : t('호스트 바꾸기')}
          >
            {active ? <StateDot state={active.state} /> : <Icon name="server" />}
          </button>
          {open && (
            <div className="rail-pop" role="menu">
              {hosts.map((h) => (
                <button
                  key={h.id}
                  role="menuitem"
                  className="rail-pop-item"
                  data-on={h.id === activeID || undefined}
                  onClick={() => {
                    onSelect(h.id)
                    // Switching to a host nobody has connected yet is almost
                    // always a request to connect to it. The list in the open
                    // rail keeps the two apart because it has room for two
                    // buttons; here there is room for one.
                    if (h.state === 'disconnected') onConnect(h.id)
                    setOpen(false)
                  }}
                >
                  <StateDot state={h.state} />
                  <span className="ellipsis">{h.name || h.hostname}</span>
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      <nav className="rail-mini-nav" aria-label={t('화면')}>
        {groups.map((g, i) => (
          <div className="rail-mini-group" key={g.label} data-first={i === 0 || undefined}>
            {g.items.map((s) => (
              <button
                key={s.id}
                className="rail-mini-btn"
                data-on={s.id === current || undefined}
                data-unsupported={s.unsupported || undefined}
                aria-current={s.id === current ? 'page' : undefined}
                onClick={() => onNavigate(s.id)}
                title={t(s.label)}
                aria-label={t(s.label)}
              >
                <Icon name={s.id as IconName} />
              </button>
            ))}
          </div>
        ))}
      </nav>
    </aside>
  )
}
