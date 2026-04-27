import { useState, useMemo, useEffect } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient, useInfiniteQuery } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatCents, formatDate, formatDateTime, type Coupon, type CouponRedemption, type Plan } from '@/lib/api'
import { applyApiError, showApiError } from '@/lib/formErrors'
import { Layout } from '@/components/Layout'
import { ExpiryBadge } from '@/components/ExpiryBadge'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { DatePicker } from '@/components/ui/date-picker'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage,
} from '@/components/ui/form'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'

import { Loader2, Archive, ArchiveRestore, Lock, Calculator, CopyPlus, Pencil } from 'lucide-react'
import { CopyButton } from '@/components/CopyButton'
import { DetailBreadcrumb } from '@/components/DetailBreadcrumb'
import { CustomerCombobox } from '@/components/CustomerCombobox'

// Mirrors couponStatus in Coupons.tsx. Duplicated (not extracted) so
// the two pages can evolve independently — the list page may later
// fold archived into a separate tab while the detail page always
// shows the true status. Extraction can come when the signatures
// genuinely align.
function couponStatus(c: Coupon): string {
  if (c.archived_at) return 'archived'
  if (c.expires_at && new Date(c.expires_at) < new Date()) return 'expired'
  if (c.max_redemptions !== null && c.times_redeemed >= c.max_redemptions) return 'maxed'
  return 'active'
}

function statusLabel(status: string): string {
  return status.charAt(0).toUpperCase() + status.slice(1)
}

function statusVariant(status: string): 'success' | 'secondary' | 'danger' | 'warning' | 'outline' {
  switch (status) {
    case 'active': return 'success'
    case 'expired': return 'secondary'
    case 'maxed': return 'warning'
    case 'archived': return 'danger'
    default: return 'outline'
  }
}

function formatDiscount(c: Coupon): string {
  if (c.type === 'percentage') {
    const pct = c.percent_off_bp / 100
    return Number.isInteger(pct) ? `${pct}%` : `${pct.toFixed(2)}%`
  }
  return formatCents(c.amount_off, c.currency)
}

function durationLabel(c: Coupon): string {
  switch (c.duration) {
    case 'once': return 'Once'
    case 'repeating':
      return c.duration_periods
        ? `Repeating — ${c.duration_periods} period${c.duration_periods === 1 ? '' : 's'}`
        : 'Repeating'
    case 'forever': return 'Forever'
    default: return '—'
  }
}

export default function CouponDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [archiveOpen, setArchiveOpen] = useState(false)
  const [unarchiveOpen, setUnarchiveOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)

  const { data: coupon, isLoading, error: loadError, refetch } = useQuery({
    queryKey: ['coupon', id],
    queryFn: () => api.getCoupon(id!),
    enabled: !!id,
  })

  // Plans + redemptions load in parallel with the coupon — they don't
  // block the main view but the data is ready by the time the operator
  // scrolls down to the restrictions / redemptions cards.
  const { data: plansData } = useQuery({
    queryKey: ['plans'],
    queryFn: () => api.listPlans(),
    enabled: !!id,
  })

  // Redemptions page in 25s. Seek-cursor pagination: API returns
  // next_cursor / has_more (see internal/api/middleware/pagination.go).
  // Using useInfiniteQuery so react-query handles accumulation + caching
  // rather than us re-implementing a page stack in useState.
  const {
    data: redemptionsPages,
    isLoading: loadingRedemptions,
    isFetchingNextPage,
    fetchNextPage,
    hasNextPage,
  } = useInfiniteQuery({
    queryKey: ['coupon-redemptions', id],
    queryFn: ({ pageParam }) => {
      const params = new URLSearchParams({ limit: '25' })
      if (pageParam) params.set('cursor', pageParam)
      return api.listCouponRedemptions(id!, params.toString())
    },
    initialPageParam: '' as string,
    getNextPageParam: (last) => (last.has_more ? last.next_cursor : undefined),
    enabled: !!id,
  })
  const redemptions = useMemo(
    () => (redemptionsPages?.pages ?? []).flatMap(p => p.data ?? []),
    [redemptionsPages],
  )

  // Look up customer names so the redemption table shows "Acme Corp"
  // instead of "cus_01HV..." — Stripe-parity. Keeping this scoped to the
  // detail page rather than expanding the redemption wire payload so the
  // API stays minimal; join happens client-side against the already-cached
  // customers list.
  const { data: customersData } = useQuery({
    queryKey: ['customers'],
    queryFn: () => api.listCustomers(),
    staleTime: 30_000,
    enabled: !!id,
  })
  const customerById = useMemo(() => {
    const list = customersData?.data ?? []
    const map = new Map<string, { name: string; email: string }>()
    for (const c of list) {
      map.set(c.id, { name: c.display_name || c.external_id || c.id, email: c.email ?? '' })
    }
    return map
  }, [customersData])

  const restrictedPlans = useMemo(() => {
    if (!coupon?.plan_ids?.length) return []
    const plans = plansData?.data ?? []
    return coupon.plan_ids
      .map(pid => plans.find(p => p.id === pid))
      .filter((p): p is Plan => !!p)
  }, [coupon, plansData])

  const archiveMutation = useMutation({
    mutationFn: () => api.archiveCoupon(id!),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['coupon', id] })
      queryClient.invalidateQueries({ queryKey: ['coupons'] })
      setArchiveOpen(false)
      toast.success('Coupon archived')
    },
    onError: (err) => showApiError(err, 'Failed to archive'),
  })

  const unarchiveMutation = useMutation({
    mutationFn: () => api.unarchiveCoupon(id!),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['coupon', id] })
      queryClient.invalidateQueries({ queryKey: ['coupons'] })
      setUnarchiveOpen(false)
      toast.success('Coupon restored')
    },
    onError: (err) => showApiError(err, 'Failed to restore'),
  })

  if (isLoading) {
    return (
      <Layout>
        <DetailBreadcrumb to="/coupons" parentLabel="Coupons" currentLabel="Loading..." />
        <div className="flex justify-center py-16">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      </Layout>
    )
  }

  if (loadError) {
    const msg = loadError instanceof Error ? loadError.message : String(loadError)
    return (
      <Layout>
        <DetailBreadcrumb to="/coupons" parentLabel="Coupons" currentLabel="Error" />
        <div className="py-16 text-center">
          <p className="text-sm text-destructive mb-3">{msg}</p>
          <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
        </div>
      </Layout>
    )
  }

  if (!coupon) {
    return (
      <Layout>
        <DetailBreadcrumb to="/coupons" parentLabel="Coupons" currentLabel="Not found" />
        <p className="text-sm text-muted-foreground py-16 text-center">Coupon not found</p>
        <div className="text-center">
          <Button variant="outline" size="sm" onClick={() => navigate('/coupons')}>
            Back to coupons
          </Button>
        </div>
      </Layout>
    )
  }

  const status = couponStatus(coupon)
  const isArchived = status === 'archived'
  const restrictions = coupon.restrictions ?? {}
  const hasRestrictions =
    restrictions.min_amount_cents ||
    restrictions.first_time_customer_only ||
    restrictions.max_redemptions_per_customer

  return (
    <Layout>
      <DetailBreadcrumb to="/coupons" parentLabel="Coupons" currentLabel={coupon.name || coupon.code} />

      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold text-foreground">{coupon.name || coupon.code}</h1>
            {coupon.customer_id && (
              <span className="inline-flex items-center gap-1 text-xs text-muted-foreground bg-muted px-2 py-1 rounded">
                <Lock size={12} /> Private
              </span>
            )}
          </div>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-xs text-muted-foreground font-mono bg-muted px-2 py-0.5 rounded">{coupon.code}</span>
            <CopyButton text={coupon.code} />
            <span className="text-xs text-muted-foreground font-mono bg-muted px-2 py-0.5 rounded">{coupon.id}</span>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <Badge variant={statusVariant(status)}>{statusLabel(status)}</Badge>
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate(`/coupons?duplicate=${coupon.id}`)}
          >
            <CopyPlus size={14} className="mr-1.5" />
            Duplicate
          </Button>
          {!isArchived && (
            <Button variant="outline" size="sm" onClick={() => setEditOpen(true)}>
              <Pencil size={14} className="mr-1.5" />
              Edit
            </Button>
          )}
          {isArchived ? (
            <Button variant="outline" size="sm" onClick={() => setUnarchiveOpen(true)}>
              <ArchiveRestore size={14} className="mr-1.5" />
              Restore
            </Button>
          ) : (
            <Button variant="outline" size="sm" onClick={() => setArchiveOpen(true)} className="text-destructive hover:text-destructive">
              <Archive size={14} className="mr-1.5" />
              Archive
            </Button>
          )}
        </div>
      </div>

      {/* Key metrics */}
      <Card>
        <CardContent className="p-0">
          <div className="flex divide-x divide-border">
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Discount</p>
              <p className="text-lg font-semibold text-foreground mt-1 tabular-nums">{formatDiscount(coupon)}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Redemptions</p>
              <p className="text-lg font-semibold text-foreground mt-1 tabular-nums">
                {coupon.times_redeemed}
                {coupon.max_redemptions !== null && (
                  <span className="text-sm text-muted-foreground"> / {coupon.max_redemptions}</span>
                )}
              </p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Duration</p>
              <p className="text-lg font-semibold text-foreground mt-1">{durationLabel(coupon)}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Expires</p>
              <p className="text-lg font-semibold text-foreground mt-1">
                {coupon.expires_at ? (
                  <span className="flex items-center gap-2">
                    <span>{formatDate(coupon.expires_at)}</span>
                    <ExpiryBadge expiresAt={coupon.expires_at} warningDays={7} />
                  </span>
                ) : (
                  <span className="text-muted-foreground text-base font-normal">No expiry</span>
                )}
              </p>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Preview — runs the real server-side discount calculator against a
          sample customer + subtotal, so operators can sanity-check an
          unfamiliar coupon without minting a throwaway invoice. This is
          the coupon equivalent of a pricing calculator. */}
      <CouponPreviewCard coupon={coupon} restrictedPlans={restrictedPlans} />

      {/* Properties */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">Properties</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            <PropRow label="Code">
              <span className="text-sm text-foreground font-mono">{coupon.code}</span>
            </PropRow>
            <PropRow label="Type">
              <Badge variant="info">{coupon.type === 'percentage' ? 'Percentage' : 'Fixed amount'}</Badge>
            </PropRow>
            {coupon.type === 'fixed_amount' && (
              <PropRow label="Currency">
                <span className="text-sm text-foreground font-medium">{coupon.currency?.toUpperCase()}</span>
              </PropRow>
            )}
            <PropRow label="Duration">
              <span className="text-sm text-foreground">{durationLabel(coupon)}</span>
            </PropRow>
            <PropRow label="Stackable">
              <Badge variant={coupon.stackable ? 'success' : 'outline'}>
                {coupon.stackable ? 'Yes' : 'No'}
              </Badge>
            </PropRow>
            <PropRow label="Status">
              <Badge variant={statusVariant(status)}>{statusLabel(status)}</Badge>
            </PropRow>
            <PropRow label="Created">
              <span className="text-sm text-foreground">{formatDateTime(coupon.created_at)}</span>
            </PropRow>
            <PropRow label="ID">
              <div className="flex items-center gap-2">
                <span className="text-sm text-foreground font-mono">{coupon.id}</span>
                <CopyButton text={coupon.id} />
              </div>
            </PropRow>
          </div>
        </CardContent>
      </Card>

      {/* Restrictions — only render when any restriction is set. An empty
          restrictions block would be visual noise on the common case where
          the operator has nothing to check. */}
      {hasRestrictions && (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle className="text-sm">Restrictions</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            <div className="divide-y divide-border">
              {restrictions.min_amount_cents ? (
                <PropRow label="Minimum purchase">
                  <span className="text-sm text-foreground tabular-nums">
                    {formatCents(restrictions.min_amount_cents, coupon.currency)}
                  </span>
                </PropRow>
              ) : null}
              {restrictions.max_redemptions_per_customer ? (
                <PropRow label="Max redemptions per customer">
                  <span className="text-sm text-foreground tabular-nums">
                    {restrictions.max_redemptions_per_customer}
                  </span>
                </PropRow>
              ) : null}
              {restrictions.first_time_customer_only ? (
                <PropRow label="First-time customers only">
                  <Badge variant="info">Yes</Badge>
                </PropRow>
              ) : null}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Plan restrictions — rendered only when the coupon is actually
          scoped. An all-plans coupon doesn't need an empty card. */}
      {restrictedPlans.length > 0 && (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle className="text-sm">Applies to plans ({restrictedPlans.length})</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            <div className="divide-y divide-border">
              {restrictedPlans.map(p => (
                <Link key={p.id} to={`/plans/${p.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-accent/50 transition-colors">
                  <div>
                    <p className="text-sm font-medium text-foreground">{p.name}</p>
                    <p className="text-xs text-muted-foreground font-mono mt-0.5">{p.code}</p>
                  </div>
                  <span className="text-xs text-muted-foreground">{formatCents(p.base_amount_cents)} / {p.billing_interval}</span>
                </Link>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Redemptions */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">
            Redemptions ({coupon.times_redeemed})
          </CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {loadingRedemptions ? (
            <div className="flex justify-center py-8">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : redemptions.length === 0 ? (
            <p className="px-6 py-8 text-sm text-muted-foreground text-center">
              No redemptions yet
            </p>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="text-xs font-medium">Customer</TableHead>
                    <TableHead className="text-xs font-medium">Invoice</TableHead>
                    <TableHead className="text-xs font-medium text-right">Discount</TableHead>
                    <TableHead className="text-xs font-medium">Date</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {redemptions.map((r: CouponRedemption) => {
                    const cust = customerById.get(r.customer_id)
                    return (
                      <TableRow key={r.id}>
                        <TableCell>
                          <Link
                            to={`/customers/${r.customer_id}`}
                            className="text-sm text-primary hover:underline"
                          >
                            {cust?.name ?? r.customer_id}
                          </Link>
                          {cust?.email && (
                            <div className="text-xs text-muted-foreground">{cust.email}</div>
                          )}
                        </TableCell>
                        <TableCell>
                          {r.invoice_id ? (
                            <Link
                              to={`/invoices/${r.invoice_id}`}
                              className="text-sm font-mono text-primary hover:underline"
                            >
                              {r.invoice_id.slice(0, 12)}…
                            </Link>
                          ) : (
                            <span className="text-xs text-muted-foreground">—</span>
                          )}
                        </TableCell>
                        <TableCell className="text-right tabular-nums font-mono text-sm text-emerald-600">
                          {formatCents(r.discount_cents)}
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {formatDateTime(r.created_at)}
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
              {hasNextPage && (
                <div className="border-t border-border px-6 py-3 flex justify-center">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => fetchNextPage()}
                    disabled={isFetchingNextPage}
                  >
                    {isFetchingNextPage ? (
                      <><Loader2 className="h-3 w-3 animate-spin mr-2" /> Loading…</>
                    ) : (
                      'Load more'
                    )}
                  </Button>
                </div>
              )}
            </>
          )}
        </CardContent>
      </Card>

      {/* Archive confirm */}
      <AlertDialog open={archiveOpen} onOpenChange={setArchiveOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Archive Coupon</AlertDialogTitle>
            <AlertDialogDescription>
              This coupon will stop accepting new redemptions. Existing redemptions continue
              to apply until their duration is exhausted. You can restore it at any time.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => archiveMutation.mutate()}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Archive
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Unarchive confirm — symmetry with archive. #329 tracks this dialog
          showing up in the list page too so operators aren't surprised by
          mid-list restore-side-effects. */}
      <AlertDialog open={unarchiveOpen} onOpenChange={setUnarchiveOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Restore Coupon</AlertDialogTitle>
            <AlertDialogDescription>
              This coupon will start accepting new redemptions again. If it has an
              expiry date in the past or is already at max redemptions, restoring
              won&apos;t re-enable it on its own.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => unarchiveMutation.mutate()}>
              Restore
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <EditCouponDialog
        open={editOpen}
        onOpenChange={setEditOpen}
        coupon={coupon}
      />
    </Layout>
  )
}

function PropRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between px-6 py-3">
      <span className="text-sm text-muted-foreground">{label}</span>
      {children}
    </div>
  )
}

// CouponPreviewCard lets an operator run the real server-side discount
// path (same code the invoice pipeline uses) against a customer + subtotal
// to confirm what will happen before they commit. Server errors (gates
// failing, customer not eligible, etc.) surface inline so the operator
// sees them in context rather than as a toast that vanishes.
function CouponPreviewCard({
  coupon,
  restrictedPlans,
}: {
  coupon: Coupon
  restrictedPlans: Plan[]
}) {
  const [customerId, setCustomerId] = useState(coupon.customer_id ?? '')
  const [subtotal, setSubtotal] = useState('')
  const [planId, setPlanId] = useState('')
  const [result, setResult] = useState<{ discount: number; subtotal: number } | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Private coupons are bound to one customer, so the picker is hidden;
  // for public coupons the CustomerCombobox handles its own fetch.
  const previewMutation = useMutation({
    mutationFn: async () => {
      const subtotalCents = Math.round(parseFloat(subtotal) * 100)
      return api.previewCoupon({
        code: coupon.code,
        customer_id: customerId,
        subtotal_cents: subtotalCents,
        ...(planId ? { plan_id: planId } : {}),
        ...(coupon.type === 'fixed_amount' && coupon.currency ? { currency: coupon.currency } : {}),
      })
    },
    onSuccess: (res) => {
      setResult({ discount: res.discount_cents, subtotal: Math.round(parseFloat(subtotal) * 100) })
      setError(null)
    },
    onError: (err) => {
      setResult(null)
      setError(err instanceof Error ? err.message : 'Preview failed')
    },
  })

  const canSubmit = customerId && subtotal && parseFloat(subtotal) > 0

  return (
    <Card className="mt-6">
      <CardHeader>
        <CardTitle className="text-sm flex items-center gap-2">
          <Calculator size={14} />
          Preview Discount
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
          <div>
            <Label className="text-xs text-muted-foreground">Customer</Label>
            {coupon.customer_id ? (
              <Input
                value={coupon.customer_id}
                disabled
                className="font-mono text-xs mt-1"
                title="Private coupon — customer is fixed"
              />
            ) : (
              <div className="mt-1">
                <CustomerCombobox
                  value={customerId}
                  onChange={setCustomerId}
                />
              </div>
            )}
          </div>
          <div>
            <Label className="text-xs text-muted-foreground">
              Subtotal {coupon.type === 'fixed_amount' && coupon.currency ? `(${coupon.currency.toUpperCase()})` : ''}
            </Label>
            <Input
              type="number"
              step="0.01"
              min="0"
              placeholder="100.00"
              value={subtotal}
              onChange={e => setSubtotal(e.target.value)}
              className="mt-1"
            />
          </div>
          {restrictedPlans.length > 0 && (
            <div>
              <Label className="text-xs text-muted-foreground">Plan (optional)</Label>
              <select
                value={planId}
                onChange={e => setPlanId(e.target.value)}
                className="mt-1 flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              >
                <option value="">Any plan</option>
                {restrictedPlans.map(p => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
              </select>
            </div>
          )}
        </div>

        <div className="flex items-center justify-between mt-4">
          <Button
            size="sm"
            disabled={!canSubmit || previewMutation.isPending}
            onClick={() => { setError(null); previewMutation.mutate() }}
          >
            {previewMutation.isPending ? (
              <><Loader2 size={14} className="animate-spin mr-2" />Calculating...</>
            ) : (
              'Calculate discount'
            )}
          </Button>

          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}

          {result && !error && (
            <div className="flex items-center gap-6 text-sm">
              <div>
                <span className="text-muted-foreground">Discount: </span>
                <span className="tabular-nums font-mono font-medium text-emerald-600">
                  −{formatCents(result.discount, coupon.currency)}
                </span>
              </div>
              <div>
                <span className="text-muted-foreground">Final: </span>
                <span className="tabular-nums font-mono font-medium text-foreground">
                  {formatCents(result.subtotal - result.discount, coupon.currency)}
                </span>
              </div>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

// Edit dialog covers the Stripe-parity mutable subset: name, max_redemptions,
// expires_at, restrictions. Discount type/value, currency, duration,
// stackable, plan_ids, and customer_id are write-once at create — changing
// them after the fact would silently re-price open subscriptions or
// invalidate redemptions already on file. Operators who need that should
// archive + create a new coupon (the Duplicate button is right next door).
const editCouponSchema = z.object({
  name: z.string().min(1, 'Name is required'),
  maxRedemptions: z.string(),
  expiresAt: z.string(),
  minAmount: z.string(),
  firstTimeCustomerOnly: z.boolean(),
  maxRedemptionsPerCustomer: z.string(),
})

type EditCouponData = z.infer<typeof editCouponSchema>

function couponToFormValues(c: Coupon): EditCouponData {
  return {
    name: c.name ?? '',
    maxRedemptions: c.max_redemptions !== null ? String(c.max_redemptions) : '',
    expiresAt: c.expires_at ? c.expires_at.slice(0, 10) : '',
    minAmount: c.restrictions?.min_amount_cents
      ? (c.restrictions.min_amount_cents / 100).toFixed(2)
      : '',
    firstTimeCustomerOnly: c.restrictions?.first_time_customer_only ?? false,
    maxRedemptionsPerCustomer: c.restrictions?.max_redemptions_per_customer
      ? String(c.restrictions.max_redemptions_per_customer)
      : '',
  }
}

function EditCouponDialog({
  open,
  onOpenChange,
  coupon,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  coupon: Coupon
}) {
  const queryClient = useQueryClient()

  const form = useForm<EditCouponData>({
    resolver: zodResolver(editCouponSchema),
    defaultValues: couponToFormValues(coupon),
  })

  // Re-seed the form on the open transition so a stale form (operator
  // closed without saving, then re-opened after the coupon was edited
  // elsewhere) doesn't show stale values. Keyed on [open, coupon.id]
  // rather than [coupon] so an unrelated query refetch doesn't
  // trample mid-edit values.
  useEffect(() => {
    if (open) form.reset(couponToFormValues(coupon))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, coupon.id])

  const updateMutation = useMutation({
    mutationFn: async (data: EditCouponData) => {
      // Backend uses full-overwrite semantics on `restrictions` — the
      // wire decoder copies the whole object onto the row. So we always
      // build the full object from form state; an empty form-state means
      // {} on the wire, which clears all restrictions in one shot.
      const restrictions: NonNullable<Parameters<typeof api.updateCoupon>[1]['restrictions']> = {}
      if (data.minAmount.trim()) {
        restrictions.min_amount_cents = Math.round(parseFloat(data.minAmount) * 100)
      }
      if (data.firstTimeCustomerOnly) {
        restrictions.first_time_customer_only = true
      }
      if (data.maxRedemptionsPerCustomer.trim()) {
        restrictions.max_redemptions_per_customer = parseInt(data.maxRedemptionsPerCustomer, 10)
      }

      return api.updateCoupon(coupon.id, {
        name: data.name.trim(),
        max_redemptions: data.maxRedemptions.trim() ? parseInt(data.maxRedemptions, 10) : null,
        expires_at: data.expiresAt.trim() ? new Date(data.expiresAt).toISOString() : null,
        restrictions,
      })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['coupon', coupon.id] })
      queryClient.invalidateQueries({ queryKey: ['coupons'] })
      toast.success('Coupon updated')
      onOpenChange(false)
    },
    onError: (err) => {
      applyApiError(form, err, {
        name: 'name',
        max_redemptions: 'maxRedemptions',
        expires_at: 'expiresAt',
        'restrictions.min_amount_cents': 'minAmount',
        'restrictions.max_redemptions_per_customer': 'maxRedemptionsPerCustomer',
      })
    },
  })

  const onSubmit = form.handleSubmit((data) => updateMutation.mutate(data))

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Edit Coupon</DialogTitle>
          <DialogDescription>
            Update the lifecycle and restriction fields. Discount value, type,
            duration, and plan/customer scope are write-once — duplicate the
            coupon if those need to change.
          </DialogDescription>
        </DialogHeader>
        <Form {...form}>
          <form onSubmit={onSubmit} noValidate className="space-y-4">
            <FormField
              control={form.control}
              name="name"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Name</FormLabel>
                  <FormControl>
                    <Input placeholder="Launch Discount" {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name="maxRedemptions"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Max Redemptions</FormLabel>
                  <FormControl>
                    <Input type="number" min="1" step="1" placeholder="Unlimited" {...field} />
                  </FormControl>
                  <FormDescription>
                    Leave empty for unlimited. Currently {coupon.times_redeemed} redeemed.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name="expiresAt"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Expiry Date</FormLabel>
                  <DatePicker
                    value={field.value}
                    onChange={field.onChange}
                    placeholder="No expiration"
                  />
                  <FormDescription>Leave empty for no expiration</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <div>
              <Label className="text-sm font-medium">Restrictions</Label>
              <p className="text-xs text-muted-foreground mt-0.5 mb-2">
                Optional — leave empty for no restriction. Clearing all three
                removes the restrictions block from the coupon.
              </p>
              <div className="space-y-3">
                <FormField
                  control={form.control}
                  name="minAmount"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel className="text-xs text-muted-foreground">
                        Minimum purchase{coupon.type === 'fixed_amount' && coupon.currency ? ` (${coupon.currency.toUpperCase()})` : ''}
                      </FormLabel>
                      <FormControl>
                        <Input type="number" min="0" step="0.01" placeholder="e.g. 50.00" {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="maxRedemptionsPerCustomer"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel className="text-xs text-muted-foreground">Max redemptions per customer</FormLabel>
                      <FormControl>
                        <Input type="number" min="1" step="1" placeholder="e.g. 1" {...field} />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="firstTimeCustomerOnly"
                  render={({ field }) => (
                    <label className="flex items-start gap-3 cursor-pointer">
                      <Checkbox
                        checked={field.value}
                        onCheckedChange={(checked) => field.onChange(!!checked)}
                        className="mt-0.5"
                      />
                      <div className="flex-1">
                        <div className="text-sm text-foreground">First-time customers only</div>
                        <p className="text-xs text-muted-foreground mt-0.5">
                          Redeemable only on a customer&apos;s very first invoice.
                        </p>
                      </div>
                    </label>
                  )}
                />
              </div>
            </div>

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={updateMutation.isPending}>
                {updateMutation.isPending ? (
                  <><Loader2 size={14} className="animate-spin mr-2" /> Saving…</>
                ) : (
                  'Save changes'
                )}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  )
}
