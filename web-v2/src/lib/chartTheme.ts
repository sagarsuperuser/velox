import { useEffect, useState } from 'react'

// Reads a CSS custom property from :root, resolving var() references so the
// value can be passed straight to recharts (which doesn't resolve vars itself).
function readVar(name: string): string {
  if (typeof document === 'undefined') return ''
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export interface ChartTheme {
  primary: string
  success: string
  danger: string
  warning: string
  info: string
  neutral: string
  grid: string
  tick: string
  tooltipBg: string
  tooltipBorder: string
  tooltipText: string
}

function snapshot(): ChartTheme {
  return {
    primary: readVar('--chart-primary'),
    success: readVar('--chart-success'),
    danger: readVar('--chart-danger'),
    warning: readVar('--chart-warning'),
    info: readVar('--chart-info'),
    neutral: readVar('--chart-neutral'),
    grid: readVar('--chart-grid'),
    tick: readVar('--chart-tick'),
    tooltipBg: readVar('--chart-tooltip-bg'),
    tooltipBorder: readVar('--chart-tooltip-border'),
    tooltipText: readVar('--chart-tooltip-text'),
  }
}

// useChartTheme returns the resolved chart palette, re-subscribing to
// html.class changes so dark-mode toggles repaint charts without a remount.
export function useChartTheme(): ChartTheme {
  const [theme, setTheme] = useState<ChartTheme>(() => snapshot())

  useEffect(() => {
    const refresh = () => setTheme(snapshot())
    // Read once on mount in case custom props weren't applied yet on first render.
    refresh()

    const mo = new MutationObserver((mutations) => {
      for (const m of mutations) {
        if (m.type === 'attributes' && m.attributeName === 'class') {
          refresh()
          return
        }
      }
    })
    mo.observe(document.documentElement, { attributes: true })
    return () => mo.disconnect()
  }, [])

  return theme
}

// Shared tooltip style derived from the current theme.
export function tooltipStyle(theme: ChartTheme): React.CSSProperties {
  return {
    backgroundColor: theme.tooltipBg,
    border: `1px solid ${theme.tooltipBorder}`,
    borderRadius: 8,
    fontSize: 13,
    color: theme.tooltipText,
    boxShadow: '0 4px 12px rgb(0 0 0 / 0.08)',
  }
}
