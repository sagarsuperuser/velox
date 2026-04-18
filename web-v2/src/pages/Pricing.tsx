import { useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import {
  api,
  formatCents,
  formatDate,
  getCurrencySymbol,
  getActiveCurrency,
  type Meter,
  type Plan,
  type RatingRule,
} from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { statusBadgeVariant } from '@/lib/status'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Checkbox } from '@/components/ui/checkbox'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'

import { Plus, Trash2, Loader2 } from 'lucide-react'

function badgeVariant(val: string): 'success' | 'info' | 'secondary' | 'outline' {
  switch (val) {
    case 'active': return 'success'
    case 'monthly': return 'info'
    case 'yearly': return 'info'
    case 'flat': return 'info'
    case 'graduated': return 'info'
    case 'package': return 'info'
    default: return 'secondary'
  }
}

export default function PricingPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const tab = (['plans', 'meters', 'rules'].includes(searchParams.get('tab') || '')
    ? searchParams.get('tab')
    : 'plans') as 'plans' | 'meters' | 'rules'
  const setTab = (t: string) => setSearchParams(t === 'plans' ? {} : { tab: t })
  const [createFor, setCreateFor] = useState<'plans' | 'meters' | 'rules' | null>(null)
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const { data: plansData, isLoading: plansLoading, error: plansError } = useQuery({
    queryKey: ['plans'],
    queryFn: () => api.listPlans(),
  })
  const { data: metersData, isLoading: metersLoading, error: metersError } = useQuery({
    queryKey: ['meters'],
    queryFn: () => api.listMeters(),
  })
  const { data: rulesData, isLoading: rulesLoading, error: rulesError } = useQuery({
    queryKey: ['rating-rules'],
    queryFn: () => api.listRatingRules(),
  })

  const plans = plansData?.data ?? []
  const meters = metersData?.data ?? []
  const rules = rulesData?.data ?? []
  const loading = plansLoading || metersLoading || rulesLoading
  const error = plansError || metersError || rulesError

  const showAdd =
    (tab === 'plans' && plans.length > 0) ||
    (tab === 'meters' && meters.length > 0) ||
    (tab === 'rules' && rules.length > 0)

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Pricing</h1>
          <p className="text-sm text-muted-foreground mt-1">Configure plans, meters, and rating rules</p>
        </div>
        {showAdd && (
          <Button size="sm" onClick={() => setCreateFor(tab)}>
            <Plus size={16} className="mr-2" />
            {tab === 'plans' ? 'Add Plan' : tab === 'meters' ? 'Add Meter' : 'Add Rule'}
          </Button>
        )}
      </div>

      <Tabs value={tab} onValueChange={setTab} className="mt-6">
        <TabsList>
          <TabsTrigger value="plans">Plans ({plans.length})</TabsTrigger>
          <TabsTrigger value="meters">Meters ({meters.length})</TabsTrigger>
          <TabsTrigger value="rules">Rules ({rules.length})</TabsTrigger>
        </TabsList>

        <Card className="mt-4">
          <CardContent className="p-0">
            {error ? (
              <div className="p-8 text-center">
                <p className="text-sm text-destructive mb-3">
                  {error instanceof Error ? error.message : 'Failed to load pricing data'}
                </p>
                <Button variant="outline" size="sm" onClick={() => queryClient.invalidateQueries({ queryKey: ['plans'] })}>
                  Retry
                </Button>
              </div>
            ) : loading ? (
              <div className="p-8 flex justify-center">
                <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
              </div>
            ) : (
              <>
                <TabsContent value="plans">
                  {plans.length === 0 ? (
                    <Empty label="plans" onAdd={() => setCreateFor('plans')} />
                  ) : (
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead>Name</TableHead>
                          <TableHead>Code</TableHead>
                          <TableHead>Interval</TableHead>
                          <TableHead>Status</TableHead>
                          <TableHead className="text-right">Base Price</TableHead>
                          <TableHead className="text-right">Meters</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {plans.map(p => (
                          <TableRow
                            key={p.id}
                            className="cursor-pointer hover:bg-muted/50 transition-colors"
                            onClick={(e) => {
                              const target = e.target as HTMLElement
                              if (target.closest('button, a, input, select')) return
                              navigate(`/plans/${p.id}`)
                            }}
                          >
                            <TableCell>
                              <Link to={`/plans/${p.id}`} className="font-medium text-foreground hover:text-primary transition-colors">
                                {p.name}
                              </Link>
                            </TableCell>
                            <TableCell className="font-mono text-muted-foreground">{p.code}</TableCell>
                            <TableCell><Badge variant={badgeVariant(p.billing_interval)}>{p.billing_interval}</Badge></TableCell>
                            <TableCell><Badge variant={statusBadgeVariant(p.status)}>{p.status}</Badge></TableCell>
                            <TableCell className="text-right font-medium">{formatCents(p.base_amount_cents)}</TableCell>
                            <TableCell className="text-right text-muted-foreground">{p.meter_ids?.length || 0}</TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  )}
                </TabsContent>

                <TabsContent value="meters">
                  {meters.length === 0 ? (
                    <Empty label="meters" onAdd={() => setCreateFor('meters')} />
                  ) : (
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead>Name</TableHead>
                          <TableHead>Key</TableHead>
                          <TableHead>Unit</TableHead>
                          <TableHead>Aggregation</TableHead>
                          <TableHead>Created</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {meters.map(m => (
                          <TableRow
                            key={m.id}
                            className="cursor-pointer hover:bg-muted/50 transition-colors"
                            onClick={(e) => {
                              const target = e.target as HTMLElement
                              if (target.closest('button, a, input, select')) return
                              navigate(`/meters/${m.id}`)
                            }}
                          >
                            <TableCell>
                              <Link to={`/meters/${m.id}`} className="font-medium text-foreground hover:text-primary transition-colors">
                                {m.name}
                              </Link>
                            </TableCell>
                            <TableCell className="font-mono text-muted-foreground">{m.key}</TableCell>
                            <TableCell className="text-muted-foreground">{m.unit}</TableCell>
                            <TableCell><Badge variant={badgeVariant(m.aggregation)}>{m.aggregation}</Badge></TableCell>
                            <TableCell className="text-muted-foreground">{formatDate(m.created_at)}</TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  )}
                </TabsContent>

                <TabsContent value="rules">
                  {rules.length === 0 ? (
                    <Empty label="rating rules" onAdd={() => setCreateFor('rules')} />
                  ) : (
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead>Name</TableHead>
                          <TableHead>Rule Key</TableHead>
                          <TableHead>Mode</TableHead>
                          <TableHead>Version</TableHead>
                          <TableHead className="text-right">Price</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {rules.map(r => (
                          <TableRow key={r.id}>
                            <TableCell className="font-medium text-foreground">{r.name}</TableCell>
                            <TableCell className="font-mono text-muted-foreground">{r.rule_key}</TableCell>
                            <TableCell><Badge variant={badgeVariant(r.mode)}>{r.mode}</Badge></TableCell>
                            <TableCell className="text-muted-foreground">v{r.version}</TableCell>
                            <TableCell className="text-right font-medium">
                              {r.mode === 'flat'
                                ? formatCents(r.flat_amount_cents)
                                : r.mode === 'graduated'
                                  ? `${r.graduated_tiers?.length || 0} tiers`
                                  : `${r.package_size}/pkg`}
                            </TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  )}
                </TabsContent>
              </>
            )}
          </CardContent>
        </Card>
      </Tabs>

      {createFor === 'rules' && (
        <CreateRuleDialog
          onClose={() => setCreateFor(null)}
          onCreated={() => {
            setCreateFor(null)
            queryClient.invalidateQueries({ queryKey: ['rating-rules'] })
            toast.success('Rating rule created')
          }}
        />
      )}
      {createFor === 'meters' && (
        <CreateMeterDialog
          rules={rules}
          onClose={() => setCreateFor(null)}
          onCreated={() => {
            setCreateFor(null)
            queryClient.invalidateQueries({ queryKey: ['meters'] })
            toast.success('Meter created')
          }}
        />
      )}
      {createFor === 'plans' && (
        <CreatePlanDialog
          meters={meters}
          onClose={() => setCreateFor(null)}
          onCreated={() => {
            setCreateFor(null)
            queryClient.invalidateQueries({ queryKey: ['plans'] })
            toast.success('Plan created')
          }}
        />
      )}
    </Layout>
  )
}

function Empty({ label, onAdd }: { label: string; onAdd?: () => void }) {
  return (
    <div className="p-12 text-center">
      <p className="text-sm font-medium text-foreground">No {label} yet</p>
      <p className="text-sm text-muted-foreground mt-1">
        Get started by creating your first {label.replace(/s$/, '')}
      </p>
      {onAdd && (
        <Button size="sm" className="mt-4" onClick={onAdd}>
          <Plus size={16} className="mr-2" />
          Add {label.replace(/s$/, '').replace(/^./, c => c.toUpperCase())}
        </Button>
      )}
    </div>
  )
}

/* ─── Create Rule Dialog ─── */

function CreateRuleDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [mode, setMode] = useState('flat')
  const [flatAmount, setFlatAmount] = useState('')
  const [tiers, setTiers] = useState([
    { upTo: '100', price: '0.10' },
    { upTo: '', price: '0.05' },
  ])
  const [packageSize, setPackageSize] = useState('100')
  const [packageAmount, setPackageAmount] = useState('10.00')
  const [name, setName] = useState('')
  const [ruleKey, setRuleKey] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const modeDescriptions: Record<string, string> = {
    flat: 'Fixed price per unit',
    graduated: 'Price decreases as usage increases (tiered)',
    package: 'Charge per block of units',
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError('')
    try {
      const payload: Record<string, unknown> = { rule_key: ruleKey, name, mode, currency: getActiveCurrency() }
      if (mode === 'flat') {
        payload.flat_amount_cents = Math.round(parseFloat(flatAmount || '0') * 100)
      }
      if (mode === 'graduated') {
        for (let i = 0; i < tiers.length - 1; i++) {
          const current = parseInt(tiers[i].upTo) || 0
          const next = tiers[i + 1].upTo === '' ? Infinity : (parseInt(tiers[i + 1].upTo) || 0)
          if (current <= 0) { setError(`Tier ${i + 1}: "Up to" must be greater than 0`); setSaving(false); return }
          if (next !== Infinity && next <= current) { setError(`Tier ${i + 2}: "Up to" must be greater than ${current}`); setSaving(false); return }
        }
        payload.graduated_tiers = tiers.map(t => ({
          up_to: t.upTo === '' ? 0 : parseInt(t.upTo) || 0,
          unit_amount_cents: Math.round(parseFloat(t.price || '0') * 100),
        }))
      }
      if (mode === 'package') {
        payload.package_size = parseInt(packageSize) || 1
        payload.package_amount_cents = Math.round(parseFloat(packageAmount || '0') * 100)
      }
      await api.createRatingRule(payload as Parameters<typeof api.createRatingRule>[0])
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create rating rule')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Create Rating Rule</DialogTitle>
          <DialogDescription>Define how usage is priced for a specific event type</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} noValidate className="space-y-4">
          <div>
            <Label>Name</Label>
            <Input value={name} onChange={e => setName(e.target.value)} placeholder="API Call Pricing" maxLength={255} className="mt-1" />
          </div>
          <div>
            <Label>Rule Key</Label>
            <Input value={ruleKey} onChange={e => setRuleKey(e.target.value)} placeholder="api_calls" maxLength={100} className="mt-1 font-mono" />
            <p className="text-xs text-muted-foreground mt-1">Matches against event names for usage metering</p>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Pricing Model</Label>
              <Select value={mode} onValueChange={setMode}>
                <SelectTrigger className="w-full mt-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="flat">Flat Rate</SelectItem>
                  <SelectItem value="graduated">Graduated Tiers</SelectItem>
                  <SelectItem value="package">Package</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <p className="text-xs text-muted-foreground -mt-2">{modeDescriptions[mode]}</p>

          {mode === 'flat' && (
            <div>
              <Label>Price per Unit ({getCurrencySymbol()})</Label>
              <Input type="number" step="0.01" min={0} max={999999.99} value={flatAmount}
                onChange={e => setFlatAmount(e.target.value)} placeholder="0.01" className="mt-1" />
              <p className="text-xs text-muted-foreground mt-1">e.g. {getCurrencySymbol()}0.01 per API call</p>
            </div>
          )}

          {mode === 'graduated' && (
            <div className="space-y-3">
              <Label>Pricing Tiers</Label>
              <div className="rounded-lg border border-border overflow-hidden">
                <div className="grid grid-cols-[1fr_1fr_36px] gap-0 px-3 py-2 border-b border-border bg-muted">
                  <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Up to (units)</span>
                  <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Price per unit ($)</span>
                  <span />
                </div>
                {tiers.map((tier, idx) => (
                  <div key={idx} className="grid grid-cols-[1fr_1fr_36px] gap-0 px-3 py-2 border-b border-border last:border-b-0 items-center">
                    <div className="pr-2">
                      <Input type="number" min={0} value={tier.upTo}
                        onChange={e => setTiers(t => t.map((r, i) => i === idx ? { ...r, upTo: e.target.value } : r))}
                        placeholder={idx === tiers.length - 1 ? 'Unlimited' : '1000'} />
                    </div>
                    <div className="pr-2">
                      <Input type="number" step="0.01" min={0} value={tier.price}
                        onChange={e => setTiers(t => t.map((r, i) => i === idx ? { ...r, price: e.target.value } : r))}
                        placeholder="0.01" />
                    </div>
                    <div className="flex justify-center">
                      {tiers.length > 1 && (
                        <button type="button" onClick={() => setTiers(t => t.filter((_, i) => i !== idx))}
                          className="text-muted-foreground hover:text-destructive transition-colors">
                          <Trash2 size={14} />
                        </button>
                      )}
                    </div>
                  </div>
                ))}
              </div>
              <button type="button" onClick={() => setTiers(t => [...t, { upTo: '', price: '' }])}
                className="text-sm font-medium text-primary hover:text-primary/80 transition-colors">
                + Add tier
              </button>
              <p className="text-xs text-muted-foreground">Leave "Up to" empty on the last tier for unlimited. Tiers are evaluated in order.</p>
            </div>
          )}

          {mode === 'package' && (
            <div className="grid grid-cols-2 gap-3">
              <div>
                <Label>Units per Package</Label>
                <Input type="number" min={1} value={packageSize} onChange={e => setPackageSize(e.target.value)}
                  placeholder="100" className="mt-1" />
                <p className="text-xs text-muted-foreground mt-1">e.g. 100 API calls per package</p>
              </div>
              <div>
                <Label>Price per Package ({getCurrencySymbol()})</Label>
                <Input type="number" step="0.01" min={0} max={999999.99} value={packageAmount}
                  onChange={e => setPackageAmount(e.target.value)} placeholder="10.00" className="mt-1" />
                <p className="text-xs text-muted-foreground mt-1">e.g. {getCurrencySymbol()}10.00 per 100 calls</p>
              </div>
            </div>
          )}

          {error && (
            <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
              <p className="text-destructive text-sm">{error}</p>
            </div>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={saving}>
              {saving ? <><Loader2 size={14} className="animate-spin mr-2" /> Saving...</> : 'Create Rule'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/* ─── Create Meter Dialog ─── */

function CreateMeterDialog({ onClose, onCreated, rules }: { onClose: () => void; onCreated: () => void; rules: RatingRule[] }) {
  const [form, setForm] = useState({ key: '', name: '', unit: 'unit', aggregation: 'sum', rating_rule_version_id: '' })
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const aggregationDescriptions: Record<string, string> = {
    sum: 'Add up all values in the billing period',
    count: 'Count the number of events',
    max: 'Use the highest value seen',
    last: 'Use the most recent value',
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError('')
    try {
      await api.createMeter(form)
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create meter')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Create Meter</DialogTitle>
          <DialogDescription>Define a new usage meter for tracking events</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} noValidate className="space-y-4">
          <div>
            <Label>Name</Label>
            <Input value={form.name} onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
              placeholder="API Calls" maxLength={255} className="mt-1" />
          </div>
          <div>
            <Label>Key</Label>
            <Input value={form.key} onChange={e => setForm(f => ({ ...f, key: e.target.value }))}
              placeholder="api_calls" maxLength={100} className="mt-1 font-mono" />
            <p className="text-xs text-muted-foreground mt-1">
              Used to match incoming usage events (e.g. api_calls, messages_sent)
            </p>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Unit Label</Label>
              <Input value={form.unit} onChange={e => setForm(f => ({ ...f, unit: e.target.value }))}
                placeholder="unit" className="mt-1" />
              <p className="text-xs text-muted-foreground mt-1">e.g. call, request, GB</p>
            </div>
            <div>
              <Label>Aggregation</Label>
              <Select value={form.aggregation} onValueChange={v => setForm(f => ({ ...f, aggregation: v }))}>
                <SelectTrigger className="w-full mt-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="sum">Sum</SelectItem>
                  <SelectItem value="count">Count</SelectItem>
                  <SelectItem value="max">Maximum</SelectItem>
                  <SelectItem value="last">Latest Value</SelectItem>
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground mt-1">{aggregationDescriptions[form.aggregation]}</p>
            </div>
          </div>
          {rules.length > 0 && (
            <div>
              <Label>Rating Rule</Label>
              <Select value={form.rating_rule_version_id} onValueChange={v => setForm(f => ({ ...f, rating_rule_version_id: v }))}>
                <SelectTrigger className="w-full mt-1">
                  <SelectValue placeholder="None (assign later)" />
                </SelectTrigger>
                <SelectContent>
                  {rules.map(r => (
                    <SelectItem key={r.id} value={r.id} label={`${r.name} (${r.mode})`}>{r.name} ({r.mode})</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          {error && (
            <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
              <p className="text-destructive text-sm">{error}</p>
            </div>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={saving}>
              {saving ? <><Loader2 size={14} className="animate-spin mr-2" /> Saving...</> : 'Create Meter'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/* ─── Create Plan Dialog ─── */

function CreatePlanDialog({ onClose, onCreated, meters }: { onClose: () => void; onCreated: () => void; meters: Meter[] }) {
  const [name, setName] = useState('')
  const [code, setCode] = useState('')
  const [interval, setInterval] = useState('monthly')
  const [basePrice, setBasePrice] = useState('')
  const [meterIds, setMeterIds] = useState<string[]>([])
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError('')
    try {
      await api.createPlan({
        name,
        code,
        currency: getActiveCurrency(),
        billing_interval: interval,
        base_amount_cents: Math.round(parseFloat(basePrice || '0') * 100),
        meter_ids: meterIds,
      })
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create plan')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Create Plan</DialogTitle>
          <DialogDescription>Define a new billing plan with pricing and meters</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} noValidate className="space-y-4">
          <div>
            <Label>Plan Name</Label>
            <Input value={name} onChange={e => setName(e.target.value)}
              placeholder="Pro Plan" maxLength={255} className="mt-1" />
          </div>
          <div>
            <Label>Code</Label>
            <Input value={code} onChange={e => setCode(e.target.value)}
              placeholder="pro" maxLength={100} className="mt-1 font-mono" />
            <p className="text-xs text-muted-foreground mt-1">Unique identifier used in API calls</p>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Billing Interval</Label>
              <Select value={interval} onValueChange={setInterval}>
                <SelectTrigger className="w-full mt-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="monthly">Monthly</SelectItem>
                  <SelectItem value="yearly">Yearly</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div>
            <Label>Base Price ({getCurrencySymbol()})</Label>
            <Input type="number" step="0.01" min={0} max={999999.99} value={basePrice}
              onChange={e => setBasePrice(e.target.value)} placeholder="49.00" className="mt-1" />
            <p className="text-xs text-muted-foreground mt-1">Fixed {interval} charge before usage fees</p>
          </div>

          {meters.length > 0 && (
            <div className="border-t border-border pt-4">
              <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Usage Meters</p>
              <div className="space-y-0 rounded-lg border border-border divide-y divide-border overflow-hidden">
                {meters.map(m => (
                  <label key={m.id} className="flex items-center gap-3 px-3 py-2.5 text-sm cursor-pointer hover:bg-accent transition-colors">
                    <Checkbox
                      checked={meterIds.includes(m.id)}
                      onCheckedChange={(checked) =>
                        setMeterIds(checked ? [...meterIds, m.id] : meterIds.filter(id => id !== m.id))
                      }
                    />
                    <div className="flex-1 min-w-0">
                      <span className="text-foreground">{m.name}</span>
                      <span className="text-muted-foreground font-mono text-xs ml-2">{m.key}</span>
                    </div>
                    <span className="text-xs text-muted-foreground">{m.aggregation} &middot; {m.unit}</span>
                  </label>
                ))}
              </div>
            </div>
          )}

          {error && (
            <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
              <p className="text-destructive text-sm">{error}</p>
            </div>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={saving}>
              {saving ? <><Loader2 size={14} className="animate-spin mr-2" /> Saving...</> : 'Create Plan'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
