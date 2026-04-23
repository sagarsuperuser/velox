import { useState, useMemo } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, formatCents, formatDate, formatDateTime, type Coupon, type CouponRedemption, type Plan } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { ExpiryBadge } from '@/components/ExpiryBadge'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'

import { Loader2, Archive, ArchiveRestore, Lock, Calculator } from 'lucide-react'
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

  const { data: redemptionsData, isLoading: loadingRedemptions } = useQuery({
    queryKey: ['coupon-redemptions', id],
    queryFn: () => api.listCouponRedemptions(id!),
    enabled: !!id,
  })
  const redemptions = redemptionsData?.data ?? []

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
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to archive'),
  })

  const unarchiveMutation = useMutation({
    mutationFn: () => api.unarchiveCoupon(id!),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['coupon', id] })
      queryClient.invalidateQueries({ queryKey: ['coupons'] })
      setUnarchiveOpen(false)
      toast.success('Coupon restored')
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : 'Failed to restore'),
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
          <Badge variant={statusVariant(status)}>{status}</Badge>
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
                  <span className="text-muted-foreground text-base font-normal">Never</span>
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
              <Badge variant={statusVariant(status)}>{status}</Badge>
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
                {redemptions.map((r: CouponRedemption) => (
                  <TableRow key={r.id}>
                    <TableCell>
                      <Link to={`/customers/${r.customer_id}`} className="text-sm font-mono text-primary hover:underline">
                        {r.customer_id}
                      </Link>
                    </TableCell>
                    <TableCell>
                      {r.invoice_id ? (
                        <Link to={`/invoices/${r.invoice_id}`} className="text-sm font-mono text-primary hover:underline">
                          {r.invoice_id}
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
                ))}
              </TableBody>
            </Table>
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
