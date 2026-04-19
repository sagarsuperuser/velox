import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { type Period, PERIOD_LABELS } from './period'

interface PeriodTabsProps {
  value: Period
  onChange: (p: Period) => void
  periods?: Period[]
}

export function PeriodTabs({ value, onChange, periods = ['7d', '30d', '90d', '12m'] }: PeriodTabsProps) {
  return (
    <Tabs value={value} onValueChange={v => onChange(v as Period)}>
      <TabsList>
        {periods.map(p => (
          <TabsTrigger key={p} value={p}>{PERIOD_LABELS[p]}</TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  )
}
