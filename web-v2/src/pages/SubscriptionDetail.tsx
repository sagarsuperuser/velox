import { useState } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm, Controller } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatCents, formatDate, formatDateTime, type Subscription, type Customer, type Plan, type Invoice, type InvoicePreview } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { cn } from '@/lib/utils'
import { statusBadgeVariant } from '@/lib/status'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'

import { ArrowLeft, Copy, Check, Loader2 } from 'lucide-react'

function CopyId({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => { navigator.clipboard.writeText(text); setCopied(true); setTimeout(() => setCopied(false), 1500) }}
      className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground transition-colors"
    >
      {copied ? <Check size={12} /> : <Copy size={12} />}
    </button>
  )
}

const statusVariant = statusBadgeVariant

export default function SubscriptionDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const [showCancelConfirm, setShowCancelConfirm] = useState(false)
  const [showPauseConfirm, setShowPauseConfirm] = useState(false)
  const [showChangePlan, setShowChangePlan] = useState(false)

  const { data: sub, isLoading, error: loadError, refetch } = useQuery({
    queryKey: ['subscription', id],
    queryFn: () => api.getSubscription(id!),
    enabled: !!id,
  })

  const { data: customer } = useQuery({
    queryKey: ['customer', sub?.customer_id],
    queryFn: () => api.getCustomer(sub!.customer_id),
    enabled: !!sub?.customer_id,
  })

  const { data: plansData } = useQuery({
    queryKey: ['plans'],
    queryFn: () => api.listPlans().then(r => r.data),
  })

  const plan = plansData?.find(p => p.id === sub?.plan_id)

  const { data: invoices } = useQuery({
    queryKey: ['subscription-invoices', id],
    queryFn: () => api.listInvoices('subscription_id=' + id).then(r => r.data),
    enabled: !!id,
  })

  const { data: preview, error: previewError } = useQuery({
    queryKey: ['subscription-preview', id],
    queryFn: () => api.invoicePreview(id!),
    enabled: !!id,
    retry: false,
  })

  const invalidateAll = () => {
    queryClient.invalidateQueries({ queryKey: ['subscription', id] })
    queryClient.invalidateQueries({ queryKey: ['subscription-invoices', id] })
    queryClient.invalidateQueries({ queryKey: ['subscription-preview', id] })
    queryClient.invalidateQueries({ queryKey: ['subscriptions'] })
  }

  const activateMutation = useMutation({
    mutationFn: () => api.activateSubscription(id!),
    onSuccess: () => { invalidateAll(); toast.success('Subscription activated') },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to activate'),
  })

  const pauseMutation = useMutation({
    mutationFn: () => api.pauseSubscription(id!),
    onSuccess: () => { invalidateAll(); toast.success('Subscription paused'); setShowPauseConfirm(false) },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to pause'),
  })

  const resumeMutation = useMutation({
    mutationFn: () => api.resumeSubscription(id!),
    onSuccess: () => { invalidateAll(); toast.success('Subscription resumed') },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to resume'),
  })

  const cancelMutation = useMutation({
    mutationFn: () => api.cancelSubscription(id!),
    onSuccess: () => { invalidateAll(); toast.success('Subscription canceled'); setShowCancelConfirm(false) },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to cancel'),
  })

  const acting = activateMutation.isPending || pauseMutation.isPending || resumeMutation.isPending || cancelMutation.isPending

  const loading = isLoading
  const error = loadError instanceof Error ? loadError.message : loadError ? String(loadError) : null

  if (loading) {
    return (
      <Layout>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Link to="/subscriptions" className="hover:text-foreground transition-colors">Subscriptions</Link>
          <span>/</span>
          <span>Loading...</span>
        </div>
        <div className="flex justify-center py-16">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      </Layout>
    )
  }

  if (error) {
    return (
      <Layout>
        <div className="py-16 text-center">
          <p className="text-sm text-destructive mb-3">{error}</p>
          <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
        </div>
      </Layout>
    )
  }

  if (!sub) {
    return (
      <Layout>
        <p className="text-sm text-muted-foreground py-16 text-center">Subscription not found</p>
      </Layout>
    )
  }

  return (
    <Layout>
      {/* Breadcrumb */}
      <div className="flex items-center gap-2 text-sm text-muted-foreground mb-4">
        <Link to="/subscriptions" className="hover:text-foreground transition-colors flex items-center gap-1">
          <ArrowLeft size={14} />
          Subscriptions
        </Link>
        <span>/</span>
        <span className="text-foreground">{sub.display_name}</span>
      </div>

      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">{sub.display_name}</h1>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-xs text-muted-foreground font-mono bg-muted px-2 py-0.5 rounded">{sub.id}</span>
            <CopyId text={sub.id} />
            <span className="text-xs text-muted-foreground font-mono bg-muted px-2 py-0.5 rounded">{sub.code}</span>
          </div>
        </div>
        <div className="flex items-center gap-2">
          {sub.status === 'draft' && (
            <Button onClick={() => activateMutation.mutate()} disabled={acting}>
              Activate
            </Button>
          )}
          {sub.status === 'active' && (
            <>
              <Button variant="outline" onClick={() => setShowChangePlan(true)} disabled={acting}>
                Change Plan
              </Button>
              <Button variant="outline" className="border-amber-300 text-amber-600 hover:bg-amber-50" onClick={() => setShowPauseConfirm(true)} disabled={acting}>
                Pause
              </Button>
              <Button variant="outline" className="border-destructive text-destructive hover:bg-destructive/10" onClick={() => setShowCancelConfirm(true)} disabled={acting}>
                Cancel
              </Button>
            </>
          )}
          {sub.status === 'paused' && (
            <>
              <Button variant="outline" className="border-emerald-300 text-emerald-600 hover:bg-emerald-50" onClick={() => resumeMutation.mutate()} disabled={acting}>
                Resume
              </Button>
              <Button variant="outline" className="border-destructive text-destructive hover:bg-destructive/10" onClick={() => setShowCancelConfirm(true)} disabled={acting}>
                Cancel
              </Button>
            </>
          )}
          <Badge variant={statusVariant(sub.status)}>{sub.status}</Badge>
        </div>
      </div>

      {/* Subscription Timeline */}
      {(() => {
        const timelinePoints: { label: string; date: string; isPast: boolean }[] = []
        const now = new Date()

        timelinePoints.push({
          label: 'Created',
          date: formatDate(sub.created_at),
          isPast: true,
        })

        if (sub.current_billing_period_start) {
          const periodStart = new Date(sub.current_billing_period_start)
          timelinePoints.push({
            label: sub.status === 'active' ? 'Period Start' : 'Last Period',
            date: formatDate(sub.current_billing_period_start),
            isPast: periodStart <= now,
          })
        }

        if (sub.current_billing_period_end) {
          const periodEnd = new Date(sub.current_billing_period_end)
          timelinePoints.push({
            label: 'Period End',
            date: formatDate(sub.current_billing_period_end),
            isPast: periodEnd <= now,
          })
        }

        if (sub.next_billing_at) {
          const nextBilling = new Date(sub.next_billing_at)
          timelinePoints.push({
            label: 'Next Billing',
            date: formatDate(sub.next_billing_at),
            isPast: nextBilling <= now,
          })
        }

        if (timelinePoints.length < 2) return null

        return (
          <Card className="mt-6 mb-6">
            <CardContent className="py-6">
              <div className="flex items-center justify-between relative px-4">
                {/* Background line */}
                <div className="absolute left-4 right-4 top-1/2 h-0.5 bg-border -translate-y-1/2" />

                {/* Timeline points */}
                {timelinePoints.map((point, i) => (
                  <div key={i} className="relative flex flex-col items-center z-10">
                    <div className={cn(
                      'w-3 h-3 rounded-full border-2',
                      point.isPast
                        ? 'bg-primary border-primary'
                        : 'bg-background border-border'
                    )} />
                    <p className="text-xs font-medium text-foreground mt-2">{point.label}</p>
                    <p className="text-[10px] text-muted-foreground mt-0.5">{point.date}</p>
                  </div>
                ))}
              </div>
            </CardContent>
          </Card>
        )
      })()}

      {/* Key metrics */}
      <Card>
        <CardContent className="p-0">
          <div className="flex divide-x divide-border">
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Customer</p>
              {customer ? (
                <Link to={`/customers/${customer.id}`} className="text-lg font-semibold text-primary hover:underline mt-1 block">
                  {customer.display_name}
                </Link>
              ) : (
                <p className="text-lg font-semibold text-foreground mt-1">{sub.customer_id.slice(0, 8)}...</p>
              )}
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Plan</p>
              {plan ? (
                <Link to={`/plans/${plan.id}`} className="text-lg font-semibold text-primary hover:underline mt-1 block">
                  {plan.name}
                </Link>
              ) : (
                <p className="text-lg font-semibold text-foreground mt-1">{sub.plan_id.slice(0, 8)}...</p>
              )}
              {plan && (
                <p className="text-xs text-muted-foreground mt-0.5">
                  {formatCents(plan.base_amount_cents)}/{plan.billing_interval === 'yearly' ? 'yr' : 'mo'}
                </p>
              )}
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Billing Period</p>
              <p className="text-lg font-semibold text-foreground mt-1">
                {sub.current_billing_period_start && sub.current_billing_period_end
                  ? `${formatDate(sub.current_billing_period_start)} \u2014 ${formatDate(sub.current_billing_period_end)}`
                  : '\u2014'}
              </p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Status</p>
              <div className="mt-1.5">
                <Badge variant={statusVariant(sub.status)}>{sub.status}</Badge>
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Properties */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">Properties</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground w-40 shrink-0">Code</span>
              <span className="text-sm text-foreground font-mono">{sub.code}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground w-40 shrink-0">Customer</span>
              {customer ? (
                <Link to={`/customers/${customer.id}`} className="text-sm font-medium text-primary hover:underline">
                  {customer.display_name}
                </Link>
              ) : (
                <span className="text-sm text-foreground font-mono">{sub.customer_id}</span>
              )}
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground w-40 shrink-0">Plan</span>
              {plan ? (
                <span className="text-sm">
                  <Link to={`/plans/${plan.id}`} className="font-medium text-primary hover:underline">
                    {plan.name}
                  </Link>
                  <span className="text-muted-foreground ml-1.5">
                    {formatCents(plan.base_amount_cents)}/{plan.billing_interval === 'yearly' ? 'yr' : 'mo'}
                  </span>
                </span>
              ) : (
                <span className="text-sm text-foreground font-mono">{sub.plan_id}</span>
              )}
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground w-40 shrink-0">Status</span>
              <Badge variant={statusVariant(sub.status)}>{sub.status}</Badge>
            </div>
            {sub.billing_time && (
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-muted-foreground w-40 shrink-0">Billing Time</span>
                <span className="text-sm text-foreground">{sub.billing_time === 'anniversary' ? 'Anniversary' : 'Calendar'}</span>
              </div>
            )}
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground w-40 shrink-0">Billing Period</span>
              <span className="text-sm text-foreground">
                {sub.current_billing_period_start && sub.current_billing_period_end
                  ? `${formatDate(sub.current_billing_period_start)} \u2014 ${formatDate(sub.current_billing_period_end)}`
                  : '\u2014'}
              </span>
            </div>
            {sub.usage_cap_units != null && (
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-muted-foreground w-40 shrink-0">Usage Cap</span>
                <span className="text-sm text-foreground">
                  {sub.usage_cap_units.toLocaleString()} units / period
                  <span className="text-xs text-muted-foreground ml-2">({sub.overage_action === 'block' ? 'hard cap' : 'charge overage'})</span>
                </span>
              </div>
            )}
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground w-40 shrink-0">Created</span>
              <span className="text-sm text-foreground">{formatDateTime(sub.created_at)}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground w-40 shrink-0">ID</span>
              <div className="flex items-center gap-2">
                <span className="text-sm text-foreground font-mono">{sub.id}</span>
                <CopyId text={sub.id} />
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Invoice Preview */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">Next Invoice Preview</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {preview ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Description</TableHead>
                  <TableHead className="text-right">Quantity</TableHead>
                  <TableHead className="text-right">Amount</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {preview.lines.map((line, i) => (
                  <TableRow key={i}>
                    <TableCell>{line.description}</TableCell>
                    <TableCell className="text-right">{line.quantity}</TableCell>
                    <TableCell className="text-right font-medium">{formatCents(line.amount_cents)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
              <tfoot>
                <TableRow className="border-t-2">
                  <TableCell colSpan={2} className="text-right font-semibold">Subtotal</TableCell>
                  <TableCell className="text-right font-semibold">{formatCents(preview.subtotal_cents)}</TableCell>
                </TableRow>
              </tfoot>
            </Table>
          ) : previewError ? (
            <div className="px-6 py-8 text-center">
              <p className="text-sm text-muted-foreground">Preview not available</p>
              <p className="text-sm text-muted-foreground mt-1">Activate the subscription and set a billing period to see a preview</p>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground text-center py-8">Invoice preview will appear once a billing period is set</p>
          )}
        </CardContent>
      </Card>

      {/* Related Invoices */}
      <Card className="mt-6">
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle className="text-sm">Invoices ({invoices?.length ?? 0})</CardTitle>
            {(invoices?.length ?? 0) > 5 && (
              <Link to="/invoices" className="text-xs text-primary hover:underline">View all</Link>
            )}
          </div>
        </CardHeader>
        <CardContent className="p-0">
          {invoices && invoices.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Invoice</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Payment</TableHead>
                  <TableHead className="text-right">Amount</TableHead>
                  <TableHead>Date</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {invoices.slice(0, 5).map(inv => (
                  <TableRow
                    key={inv.id}
                    className="cursor-pointer hover:bg-muted/50 transition-colors"
                    onClick={(e) => {
                      const target = e.target as HTMLElement
                      if (target.closest('button, a, input, select')) return
                      navigate(`/invoices/${inv.id}`)
                    }}
                  >
                    <TableCell>
                      <Link to={`/invoices/${inv.id}`} className="text-sm font-medium text-foreground hover:text-primary transition-colors">
                        {inv.invoice_number}
                      </Link>
                    </TableCell>
                    <TableCell><Badge variant={statusVariant(inv.status)}>{inv.status}</Badge></TableCell>
                    <TableCell><Badge variant={statusVariant(inv.payment_status)}>{inv.payment_status}</Badge></TableCell>
                    <TableCell className="text-right font-medium">{formatCents(inv.total_amount_cents)}</TableCell>
                    <TableCell className="text-sm text-muted-foreground">{formatDate(inv.created_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : (
            <p className="text-sm text-muted-foreground text-center py-8">Invoices will appear here after billing runs</p>
          )}
        </CardContent>
      </Card>

      {/* Pause Confirm */}
      <AlertDialog open={showPauseConfirm} onOpenChange={setShowPauseConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Pause Subscription</AlertDialogTitle>
            <AlertDialogDescription>
              Pausing will stop billing for this subscription. Usage will not be metered while paused. You can resume at any time.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => pauseMutation.mutate()} disabled={pauseMutation.isPending}>
              Pause Subscription
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Cancel Confirm */}
      <AlertDialog open={showCancelConfirm} onOpenChange={setShowCancelConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Cancel Subscription</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to cancel this subscription? This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => cancelMutation.mutate()} disabled={cancelMutation.isPending}>
              Cancel Subscription
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Change Plan Dialog */}
      {showChangePlan && plansData && (
        <ChangePlanDialog
          subscriptionId={sub.id}
          currentPlanId={sub.plan_id}
          currentPlanName={plan?.name || 'Unknown'}
          plans={plansData}
          onClose={() => setShowChangePlan(false)}
          onChanged={(proration) => {
            setShowChangePlan(false)
            invalidateAll()
            if (proration) {
              if (proration.type === 'upgrade') {
                toast.success(`Proration invoice created for ${formatCents(proration.amount_cents)}`)
              } else if (proration.type === 'downgrade') {
                toast.success(`${formatCents(Math.abs(proration.amount_cents))} credited to customer balance`)
              } else {
                toast.success('Plan changed successfully')
              }
            } else {
              toast.success('Plan changed successfully')
            }
          }}
        />
      )}
    </Layout>
  )
}

const changePlanSchema = z.object({
  plan_id: z.string().min(1, 'Plan is required'),
  immediate: z.boolean(),
})
type ChangePlanData = z.infer<typeof changePlanSchema>

function ChangePlanDialog({ subscriptionId, currentPlanId, currentPlanName, plans, onClose, onChanged }: {
  subscriptionId: string
  currentPlanId: string
  currentPlanName: string
  plans: Plan[]
  onClose: () => void
  onChanged: (proration?: { type: string; amount_cents: number; invoice_id?: string }) => void
}) {
  const [error, setError] = useState('')
  const [selectedPlan, setSelectedPlan] = useState('')
  const [immediate, setImmediate] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  const availablePlans = plans.filter(p => p.id !== currentPlanId && p.status === 'active')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!selectedPlan) return
    setError('')
    setSubmitting(true)
    try {
      const res = await api.changePlan(subscriptionId, { new_plan_id: selectedPlan, immediate })
      onChanged(res.proration)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to change plan')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Change Plan</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} noValidate className="space-y-4">
          <div>
            <p className="text-sm text-muted-foreground">Current plan</p>
            <p className="text-sm font-semibold text-foreground mt-0.5">{currentPlanName}</p>
          </div>

          <div className="space-y-2">
            <Label>New Plan</Label>
            <Select value={selectedPlan} onValueChange={setSelectedPlan}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Select a plan..." />
              </SelectTrigger>
              <SelectContent>
                {availablePlans.map(p => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name} -- {formatCents(p.base_amount_cents)}/{p.billing_interval}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <label className="flex items-start gap-2 text-sm cursor-pointer">
            <Checkbox
              checked={immediate}
              onCheckedChange={(checked) => setImmediate(checked === true)}
              className="mt-0.5"
            />
            <div>
              <span className="font-medium text-foreground">Apply immediately (with proration)</span>
              {immediate && (
                <p className="text-xs text-muted-foreground mt-1">
                  The remaining time on the current billing period will be prorated. A credit or charge will be applied based on the price difference between plans.
                </p>
              )}
            </div>
          </label>

          {error && (
            <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
              <p className="text-destructive text-sm">{error}</p>
            </div>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={submitting || !selectedPlan}>
              {submitting ? (
                <><Loader2 size={14} className="animate-spin mr-2" />Changing...</>
              ) : 'Change Plan'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
