import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import {
  api,
  formatCents,
  formatDateTime,
  type BillingAlert,
  type BillingAlertStatus,
  type Customer,
  type Meter,
  type CreateBillingAlertRequest,
} from '@/lib/api'
import { applyApiError, showApiError } from '@/lib/formErrors'
import { Layout } from '@/components/Layout'
import { CustomerCombobox } from '@/components/CustomerCombobox'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import {
  Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage,
} from '@/components/ui/form'
import { TableSkeleton } from '@/components/ui/TableSkeleton'

import { Loader2, Plus, BellRing, Archive } from 'lucide-react'
import { EmptyState } from '@/components/EmptyState'

const statusOptions: BillingAlertStatus[] = ['active', 'triggered', 'triggered_for_period', 'archived']

const createAlertSchema = z.object({
  title: z.string().min(1, 'Title is required'),
  customerId: z.string().min(1, 'Customer is required'),
  meterId: z.string(),
  thresholdKind: z.enum(['amount_gte', 'usage_gte']),
  amountStr: z.string(),
  usageStr: z.string(),
  recurrence: z.enum(['one_time', 'per_period']),
}).superRefine((d, ctx) => {
  if (d.thresholdKind === 'amount_gte') {
    const v = d.amountStr.trim()
    if (v === '' || !/^\d+(\.\d{1,2})?$/.test(v) || Number(v) <= 0) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['amountStr'], message: 'Must be a positive number' })
    }
  } else {
    const v = d.usageStr.trim()
    if (v === '' || !/^\d+(\.\d+)?$/.test(v) || Number(v) <= 0) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['usageStr'], message: 'Must be a positive number' })
    }
  }
})

type CreateAlertData = z.infer<typeof createAlertSchema>

function statusVariant(s: BillingAlertStatus): 'success' | 'secondary' | 'danger' | 'warning' {
  switch (s) {
    case 'active': return 'success'
    case 'triggered': return 'danger'
    case 'triggered_for_period': return 'warning'
    case 'archived': return 'secondary'
  }
}

function trimDecimal(s: string): string {
  if (!s.includes('.')) return s
  return s.replace(/0+$/, '').replace(/\.$/, '')
}

function customerLabel(c: Customer | undefined, fallback: string): string {
  if (!c) return fallback
  return c.display_name || c.email || c.external_id || c.id
}

export default function BillingAlertsPage() {
  const [statusFilter, setStatusFilter] = useState<BillingAlertStatus | 'all'>('all')
  const [showCreate, setShowCreate] = useState(false)
  const [archiveTarget, setArchiveTarget] = useState<BillingAlert | null>(null)
  const queryClient = useQueryClient()

  const { data: alertsData, isLoading: loading, error: loadError } = useQuery({
    queryKey: ['billing-alerts', statusFilter],
    queryFn: () => api.listBillingAlerts(
      statusFilter === 'all' ? { limit: 100 } : { status: statusFilter, limit: 100 }
    ),
  })

  const { data: customers } = useQuery({
    queryKey: ['customers'],
    queryFn: () => api.listCustomers().then(r => r.data),
    staleTime: 30_000,
  })

  const { data: meters } = useQuery({
    queryKey: ['meters'],
    queryFn: () => api.listMeters().then(r => r.data),
    staleTime: 30_000,
  })

  const customerById = (id: string) => customers?.find(c => c.id === id)
  const meterById = (id: string) => meters?.find(m => m.id === id)

  const archiveMutation = useMutation({
    mutationFn: (id: string) => api.archiveBillingAlert(id),
    onSuccess: () => {
      toast.success('Alert archived')
      setArchiveTarget(null)
      queryClient.invalidateQueries({ queryKey: ['billing-alerts'] })
    },
    onError: (err) => {
      showApiError(err, 'Failed to archive alert')
    },
  })

  const alerts = alertsData?.data ?? []
  const errorMsg = loadError instanceof Error ? loadError.message : null

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Billing alerts</h1>
          <p className="text-sm text-muted-foreground mt-1 max-w-3xl">
            Operator-defined customer-scoped spend or usage thresholds. The evaluator runs in the background and fires <code className="px-1 bg-muted rounded text-xs">billing.alert.triggered</code> via webhook + dashboard notification when a customer's running cycle total crosses the configured threshold.
          </p>
        </div>
        <Button onClick={() => setShowCreate(true)}>
          <Plus className="size-4 mr-2" />
          New alert
        </Button>
      </div>

      <Card className="mt-6">
        <CardContent className="p-0">
          <div className="flex items-center gap-2 p-4 border-b border-border">
            <span className="text-sm text-muted-foreground">Status:</span>
            <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v as BillingAlertStatus | 'all')}>
              <SelectTrigger className="w-[220px] h-8">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All</SelectItem>
                {statusOptions.map(s => (
                  <SelectItem key={s} value={s}>{s.replaceAll('_', ' ')}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {loading ? (
            <TableSkeleton columns={7} />
          ) : errorMsg ? (
            <div className="p-8 text-center">
              <p className="text-sm text-destructive">Failed to load alerts: {errorMsg}</p>
            </div>
          ) : alerts.length === 0 ? (
            <EmptyState
              icon={BellRing}
              title="No billing alerts"
              description="Configure customer-scoped spend or usage thresholds. When a customer's running cycle total crosses the configured threshold, the evaluator fires billing.alert.triggered via webhook + dashboard notification."
              action={{ label: 'New alert', icon: Plus, onClick: () => setShowCreate(true) }}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Title</TableHead>
                  <TableHead>Customer</TableHead>
                  <TableHead>Meter</TableHead>
                  <TableHead>Threshold</TableHead>
                  <TableHead>Recurrence</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Last triggered</TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {alerts.map(a => {
                  const c = customerById(a.customer_id)
                  const m = a.filter.meter_id ? meterById(a.filter.meter_id) : undefined
                  const isAmount = a.threshold.amount_gte != null
                  return (
                    <TableRow key={a.id}>
                      <TableCell className="font-medium">{a.title}</TableCell>
                      <TableCell>
                        {c ? (
                          <Link to={`/customers/${c.id}`} className="text-primary hover:underline">
                            {customerLabel(c, a.customer_id)}
                          </Link>
                        ) : (
                          <span className="font-mono text-xs">{a.customer_id}</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {m ? (
                          <Link to={`/meters/${m.id}`} className="text-primary hover:underline">{m.name}</Link>
                        ) : a.filter.meter_id ? (
                          <span className="font-mono text-xs">{a.filter.meter_id}</span>
                        ) : (
                          <span className="text-xs text-muted-foreground">all meters</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {isAmount ? (
                          <span>&ge; {formatCents(a.threshold.amount_gte!)}</span>
                        ) : (
                          <span>&ge; {trimDecimal(a.threshold.usage_gte!)} units</span>
                        )}
                      </TableCell>
                      <TableCell className="text-sm">
                        {a.recurrence === 'one_time' ? 'one-time' : 'per period'}
                      </TableCell>
                      <TableCell>
                        <Badge variant={statusVariant(a.status)}>{a.status.replaceAll('_', ' ')}</Badge>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {a.last_triggered_at ? formatDateTime(a.last_triggered_at) : '\u2014'}
                      </TableCell>
                      <TableCell className="text-right">
                        {a.status !== 'archived' && (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 text-xs"
                            onClick={() => setArchiveTarget(a)}
                          >
                            <Archive className="size-3 mr-1" />
                            Archive
                          </Button>
                        )}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <CreateAlertDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        meters={meters ?? []}
        onCreated={() => {
          setShowCreate(false)
          queryClient.invalidateQueries({ queryKey: ['billing-alerts'] })
        }}
      />

      <AlertDialog open={!!archiveTarget} onOpenChange={(open) => { if (!open) setArchiveTarget(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Archive billing alert?</AlertDialogTitle>
            <AlertDialogDescription>
              Archive <strong>{archiveTarget?.title}</strong>. The evaluator will stop firing this alert. Archive is final &mdash; recreate the alert if you need to track this threshold again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={archiveMutation.isPending}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => archiveTarget && archiveMutation.mutate(archiveTarget.id)}
              disabled={archiveMutation.isPending}
            >
              {archiveMutation.isPending ? 'Archiving...' : 'Archive'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Layout>
  )
}

function CreateAlertDialog({ open, onOpenChange, meters, onCreated }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  meters: Meter[]
  onCreated: () => void
}) {
  const form = useForm<CreateAlertData>({
    resolver: zodResolver(createAlertSchema),
    defaultValues: {
      title: '',
      customerId: '',
      meterId: '',
      thresholdKind: 'amount_gte',
      amountStr: '',
      usageStr: '',
      recurrence: 'one_time',
    },
  })

  const createMutation = useMutation({
    mutationFn: (body: CreateBillingAlertRequest) => api.createBillingAlert(body),
    onSuccess: () => {
      toast.success('Alert created')
      form.reset()
      onCreated()
    },
    onError: (err) => {
      applyApiError(form, err)
    },
  })

  const kind = form.watch('thresholdKind')

  const onSubmit = (data: CreateAlertData) => {
    const body: CreateBillingAlertRequest = {
      title: data.title.trim(),
      customer_id: data.customerId,
      threshold: data.thresholdKind === 'amount_gte'
        ? { amount_gte: Math.round(Number(data.amountStr) * 100) }
        : { usage_gte: data.usageStr.trim() },
      recurrence: data.recurrence,
    }
    if (data.meterId) {
      body.filter = { meter_id: data.meterId }
    }
    createMutation.mutate(body)
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!createMutation.isPending) onOpenChange(v) }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>New billing alert</DialogTitle>
          <DialogDescription>
            Customer-scoped threshold. Fires <code className="px-1 bg-muted rounded text-xs">billing.alert.triggered</code> via webhook + dashboard notification when the running cycle total crosses the configured value.
          </DialogDescription>
        </DialogHeader>
        <Form {...form}>
          <form onSubmit={form.handleSubmit(onSubmit)} noValidate className="space-y-4">
            <FormField
              control={form.control}
              name="title"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Title</FormLabel>
                  <FormControl>
                    <Input placeholder="e.g. Spend over $1k" {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name="customerId"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Customer</FormLabel>
                  <FormControl>
                    <CustomerCombobox value={field.value} onChange={field.onChange} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name="meterId"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Meter (optional)</FormLabel>
                  <Select
                    value={field.value || '__all__'}
                    onValueChange={(v) => field.onChange(v === '__all__' ? '' : v)}
                  >
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue placeholder="All meters" />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      <SelectItem value="__all__">All meters (subtotal across all usage)</SelectItem>
                      {meters.map(m => (
                        <SelectItem key={m.id} value={m.id}>{m.name}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    Leave blank to alert on the customer's full cycle subtotal across all meters.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name="thresholdKind"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Threshold type</FormLabel>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      <SelectItem value="amount_gte">Amount (running cycle subtotal)</SelectItem>
                      <SelectItem value="usage_gte">Usage (running cycle quantity)</SelectItem>
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )}
            />

            {kind === 'amount_gte' ? (
              <FormField
                control={form.control}
                name="amountStr"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Amount</FormLabel>
                    <FormControl>
                      <Input type="number" inputMode="decimal" step="0.01" min="0" placeholder="1000.00" {...field} />
                    </FormControl>
                    <FormDescription>
                      Major units. Multiplied by 100 to integer cents on submit.
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            ) : (
              <FormField
                control={form.control}
                name="usageStr"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Quantity</FormLabel>
                    <FormControl>
                      <Input type="text" inputMode="decimal" placeholder="1000000" {...field} />
                    </FormControl>
                    <FormDescription>
                      Decimal allowed (cached-token ratios, GPU-hours).
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            <FormField
              control={form.control}
              name="recurrence"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Recurrence</FormLabel>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <FormControl>
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      <SelectItem value="one_time">One-time (fires once, then status flips to triggered)</SelectItem>
                      <SelectItem value="per_period">Per period (re-arms each billing cycle)</SelectItem>
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )}
            />

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={createMutation.isPending}>
                Cancel
              </Button>
              <Button type="submit" disabled={createMutation.isPending}>
                {createMutation.isPending ? (
                  <><Loader2 size={14} className="animate-spin mr-2" />Creating...</>
                ) : 'Create alert'}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  )
}
