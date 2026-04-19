import { Area, AreaChart, ResponsiveContainer } from 'recharts'
import { useChartTheme } from '@/lib/chartTheme'

interface SparklineProps {
  data: number[]
  height?: number
  // Uses the theme's "direction" palette when provided; defaults to primary.
  tone?: 'primary' | 'success' | 'danger' | 'warning'
  ariaLabel?: string
}

// Sparkline: tiny decorative area chart for trend cards. Not interactive —
// hover/tooltips live on the full chart a click away. `aria-label` is what
// a screen reader announces; data points are otherwise unlabeled.
export function Sparkline({ data, height = 32, tone = 'primary', ariaLabel }: SparklineProps) {
  const theme = useChartTheme()

  if (data.length < 2) {
    return <div style={{ height }} aria-hidden />
  }

  const color =
    tone === 'success' ? theme.success :
    tone === 'danger' ? theme.danger :
    tone === 'warning' ? theme.warning :
    theme.primary

  const id = `spark-${tone}-${data.length}`
  const chartData = data.map((v, i) => ({ i, v }))

  return (
    <div style={{ height }} role="img" aria-label={ariaLabel ?? 'Trend sparkline'}>
      <ResponsiveContainer width="100%" height={height}>
        <AreaChart data={chartData} margin={{ top: 2, right: 0, left: 0, bottom: 0 }}>
          <defs>
            <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity={0.2} />
              <stop offset="100%" stopColor={color} stopOpacity={0} />
            </linearGradient>
          </defs>
          <Area
            type="monotone"
            dataKey="v"
            stroke={color}
            strokeWidth={1.5}
            fill={`url(#${id})`}
            dot={false}
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}
