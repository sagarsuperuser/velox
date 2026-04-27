// VeloxCostDashboard — typed React wrapper around the public cost-dashboard
// embed iframe. Renders /public/cost-dashboard/:token with theme + accent
// applied via the URL params documented in lib/embedTheme.ts.
//
// Status: in-repo component. A standalone npm package
// (`@velox/react`) is on the v1.1 roadmap — partners who can't fork the
// repo today should embed the iframe directly per /docs/embeds/cost-dashboard.
// This component exists so internal Velox surfaces and partners willing to
// vendor the source get a typed prop interface instead of hand-rolling URL
// construction.

import { useMemo } from 'react'

export interface VeloxCostDashboardProps {
  /** The per-customer embed token minted via POST /v1/customers/{id}/rotate-cost-dashboard-token. */
  token: string
  /** Origin where the Velox dashboard is hosted, e.g. "https://app.velox.dev". No trailing slash. */
  baseUrl: string
  /** Default "dark". Maps to ?theme=light|dark on the iframe URL. */
  theme?: 'dark' | 'light'
  /** Brand colour as 6-digit hex (e.g. "#10b981"). Overrides --primary + --ring. Anything not matching the hex regex is silently dropped server-side. */
  accent?: string
  /** Iframe width. Defaults to "100%". */
  width?: string | number
  /** Iframe height. Defaults to 600. */
  height?: string | number
  /** Optional className passed to the iframe element. */
  className?: string
  /** Optional title for accessibility. Defaults to "Cost dashboard". */
  title?: string
}

const HEX6 = /^#[0-9a-fA-F]{6}$/

export function VeloxCostDashboard(props: VeloxCostDashboardProps) {
  const { token, baseUrl, theme, accent, width = '100%', height = 600, className, title = 'Cost dashboard' } = props

  const src = useMemo(() => {
    const params = new URLSearchParams()
    if (theme === 'light') {
      params.set('theme', 'light')
    }
    if (accent && HEX6.test(accent)) {
      params.set('accent', accent)
    }
    const query = params.toString()
    const trimmed = baseUrl.replace(/\/+$/, '')
    return `${trimmed}/public/cost-dashboard/${encodeURIComponent(token)}${query ? `?${query}` : ''}`
  }, [token, baseUrl, theme, accent])

  return (
    <iframe
      src={src}
      width={width}
      height={height}
      className={className}
      title={title}
      frameBorder={0}
      loading="lazy"
      referrerPolicy="no-referrer"
    />
  )
}

export default VeloxCostDashboard
