import { useState } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, formatCents, formatDate, formatDateTime, type Subscription, type SubscriptionItem, type Plan, type ItemChangeResult } from '@/lib/api'
import { showApiError } from '@/lib/formErrors'
import { Layout } from '@/components/Layout'
import { ExpiryBadge } from '@/components/ExpiryBadge'
import { cn } from '@/lib/utils'
import { statusBadgeVariant } from '@/lib/status'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogDescription,
} from '@/components/ui/dialog'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { TypedConfirmDialog } from '@/components/TypedConfirmDialog'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'

import { Loader2, Plus } from 'lucide-react'
import { CopyButton } from '@/components/CopyButton'
import { DetailBreadcrumb } from '@/components/DetailBreadcrumb'

const statusVariant = statusBadgeVariant

type ItemDialogState =
  | { kind: 'add' }
  | { kind: 'change-plan'; item: SubscriptionItem }
  | { kind: 'change-quantity'; item: SubscriptionItem }
  | { kind: 'remove'; item: SubscriptionItem }
  | null

export default function SubscriptionDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const [showCancelConfirm, setShowCancelConfirm] = useState(false)
  const [showPauseConfirm, setShowPauseConfirm] = useState(false)
  const [itemDialog, setItemDialog] = useState<ItemDialogState>(null)

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
    queryFn: () => api.listPlans(),
    select: (res) => res.data,
  })

  const planById = (planID: string): Plan | undefined => plansData?.find(p => p.id === planID)
  const items = sub?.items ?? []

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

  // Activity timeline (T0-18) — chronological feed of lifecycle events
  // pulled from the audit log. Separate query key from the period-
  // progress visualization further down; the two are unrelated despite
  // both being called "timeline" in local parlance.
  const { data: activityTimelineData } = useQuery({
    queryKey: ['subscription-activity-timeline', id],
    queryFn: () => api.getSubscriptionTimeline(id!).then(r => r.events || []),
    enabled: !!id,
  })
  const activityTimeline = activityTimelineData ?? []

  const invalidateAll = () => {
    queryClient.invalidateQueries({ queryKey: ['subscription', id] })
    queryClient.invalidateQueries({ queryKey: ['subscription-invoices', id] })
    queryClient.invalidateQueries({ queryKey: ['subscription-preview', id] })
    queryClient.invalidateQueries({ queryKey: ['subscription-activity-timeline', id] })
    queryClient.invalidateQueries({ queryKey: ['subscriptions'] })
  }

  const activateMutation = useMutation({
    mutationFn: () => api.activateSubscription(id!),
    onSuccess: () => { invalidateAll(); toast.success('Subscription activated') },
    onError: (err) => showApiError(err, 'Failed to activate'),
  })

  const pauseMutation = useMutation({
    mutationFn: () => api.pauseSubscription(id!),
    onSuccess: () => { invalidateAll(); toast.success('Subscription paused'); setShowPauseConfirm(false) },
    onError: (err) => showApiError(err, 'Failed to pause'),
  })

  const resumeMutation = useMutation({
    mutationFn: () => api.resumeSubscription(id!),
    onSuccess: () => { invalidateAll(); toast.success('Subscription resumed') },
    onError: (err) => showApiError(err, 'Failed to resume'),
  })

  const cancelMutation = useMutation({
    mutationFn: () => api.cancelSubscription(id!),
    onSuccess: () => { invalidateAll(); toast.success('Subscription canceled'); setShowCancelConfirm(false) },
    onError: (err) => showApiError(err, 'Failed to cancel'),
  })

  const cancelPendingMutation = useMutation({
    mutationFn: (itemID: string) => api.cancelPendingItemChange(id!, itemID),
    onSuccess: () => { invalidateAll(); toast.success('Pending plan change canceled') },
    onError: (err) => showApiError(err, 'Failed to cancel pending change'),
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

  const firstItemPlan = items[0] ? planById(items[0].plan_id) : undefined

  return (
    <Layout>
      <DetailBreadcrumb to="/subscriptions" parentLabel="Subscriptions" currentLabel={sub.display_name} />

      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">{sub.display_name}</h1>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-xs text-muted-foreground font-mono bg-muted px-2 py-0.5 rounded">{sub.id}</span>
            <CopyButton text={sub.id} />
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
          {sub.status === 'trialing' && sub.trial_end_at && (
            <ExpiryBadge expiresAt={sub.trial_end_at} label="Trial ends" warningDays={3} />
          )}
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

        const lastPastIndex = timelinePoints.reduce((acc, p, i) => (p.isPast ? i : acc), -1)
        const progressPercent = lastPastIndex >= 0
          ? (lastPastIndex / (timelinePoints.length - 1)) * 100
          : 0

        return (
          <Card className="mt-6 mb-6">
            <CardContent className="py-5 px-6">
              <div className="relative">
                <div className="absolute left-[calc(0%+6px)] right-[calc(0%+6px)] top-[11px] h-[2px] bg-border" />
                <div
                  className="absolute left-[calc(0%+6px)] top-[11px] h-[2px] bg-primary transition-all duration-300"
                  style={{ width: `calc(${progressPercent}% - 12px)` }}
                />

                <div className="relative flex justify-between">
                  {timelinePoints.map((point, i) => (
                    <div key={i} className="flex flex-col items-center" style={{ width: 90 }}>
                      <div className={cn(
                        'w-6 h-6 rounded-full flex items-center justify-center',
                        point.isPast
                          ? 'bg-primary'
                          : 'bg-background border-2 border-border'
                      )}>
                        {point.isPast && (
                          <svg width="10" height="10" viewBox="0 0 10 10" fill="none">
                            <path d="M2 5L4.5 7.5L8 3" stroke="white" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
                          </svg>
                        )}
                      </div>
                      <p className={cn(
                        'text-xs mt-2 text-center',
                        point.isPast ? 'font-medium text-foreground' : 'text-muted-foreground'
                      )}>{point.label}</p>
                      <p className="text-[11px] text-muted-foreground mt-0.5">{point.date}</p>
                    </div>
                  ))}
                </div>
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
              <p className="text-sm text-muted-foreground">Items</p>
              {items.length === 0 ? (
                <p className="text-lg font-semibold text-foreground mt-1">—</p>
              ) : items.length === 1 && firstItemPlan ? (
                <>
                  <Link to={`/plans/${firstItemPlan.id}`} className="text-lg font-semibold text-primary hover:underline mt-1 block">
                    {firstItemPlan.name}
                  </Link>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    {items[0].quantity > 1 ? `${items[0].quantity} × ` : ''}
                    {formatCents(firstItemPlan.base_amount_cents)}/{firstItemPlan.billing_interval === 'yearly' ? 'yr' : 'mo'}
                  </p>
                </>
              ) : (
                <p className="text-lg font-semibold text-foreground mt-1">{items.length} items</p>
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

      {/* Items */}
      <Card className="mt-6">
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle className="text-sm">Items ({items.length})</CardTitle>
            {sub.status === 'active' && plansData && (
              <Button size="sm" variant="outline" onClick={() => setItemDialog({ kind: 'add' })}>
                <Plus size={14} className="mr-1" /> Add Item
              </Button>
            )}
          </div>
        </CardHeader>
        <CardContent className="p-0">
          {items.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-8">
              No items on this subscription yet
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Plan</TableHead>
                  <TableHead className="text-right">Quantity</TableHead>
                  <TableHead>Pending Change</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map(item => {
                  const itemPlan = planById(item.plan_id)
                  const pendingPlan = item.pending_plan_id ? planById(item.pending_plan_id) : undefined
                  return (
                    <TableRow key={item.id}>
                      <TableCell>
                        {itemPlan ? (
                          <div>
                            <Link to={`/plans/${itemPlan.id}`} className="text-sm font-medium text-primary hover:underline">
                              {itemPlan.name}
                            </Link>
                            <p className="text-xs text-muted-foreground mt-0.5">
                              {formatCents(itemPlan.base_amount_cents)}/{itemPlan.billing_interval === 'yearly' ? 'yr' : 'mo'}
                            </p>
                          </div>
                        ) : (
                          <span className="text-sm text-foreground font-mono">{item.plan_id.slice(0, 8)}...</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right text-sm font-medium">{item.quantity}</TableCell>
                      <TableCell>
                        {item.pending_plan_id ? (
                          <div className="flex items-center gap-2">
                            <Badge variant="outline">
                              → {pendingPlan?.name || item.pending_plan_id.slice(0, 8) + '...'}
                            </Badge>
                            {item.pending_plan_effective_at && (
                              <span className="text-xs text-muted-foreground">
                                {formatDate(item.pending_plan_effective_at)}
                              </span>
                            )}
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 text-xs"
                              disabled={cancelPendingMutation.isPending}
                              onClick={() => cancelPendingMutation.mutate(item.id)}
                            >
                              Cancel
                            </Button>
                          </div>
                        ) : (
                          <span className="text-xs text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right">
                        {sub.status === 'active' && (
                          <div className="flex items-center gap-2 justify-end">
                            <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => setItemDialog({ kind: 'change-quantity', item })}>
                              Change Qty
                            </Button>
                            <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => setItemDialog({ kind: 'change-plan', item })}>
                              Change Plan
                            </Button>
                            <Button size="sm" variant="outline" className="h-7 text-xs border-destructive text-destructive hover:bg-destructive/10" onClick={() => setItemDialog({ kind: 'remove', item })}>
                              Remove
                            </Button>
                          </div>
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
                <CopyButton text={sub.id} />
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

      {/* Activity Timeline (T0-18) — lifecycle audit feed. Mirrors the
          invoice payment-activity panel so CS reps see the same shape
          on both resources. Hidden when there's nothing to show. */}
      {activityTimeline.length > 0 && (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle className="text-sm">Activity</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="relative">
              {activityTimeline.map((event, i) => (
                <div key={i} className="flex gap-4 pb-4 last:pb-0">
                  <div className="flex flex-col items-center">
                    <div className={cn(
                      'w-2.5 h-2.5 rounded-full mt-1.5',
                      event.status === 'succeeded' || event.status === 'resolved' ? 'bg-emerald-500' :
                      event.status === 'canceled' ? 'bg-destructive' :
                      event.status === 'warning' ? 'bg-amber-500' :
                      event.status === 'escalated' ? 'bg-violet-500' :
                      'bg-blue-500'
                    )} />
                    {i < activityTimeline.length - 1 && <div className="w-px flex-1 bg-border mt-1" />}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center justify-between">
                      <p className="text-sm text-foreground">{event.description}</p>
                      <span className="text-xs text-muted-foreground ml-4 whitespace-nowrap">{formatDateTime(event.timestamp)}</span>
                    </div>
                    {(event.actor_name || event.actor_type) && (
                      <p className="text-xs text-muted-foreground mt-0.5">
                        by {event.actor_name || event.actor_type}
                      </p>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

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
      <TypedConfirmDialog
        open={showCancelConfirm}
        onOpenChange={setShowCancelConfirm}
        title="Cancel Subscription"
        description="Cancelling ends billing and stops future invoices for this subscription. This action cannot be undone."
        confirmWord="CANCEL"
        confirmLabel="Cancel Subscription"
        onConfirm={() => cancelMutation.mutate()}
        loading={cancelMutation.isPending}
      />

      {/* Add Item */}
      {itemDialog?.kind === 'add' && plansData && (
        <AddItemDialog
          subscription={sub}
          plans={plansData}
          existingPlanIDs={items.map(i => i.plan_id)}
          onClose={() => setItemDialog(null)}
          onAdded={() => { setItemDialog(null); invalidateAll(); toast.success('Item added') }}
        />
      )}

      {/* Change Item Plan */}
      {itemDialog?.kind === 'change-plan' && plansData && (
        <ChangeItemPlanDialog
          subscriptionID={sub.id}
          item={itemDialog.item}
          plans={plansData}
          existingPlanIDs={items.map(i => i.plan_id)}
          onClose={() => setItemDialog(null)}
          onChanged={(res) => {
            setItemDialog(null)
            invalidateAll()
            if (res.proration) {
              if (res.proration.type === 'invoice') {
                toast.success(`Proration invoice created for ${formatCents(res.proration.amount_cents)}`)
              } else {
                toast.success(`${formatCents(Math.abs(res.proration.amount_cents))} credited to customer balance`)
              }
            } else {
              toast.success('Plan change saved')
            }
          }}
        />
      )}

      {/* Change Item Quantity */}
      {itemDialog?.kind === 'change-quantity' && (
        <ChangeItemQuantityDialog
          subscriptionID={sub.id}
          item={itemDialog.item}
          plan={planById(itemDialog.item.plan_id)}
          onClose={() => setItemDialog(null)}
          onChanged={(res) => {
            setItemDialog(null)
            invalidateAll()
            if (res.proration) {
              if (res.proration.type === 'invoice') {
                toast.success(`Proration invoice created for ${formatCents(res.proration.amount_cents)}`)
              } else {
                toast.success(`${formatCents(Math.abs(res.proration.amount_cents))} credited to customer balance`)
              }
            } else {
              toast.success('Quantity updated')
            }
          }}
        />
      )}

      {/* Remove Item */}
      {itemDialog?.kind === 'remove' && (
        <RemoveItemConfirm
          subscriptionID={sub.id}
          item={itemDialog.item}
          plan={planById(itemDialog.item.plan_id)}
          onClose={() => setItemDialog(null)}
          onRemoved={() => { setItemDialog(null); invalidateAll(); toast.success('Item removed') }}
        />
      )}
    </Layout>
  )
}

// AddItemDialog picks a plan + quantity and POSTs to /subscriptions/:id/items.
// The backend rejects duplicates on (subscription, plan); we pre-filter to
// keep the dropdown clean.
function AddItemDialog({ subscription, plans, existingPlanIDs, onClose, onAdded }: {
  subscription: Subscription
  plans: Plan[]
  existingPlanIDs: string[]
  onClose: () => void
  onAdded: () => void
}) {
  const [selectedPlan, setSelectedPlan] = useState('')
  const [quantity, setQuantity] = useState('1')
  const [submitting, setSubmitting] = useState(false)

  const availablePlans = plans.filter(p => p.status === 'active' && !existingPlanIDs.includes(p.id))

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!selectedPlan) return
    const qty = parseInt(quantity, 10)
    if (!Number.isFinite(qty) || qty < 1) {
      toast.error('Quantity must be a positive integer')
      return
    }
    setSubmitting(true)
    try {
      await api.addSubscriptionItem(subscription.id, { plan_id: selectedPlan, quantity: qty })
      onAdded()
    } catch (err) {
      showApiError(err, 'Failed to add item')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add Item</DialogTitle>
          <DialogDescription>
            Add a plan to this subscription. Mid-cycle adds are prorated against the remaining period.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} noValidate className="space-y-4">
          <div className="space-y-2">
            <Label>Plan</Label>
            <Select value={selectedPlan} onValueChange={(v) => setSelectedPlan(v ?? '')}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Select a plan..." />
              </SelectTrigger>
              <SelectContent>
                {availablePlans.length === 0 ? (
                  <div className="px-2 py-2 text-sm text-muted-foreground">
                    No plans available — all active plans are already on this subscription.
                  </div>
                ) : availablePlans.map(p => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name} — {formatCents(p.base_amount_cents)}/{p.billing_interval}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <Label htmlFor="add-qty">Quantity</Label>
            <Input
              id="add-qty"
              type="number"
              min="1"
              value={quantity}
              onChange={(e) => setQuantity(e.target.value)}
            />
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={submitting || !selectedPlan}>
              {submitting ? (
                <><Loader2 size={14} className="animate-spin mr-2" />Adding...</>
              ) : 'Add Item'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ChangeItemPlanDialog swaps an item's plan. Immediate swaps prorate; deferred
// swaps stamp pending_plan_id and apply at the next cycle boundary.
function ChangeItemPlanDialog({ subscriptionID, item, plans, existingPlanIDs, onClose, onChanged }: {
  subscriptionID: string
  item: SubscriptionItem
  plans: Plan[]
  existingPlanIDs: string[]
  onClose: () => void
  onChanged: (res: ItemChangeResult) => void
}) {
  const [selectedPlan, setSelectedPlan] = useState('')
  const [immediate, setImmediate] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  // Candidates: active plans that aren't the current item's plan and that
  // aren't already attached to another item on this subscription (the backend
  // rejects the duplicate with 409).
  const availablePlans = plans.filter(
    p => p.status === 'active' && p.id !== item.plan_id && !existingPlanIDs.includes(p.id),
  )
  const currentPlan = plans.find(p => p.id === item.plan_id)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!selectedPlan) return
    setSubmitting(true)
    try {
      const res = await api.updateSubscriptionItem(subscriptionID, item.id, {
        new_plan_id: selectedPlan,
        immediate,
      })
      onChanged(res)
    } catch (err) {
      showApiError(err, 'Failed to change plan')
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
            <p className="text-sm font-semibold text-foreground mt-0.5">
              {currentPlan?.name || item.plan_id}
            </p>
          </div>

          <div className="space-y-2">
            <Label>New Plan</Label>
            <Select value={selectedPlan} onValueChange={(v) => setSelectedPlan(v ?? '')}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Select a plan..." />
              </SelectTrigger>
              <SelectContent>
                {availablePlans.length === 0 ? (
                  <div className="px-2 py-2 text-sm text-muted-foreground">No other plans available</div>
                ) : availablePlans.map(p => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name} — {formatCents(p.base_amount_cents)}/{p.billing_interval}
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
              {immediate ? (
                <p className="text-xs text-muted-foreground mt-1">
                  The remaining time on the current billing period will be prorated. A credit or charge is applied based on the price difference between plans.
                </p>
              ) : (
                <p className="text-xs text-muted-foreground mt-1">
                  Scheduled — the plan swap will apply at the next billing cycle boundary. No proration.
                </p>
              )}
            </div>
          </label>

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

// ChangeItemQuantityDialog updates a single item's quantity. The backend
// rejects no-ops (new qty == current qty) with a 422 — we pre-check to keep
// the UX tight.
function ChangeItemQuantityDialog({ subscriptionID, item, plan, onClose, onChanged }: {
  subscriptionID: string
  item: SubscriptionItem
  plan: Plan | undefined
  onClose: () => void
  onChanged: (res: ItemChangeResult) => void
}) {
  const [quantity, setQuantity] = useState(String(item.quantity))
  const [submitting, setSubmitting] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const qty = parseInt(quantity, 10)
    if (!Number.isFinite(qty) || qty < 1) {
      toast.error('Quantity must be a positive integer')
      return
    }
    if (qty === item.quantity) {
      toast.error('New quantity is the same as current quantity')
      return
    }
    setSubmitting(true)
    try {
      const res = await api.updateSubscriptionItem(subscriptionID, item.id, { quantity: qty })
      onChanged(res)
    } catch (err) {
      showApiError(err, 'Failed to change quantity')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Change Quantity</DialogTitle>
          <DialogDescription>
            Applied immediately. Quantity increases charge prorated incrementally; decreases credit the customer.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} noValidate className="space-y-4">
          <div>
            <p className="text-sm text-muted-foreground">Plan</p>
            <p className="text-sm font-semibold text-foreground mt-0.5">{plan?.name || item.plan_id}</p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="qty-input">Quantity</Label>
            <Input
              id="qty-input"
              type="number"
              min="1"
              value={quantity}
              onChange={(e) => setQuantity(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">Current: {item.quantity}</p>
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={submitting}>
              {submitting ? (
                <><Loader2 size={14} className="animate-spin mr-2" />Saving...</>
              ) : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// RemoveItemConfirm deletes an item. Removing mid-period generates a credit
// for the unused portion of the just-paid period on the backend — surface
// that explicitly so the operator isn't surprised.
function RemoveItemConfirm({ subscriptionID, item, plan, onClose, onRemoved }: {
  subscriptionID: string
  item: SubscriptionItem
  plan: Plan | undefined
  onClose: () => void
  onRemoved: () => void
}) {
  const [submitting, setSubmitting] = useState(false)

  const handleConfirm = async () => {
    setSubmitting(true)
    try {
      await api.removeSubscriptionItem(subscriptionID, item.id)
      onRemoved()
    } catch (err) {
      showApiError(err, 'Failed to remove item')
      setSubmitting(false)
    }
  }

  return (
    <AlertDialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Remove item from subscription?</AlertDialogTitle>
          <AlertDialogDescription>
            Remove <strong>{plan?.name || item.plan_id}</strong> (qty {item.quantity}) from this subscription. If removed mid-period, a credit will be issued for the unused portion.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={submitting}>Cancel</AlertDialogCancel>
          <AlertDialogAction variant="destructive" onClick={handleConfirm} disabled={submitting}>
            {submitting ? 'Removing...' : 'Remove Item'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
