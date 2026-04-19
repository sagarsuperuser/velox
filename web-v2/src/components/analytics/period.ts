export type Period = '7d' | '30d' | '90d' | '12m'

export const PERIOD_LABELS: Record<Period, string> = {
  '7d': '7 days',
  '30d': '30 days',
  '90d': '90 days',
  '12m': '12 months',
}
