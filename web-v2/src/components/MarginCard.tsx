import { useMemo, useState } from 'react'
import { Loader2 } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { DatePicker } from '@/components/ui/date-picker'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { formatCents } from '@/lib/api'
import { endOfDayInTZ, startOfDayInTZ } from '@/lib/dates'
import { rollingWindow } from '@/lib/effectiveNow'
import { useGetV1CustomersIdMargin } from '@/lib/gen/queries.gen'
import { useEffectiveNow } from '@/hooks/useClockFrozenMap'

const WINDOW_PRESETS = [
  { key: '7d', label: 'Last 7 days', days: 7 },
  { key: '30d', label: 'Last 30 days', days: 30 },
  { key: '90d', label: 'Last 90 days', days: 90 },
  { key: 'custom', label: 'Custom', days: 0 },
] as const

// Margin (ADR-079): rated usage revenue vs stamped provider COGS over an
// operator-picked window (presets + custom dates; the API always took
// from/to — the UI just never exposed them, hardwiring last-30-days).
// Operator-only surface — COGS never renders on customer-facing pages.
// Attribution is honest by design: per-model margin shows only where
// pricing rules pin the model; everything else appears as "not
// model-attributed" rather than being split by heuristic.
export function MarginCard({ customerId, customerTestClockId }: { customerId: string; customerTestClockId?: string }) {
  const [preset, setPreset] = useState<typeof WINDOW_PRESETS[number]['key']>('30d')
  const [customFrom, setCustomFrom] = useState('')
  const [customTo, setCustomTo] = useState('')

  // For a clock-pinned customer, anchor the preset window's upper bound to the
  // clock's frozen_time: provider-costed usage events are stamped in simulation
  // time (usage/service.go), so a wall-clock "last 30 days" sits before them and
  // the card reads "$0.00 / Margin —". Custom absolute dates are left untouched.
  const now = useEffectiveNow(customerTestClockId)

  const params = useMemo(() => {
    if (preset === 'custom') {
      if (!customFrom || !customTo) return undefined // wait for both ends
      return { from: startOfDayInTZ(customFrom), to: endOfDayInTZ(customTo) }
    }
    const days = WINDOW_PRESETS.find(p => p.key === preset)?.days ?? 30
    return rollingWindow(days, now)
  }, [preset, customFrom, customTo, now])

  const { data: rep, isLoading } = useGetV1CustomersIdMargin(customerId, params, {
    query: { enabled: params !== undefined },
  })

  // micros (1e-6 dollars) → display dollars.
  const costDollars = (micros: number) => `$${(micros / 1_000_000).toFixed(micros >= 100_000 ? 2 : 4)}`

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between gap-3 flex-wrap">
          <div>
            <CardTitle>Margin</CardTitle>
            <CardDescription>
              Usage billing vs your provider costs. Excludes base fees, credits,
              and taxes — this is usage unit economics, not accounting margin.
            </CardDescription>
          </div>
          <div className="flex items-center gap-2">
            <select
              aria-label="Margin window"
              className="h-8 rounded-md border border-input bg-transparent px-2 text-sm"
              value={preset}
              onChange={e => setPreset(e.target.value as typeof preset)}
            >
              {WINDOW_PRESETS.map(p => (
                <option key={p.key} value={p.key}>{p.label}</option>
              ))}
            </select>
            {preset === 'custom' && (
              <>
                <DatePicker value={customFrom} onChange={setCustomFrom} placeholder="From" className="w-40" />
                <DatePicker value={customTo} onChange={setCustomTo} placeholder="To" className="w-40"
                  minDate={customFrom ? new Date(customFrom + 'T00:00:00') : undefined} />
              </>
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {isLoading || !rep ? (
          <div className="flex justify-center p-6">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
          </div>
        ) : (
          <>
            <div className="grid grid-cols-3 gap-4">
              <div>
                <p className="text-xs text-muted-foreground uppercase tracking-wide">Usage billed</p>
                <p className="text-lg font-semibold text-foreground mt-0.5">{formatCents(rep.revenue_cents, 'USD')}</p>
              </div>
              <div>
                <p className="text-xs text-muted-foreground uppercase tracking-wide">Provider cost</p>
                <p className="text-lg font-semibold text-foreground mt-0.5">{costDollars(rep.cost_micros)}</p>
              </div>
              <div>
                <p className="text-xs text-muted-foreground uppercase tracking-wide">Margin</p>
                <p className="text-lg font-semibold text-foreground mt-0.5">
                  {rep.margin_bps != null ? `${(rep.margin_bps / 100).toFixed(1)}%` : '—'}
                </p>
              </div>
            </div>

            {rep.by_model.length > 0 && (
              <Table className="mt-4">
                <TableHeader>
                  <TableRow>
                    <TableHead>Model</TableHead>
                    <TableHead className="text-right">Billed</TableHead>
                    <TableHead className="text-right">Cost</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rep.by_model.map(m => (
                    <TableRow key={m.model}>
                      <TableCell className="text-sm font-mono">{m.model}</TableCell>
                      <TableCell className="text-sm text-right">
                        {m.attributed
                          ? formatCents(m.revenue_cents ?? 0, 'USD')
                          : <Badge variant="outline">not model-priced</Badge>}
                      </TableCell>
                      <TableCell className="text-sm text-right">{costDollars(m.cost_micros)}</TableCell>
                    </TableRow>
                  ))}
                  {rep.unattributed_revenue_cents > 0 && (
                    <TableRow>
                      <TableCell className="text-sm text-muted-foreground">Not model-attributed</TableCell>
                      <TableCell className="text-sm text-right">{formatCents(rep.unattributed_revenue_cents, 'USD')}</TableCell>
                      <TableCell className="text-sm text-right text-muted-foreground">—</TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            )}

            {rep.unresolved_events > 0 && (
              <p className="text-xs text-muted-foreground mt-3">
                {rep.unresolved_events.toLocaleString()} usage events have no matching
                provider rate — they were recorded before a rate existed or their model
                isn't in your Provider costs table. New events are costed automatically
                once a rate matches.
              </p>
            )}
            {rep.by_model.length === 0 && rep.revenue_cents === 0 && (
              <p className="text-sm text-muted-foreground mt-2">
                No usage in the window yet. Add provider rates under Provider costs,
                send usage, and margin appears here.
              </p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  )
}
