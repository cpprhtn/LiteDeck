// The icon set.
//
// # Why these are drawn here rather than installed
//
// Nine glyphs. A library would be a dependency, a licence, a build step and
// ~40KB to pick nine paths out of a thousand, and this app ships one bundle
// with no CDN — the artifact would have to carry the whole set.
//
// # Why not emoji
//
// 📁 and 📄 are in the file listing today and they are the wrong tool: they are
// somebody else's colour at somebody else's weight, they render differently on
// every platform, and they cannot take `currentColor` — so a folder stays
// yellow when the row is selected and the text goes white. These are strokes in
// currentColor at a single weight, which is the only way a rail can show what
// is selected.
//
// Drawn on a 16×16 grid, aligned to half-pixels so the verticals land on device
// pixels. The rendered size comes from CSS (.icon), not from the element: at
// 16px the globe was a blob, two people were mush and the service stack was
// indistinguishable from the server rack. They are 24px now, and the ones that
// carried the most detail per pixel were redrawn with less of it.
//
// The stroke is 1.2 on the 16 grid, which lands at 1.8 device px at 24 — the
// weight the 13px label beside it is set at. Scaling a glyph without scaling
// its stroke down is how icons end up shouting over their own text.

const box = {
  viewBox: '0 0 16 16',
  className: 'icon',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.2,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
  'aria-hidden': true,
}

export type IconName =
  | 'files'
  | 'terminal'
  | 'services'
  | 'processes'
  | 'containers'
  | 'network'
  | 'sessions'
  | 'security'
  | 'monitor'
  | 'server'

export function Icon({ name }: { name: IconName }) {
  switch (name) {
    case 'files':
      return (
        <svg {...box}>
          <path d="M1.75 4.25v8a1 1 0 0 0 1 1h10.5a1 1 0 0 0 1-1v-6a1 1 0 0 0-1-1H8L6.5 3.25H2.75a1 1 0 0 0-1 1Z" />
        </svg>
      )
    case 'terminal':
      return (
        <svg {...box}>
          <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="1" />
          <path d="M4.5 6.5 6.5 8l-2 1.5M8.5 10h3" />
        </svg>
      )
    case 'services':
      // Units with a state each, not a cog — a cog says "settings" in every
      // other app. Three rows rather than the rack's two, and the dots sit
      // outside the rows, so it does not read as the server icon at a glance.
      return (
        <svg {...box}>
          <circle cx="3" cy="3.75" r="1.15" />
          <circle cx="3" cy="8" r="1.15" />
          <circle cx="3" cy="12.25" r="1.15" />
          <path d="M6.75 3.75h7.5M6.75 8h7.5M6.75 12.25h7.5" />
        </svg>
      )
    case 'processes':
      return (
        <svg {...box}>
          <path d="M1.75 8h2.5l2-4.5 3 9 2-4.5h2.5" />
        </svg>
      )
    case 'containers':
      return (
        <svg {...box}>
          <path d="M8 1.75 14.25 5v6L8 14.25 1.75 11V5Z" />
          <path d="M1.75 5 8 8.25 14.25 5M8 8.25v6" />
        </svg>
      )
    case 'network':
      return (
        <svg {...box}>
          <circle cx="8" cy="8" r="6.25" />
          <path d="M1.75 8h12.5" />
          <ellipse cx="8" cy="8" rx="2.9" ry="6.25" />
        </svg>
      )
    case 'sessions':
      return (
        <svg {...box}>
          <circle cx="6" cy="5.25" r="2.6" />
          <path d="M1.25 13.5c0-2.5 2.05-4 4.75-4s4.75 1.5 4.75 4" />
          <circle cx="12.1" cy="5.75" r="1.9" />
          <path d="M12.1 9.5c1.65.35 2.65 1.6 2.65 4" />
        </svg>
      )
    case 'security':
      return (
        <svg {...box}>
          <path d="M8 1.75 13.25 4v4c0 3-2.2 5.2-5.25 6.25C4.95 13.2 2.75 11 2.75 8V4Z" />
          <path d="m5.75 8 1.6 1.6 3-3.2" />
        </svg>
      )
    case 'monitor':
      return (
        <svg {...box}>
          <path d="M1.75 13.25V2.75M1.75 13.25h12.5" />
          <path d="m4.25 10.5 2.5-3 2.25 2 3.25-4.5" />
        </svg>
      )
    case 'server':
      return (
        <svg {...box}>
          <rect x="2.25" y="2.75" width="11.5" height="4" rx="1" />
          <rect x="2.25" y="9.25" width="11.5" height="4" rx="1" />
          <path d="M4.75 4.75h.01M4.75 11.25h.01" />
        </svg>
      )
  }
}
