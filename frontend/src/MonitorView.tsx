import { useState } from 'react'
import { EventTimeline } from './EventTimeline'
import { ResourceView } from './ResourceView'
import { t } from './i18n'

// The monitoring tab (§4.7, arch/07).
//
// One tab rather than several, because the question underneath is one question:
// is this box all right, and if not, since when. Resources answer the first
// half and events the second, and splitting them across the tab strip would
// make the user assemble the answer themselves.
//
// Sub-tabs rather than one scrolling page: the event list runs to hundreds of
// rows, and anything placed below it would never be seen.
//
// There is no separate "curves" section by design. When sar arrives (T-16) it
// belongs to the range selector inside Resources — LiteDeck's own samples are
// the fine recent end and sar is the coarse older end of the *same* chart, with
// the seam shown rather than hidden.

type Pane = 'resources' | 'events'

export function MonitorView({
  hostID,
  hasEvents,
  cpuModel,
  onError,
}: {
  hostID: string
  /** No systemd, no journal, no event pane. The resource pane still works. */
  hasEvents: boolean
  /** From detection, not from the poll — it cannot change while connected. */
  cpuModel?: string
  onError: (msg: string) => void
}) {
  const [pane, setPane] = useState<Pane>('resources')
  const active = pane === 'events' && !hasEvents ? 'resources' : pane

  return (
    <div className="view monitor-pane">
      <div className="monitor-bar">
        {/* The same control the container tab uses for its two panes, so the
            two read as the same kind of switch. */}
        <div className="segmented">
          <button
            data-on={active === 'resources' || undefined}
            onClick={() => setPane('resources')}
          >
            {t('리소스')}
          </button>
          {hasEvents && (
            <button data-on={active === 'events' || undefined} onClick={() => setPane('events')}>
              {t('이벤트')}
            </button>
          )}
        </div>
      </div>

      {active === 'resources' ? (
        <ResourceView hostID={hostID} cpuModel={cpuModel} />
      ) : (
        <EventTimeline hostID={hostID} onError={onError} />
      )}
    </div>
  )
}
