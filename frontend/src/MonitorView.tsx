import { useState } from 'react'
import { CommandHistory } from './CommandHistory'
import { EventTimeline } from './EventTimeline'
import { ResourceView, type SysFacts } from './ResourceView'
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

// History sits here rather than in a tab of its own for the reason the other
// two share one: it reads the same journal, through the same permission answer,
// and it is the same kind of looking — what happened on this box, and when.
// Resources say "is it all right", events "since when", history "what did we do
// to it". A ninth entry in the tab strip would have split that question up.
type Pane = 'resources' | 'events' | 'history'

export function MonitorView({
  hostID,
  hasEvents,
  facts,
  onError,
}: {
  hostID: string
  /** No systemd, no journal, no event pane. The resource pane still works. */
  hasEvents: boolean
  /** From detection, not from the poll — none of it can change while the
   *  connection is up. */
  facts: SysFacts
  onError: (msg: string) => void
}) {
  const [pane, setPane] = useState<Pane>('resources')
  // Both journal panes need the journal. Falling back rather than hiding the
  // choice after the fact: the buttons are not rendered either.
  const needsJournal = pane === 'events' || pane === 'history'
  const active = needsJournal && !hasEvents ? 'resources' : pane

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
          {hasEvents && (
            <button data-on={active === 'history' || undefined} onClick={() => setPane('history')}>
              {t('이력')}
            </button>
          )}
        </div>
      </div>

      {active === 'resources' && <ResourceView hostID={hostID} facts={facts} />}
      {active === 'events' && <EventTimeline hostID={hostID} onError={onError} />}
      {active === 'history' && <CommandHistory hostID={hostID} onError={onError} />}
    </div>
  )
}
