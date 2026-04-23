import { useState, useMemo, useEffect } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient, useInfiniteQuery } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatCents, formatDate } from '@/lib/api'
import type { Coupon, CouponRedemption } from '@/lib/api'
import { applyApiError } from '@/lib/formErrors'
import { Layout } from '@/components/Layout'
import { ExpiryBadge } from '@/components/ExpiryBadge'
import { CustomerCombobox } from '@/components/CustomerCombobox'
import { cn } from '@/lib/utils'
import { DatePicker } from '@/components/ui/date-picker'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
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
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { TableSkeleton } from '@/components/ui/TableSkeleton'

import { Plus, Archive, ArchiveRestore, Eye, Copy, Search, Loader2, Ticket, Sparkles, Lock } from 'lucide-react'
import { EmptyState } from '@/components/EmptyState'

// Code is optional — the server auto-generates a CPN-XXXX-XXXX code when
// empty, which is the default path for enterprise private coupons.
//
// Duration + stackable + restrictions landed in v2 to match Stripe's
// coupon surface. durationPeriods is only sent when duration === repeating;
// the refine below enforces that together rather than scattering the
// invariant across onSubmit.
const createCouponSchema = z.object({
  code: z.string(),
  name: z.string().min(1, 'Name is required'),
  type: z.enum(['percentage', 'fixed_amount']),
  discountValue: z.string().min(1, 'Discount value is required').refine(v => parseFloat(v) >= 0.01, 'Must be at least 0.01'),
  currency: z.string(),
  maxRedemptions: z.string(),
  expiresAt: z.string(),
  planIds: z.array(z.string()),
  customerId: z.string(),
  duration: z.enum(['once', 'repeating', 'forever']),
  durationPeriods: z.string(),
  stackable: z.boolean(),
  minAmount: z.string(),
  firstTimeCustomerOnly: z.boolean(),
  maxRedemptionsPerCustomer: z.string(),
}).refine(
  (d) => d.duration !== 'repeating' || (d.durationPeriods && parseInt(d.durationPeriods, 10) >= 1),
  { message: 'Duration periods is required for repeating coupons', path: ['durationPeriods'] },
)

type CreateCouponData = z.infer<typeof createCouponSchema>

function couponStatus(c: Coupon): string {
  if (c.archived_at) return 'archived'
  if (c.expires_at && new Date(c.expires_at) < new Date()) return 'expired'
  if (c.max_redemptions !== null && c.times_redeemed >= c.max_redemptions) return 'maxed'
  return 'active'
}

function formatDiscount(c: Coupon): string {
  if (c.type === 'percentage') {
    // percent_off_bp is basis points: 5050 = 50.50%. Strip the trailing
    // ".00" so the common-case integer percent reads cleanly.
    const pct = c.percent_off_bp / 100
    return Number.isInteger(pct) ? `${pct}%` : `${pct.toFixed(2)}%`
  }
  return formatCents(c.amount_off, c.currency)
}

function couponStatusVariant(status: string): 'success' | 'secondary' | 'danger' | 'warning' | 'outline' {
  switch (status) {
    case 'active': return 'success'
    case 'expired': return 'secondary'
    case 'maxed': return 'warning'
    case 'archived': return 'danger'
    default: return 'outline'
  }
}

export default function CouponsPage() {
  const [showCreate, setShowCreate] = useState(false)
  const [archiveId, setArchiveId] = useState<string | null>(null)
  const [unarchiveId, setUnarchiveId] = useState<string | null>(null)
  const [redemptionsCoupon, setRedemptionsCoupon] = useState<Coupon | null>(null)
  // URL-backed filter state so refresh, back-button, and shared links all
  // land on the same view. `replace: true` on every write so typing into
  // the search box doesn't flood browser history.
  const [searchParams, setSearchParams] = useSearchParams()
  const filterStatus = searchParams.get('status') ?? ''
  const search = searchParams.get('q') ?? ''
  const setFilterStatus = (value: string) => {
    setSearchParams(prev => {
      const next = new URLSearchParams(prev)
      if (value) next.set('status', value)
      else next.delete('status')
      return next
    }, { replace: true })
  }
  const setSearch = (value: string) => {
    setSearchParams(prev => {
      const next = new URLSearchParams(prev)
      if (value) next.set('q', value)
      else next.delete('q')
      return next
    }, { replace: true })
  }
  const queryClient = useQueryClient()

  // Include archived rows so the archived tab can show history. Status gates
  // handle filtering on the client so the list stays a single round trip.
  const { data: couponsData, isLoading: loading, error: loadErrorObj, refetch } = useQuery({
    queryKey: ['coupons'],
    queryFn: () => api.listCoupons(true),
  })

  const coupons = couponsData?.data ?? []
  const loadError = loadErrorObj instanceof Error ? loadErrorObj.message : loadErrorObj ? String(loadErrorObj) : null

  const archiveMutation = useMutation({
    mutationFn: (id: string) => api.archiveCoupon(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['coupons'] })
      toast.success('Coupon archived')
      setArchiveId(null)
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : 'Failed to archive')
    },
  })

  const unarchiveMutation = useMutation({
    mutationFn: (id: string) => api.unarchiveCoupon(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['coupons'] })
      toast.success('Coupon restored')
      setUnarchiveId(null)
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : 'Failed to restore')
    },
  })

  const stats = useMemo(() => {
    const active = coupons.filter(c => couponStatus(c) === 'active').length
    const expired = coupons.filter(c => couponStatus(c) === 'expired').length
    const archived = coupons.filter(c => couponStatus(c) === 'archived').length
    return { active, expired, archived }
  }, [coupons])

  const filteredCoupons = useMemo(() => {
    let result = coupons
    if (filterStatus) {
      result = result.filter(c => couponStatus(c) === filterStatus)
    }
    if (search) {
      const q = search.toLowerCase()
      result = result.filter(c =>
        c.code.toLowerCase().includes(q) ||
        (c.name && c.name.toLowerCase().includes(q))
      )
    }
    return result
  }, [coupons, filterStatus, search])

  return (
    <Layout>
      {/* Header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">Coupons</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Create and manage discount codes{coupons.length > 0 ? ` · ${stats.active} active of ${coupons.length} total` : ''}
          </p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          <Plus size={16} className="mr-2" />
          Create Coupon
        </Button>
      </div>

      {/* Tab filters + search */}
      {(coupons.length > 0 || filterStatus) && (
        <div className="flex items-center gap-3 mt-6">
          <div className="flex gap-1 bg-muted rounded-lg p-1">
            {[
              { value: '', label: 'All', count: coupons.length },
              { value: 'active', label: 'Active', count: stats.active },
              { value: 'expired', label: 'Expired', count: stats.expired },
              { value: 'archived', label: 'Archived', count: stats.archived },
            ].map(f => (
              <button
                key={f.value}
                onClick={() => setFilterStatus(f.value)}
                className={cn(
                  'px-3 py-1.5 rounded-md text-xs font-medium transition-colors',
                  filterStatus === f.value
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                )}
              >
                {f.label}
                {f.count > 0 && <span className="ml-1 text-muted-foreground/60">{f.count}</span>}
              </button>
            ))}
          </div>
          <div className="relative flex-1">
            <Search size={16} className="absolute left-3 top-2.5 text-muted-foreground" />
            <Input
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search by code or name..."
              className="pl-9"
            />
          </div>
        </div>
      )}

      {/* Table */}
      <Card className="mt-4">
        <CardContent className="p-0">
          {loadError ? (
            <div className="p-8 text-center">
              <p className="text-sm text-destructive mb-3">{loadError}</p>
              <Button variant="outline" size="sm" onClick={() => refetch()}>
                Retry
              </Button>
            </div>
          ) : loading ? (
            <TableSkeleton columns={8} />
          ) : coupons.length === 0 ? (
            filterStatus ? (
              <EmptyState
                title={`No ${filterStatus} coupons`}
                description="Try a different filter to see more results."
                action={{
                  label: 'Clear filter',
                  variant: 'outline',
                  onClick: () => setFilterStatus(''),
                }}
              />
            ) : (
              <EmptyState
                icon={Ticket}
                title="No coupons yet"
                description="Create your first coupon to offer discounts to customers."
                action={{
                  label: 'Create Coupon',
                  icon: Plus,
                  onClick: () => setShowCreate(true),
                }}
              />
            )
          ) : filteredCoupons.length === 0 ? (
            <p className="px-6 py-8 text-sm text-muted-foreground text-center">
              No coupons match your filters
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-xs font-medium">Code</TableHead>
                  <TableHead className="text-xs font-medium">Name</TableHead>
                  <TableHead className="text-xs font-medium">Type</TableHead>
                  <TableHead className="text-xs font-medium text-right">Discount</TableHead>
                  <TableHead className="text-xs font-medium">Redemptions</TableHead>
                  <TableHead className="text-xs font-medium">Expires</TableHead>
                  <TableHead className="text-xs font-medium">Status</TableHead>
                  <TableHead className="text-xs font-medium text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filteredCoupons.map(c => {
                  const status = couponStatus(c)
                  return (
                    <TableRow key={c.id}>
                      <TableCell>
                        <div className="flex items-center gap-1.5">
                          <Link
                            to={`/coupons/${c.id}`}
                            className="text-sm font-mono font-medium text-foreground hover:text-primary hover:underline truncate max-w-[140px]"
                            title={c.code}
                          >
                            {c.code}
                          </Link>
                          {c.customer_id && (
                            <Lock
                              size={12}
                              className="text-muted-foreground shrink-0"
                              aria-label="Private coupon"
                            />
                          )}
                          <button
                            onClick={(e) => { e.stopPropagation(); navigator.clipboard.writeText(c.code); toast.success('Code copied') }}
                            className="p-1 rounded text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
                            title="Copy code"
                          >
                            <Copy size={13} />
                          </button>
                        </div>
                      </TableCell>
                      <TableCell>
                        <Link
                          to={`/coupons/${c.id}`}
                          className="text-sm text-muted-foreground hover:text-foreground truncate max-w-[160px] block"
                          title={c.name || ''}
                        >
                          {c.name || '\u2014'}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <Badge variant="info">{c.type === 'percentage' ? 'Percentage' : 'Fixed'}</Badge>
                      </TableCell>
                      <TableCell className="text-right tabular-nums font-mono text-sm">
                        {formatDiscount(c)}
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {c.times_redeemed}{c.max_redemptions !== null ? ` / ${c.max_redemptions}` : ''}
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {c.expires_at ? (
                          <div className="flex items-center gap-2">
                            <span>{formatDate(c.expires_at)}</span>
                            <ExpiryBadge expiresAt={c.expires_at} warningDays={7} />
                          </div>
                        ) : (
                          'Never'
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant={couponStatusVariant(status)}>{status}</Badge>
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setRedemptionsCoupon(c)}
                            title="View redemptions"
                          >
                            <Eye size={16} />
                          </Button>
                          {status === 'archived' ? (
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => setUnarchiveId(c.id)}
                              title="Restore"
                            >
                              <ArchiveRestore size={16} />
                            </Button>
                          ) : (
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => setArchiveId(c.id)}
                              title="Archive"
                              className="text-destructive hover:text-destructive"
                            >
                              <Archive size={16} />
                            </Button>
                          )}
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* Create Coupon Dialog */}
      <CreateCouponDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        onCreated={() => {
          setShowCreate(false)
          queryClient.invalidateQueries({ queryKey: ['coupons'] })
          toast.success('Coupon created')
        }}
      />

      {/* Archive Confirm */}
      <AlertDialog open={!!archiveId} onOpenChange={(open) => { if (!open) setArchiveId(null) }}>
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
              onClick={() => { if (archiveId) archiveMutation.mutate(archiveId) }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Archive
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Restore Confirm — symmetric with Archive so operators can't
          restore by accident. If the coupon has an expires_at in the
          past or is at max_redemptions the service will still enforce
          those gates, but the confirmation surfaces that before the
          round trip. */}
      <AlertDialog open={!!unarchiveId} onOpenChange={(open) => { if (!open) setUnarchiveId(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Restore Coupon</AlertDialogTitle>
            <AlertDialogDescription>
              This coupon will start accepting new redemptions again. If it has an
              expiry date in the past or has reached its max redemptions, restoring
              it won't make it usable — you'd need to extend or raise those first.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => { if (unarchiveId) unarchiveMutation.mutate(unarchiveId) }}
            >
              Restore
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Redemptions Dialog */}
      {redemptionsCoupon && (
        <RedemptionsDialog
          coupon={redemptionsCoupon}
          open={!!redemptionsCoupon}
          onOpenChange={(open) => { if (!open) setRedemptionsCoupon(null) }}
        />
      )}
    </Layout>
  )
}

// --- Create Coupon Dialog ---

function CreateCouponDialog({ open, onOpenChange, onCreated }: {
  open: boolean; onOpenChange: (open: boolean) => void; onCreated: () => void
}) {
  const [plans, setPlans] = useState<{ id: string; name: string; code: string }[]>([])
  const [isPrivate, setIsPrivate] = useState(false)

  const form = useForm<CreateCouponData>({
    resolver: zodResolver(createCouponSchema),
    defaultValues: {
      code: '', name: '', type: 'percentage', discountValue: '',
      currency: 'USD', maxRedemptions: '', expiresAt: '',
      planIds: [], customerId: '',
      duration: 'once', durationPeriods: '', stackable: false,
      minAmount: '', firstTimeCustomerOnly: false, maxRedemptionsPerCustomer: '',
    },
  })

  useEffect(() => {
    if (open) {
      api.listPlans().then(res => setPlans(res.data || [])).catch(() => {})
    }
  }, [open])

  const type = form.watch('type')
  const planIds = form.watch('planIds')
  const duration = form.watch('duration')

  const createMutation = useMutation({
    mutationFn: async (data: CreateCouponData) => {
      // Only include restrictions when at least one sub-field is set. An
      // all-defaults restrictions object would still round-trip through
      // the API harmlessly, but keeping the payload minimal makes server
      // logs cleaner and prevents confusion when diffing coupons.
      const restrictions: NonNullable<Parameters<typeof api.createCoupon>[0]['restrictions']> = {}
      if (data.minAmount) {
        restrictions.min_amount_cents = Math.round(parseFloat(data.minAmount) * 100)
      }
      if (data.firstTimeCustomerOnly) {
        restrictions.first_time_customer_only = true
      }
      if (data.maxRedemptionsPerCustomer) {
        restrictions.max_redemptions_per_customer = parseInt(data.maxRedemptionsPerCustomer, 10)
      }

      const payload: Parameters<typeof api.createCoupon>[0] = {
        code: data.code.trim(),
        name: data.name,
        type: data.type,
        ...(data.type === 'percentage'
          ? { percent_off_bp: Math.round(parseFloat(data.discountValue) * 100) }
          : { amount_off: Math.round(parseFloat(data.discountValue) * 100), currency: data.currency }),
        ...(data.maxRedemptions ? { max_redemptions: parseInt(data.maxRedemptions, 10) } : {}),
        ...(data.expiresAt ? { expires_at: new Date(data.expiresAt).toISOString() } : {}),
        ...(data.planIds.length > 0 ? { plan_ids: data.planIds } : {}),
        ...(isPrivate && data.customerId ? { customer_id: data.customerId } : {}),
        duration: data.duration,
        ...(data.duration === 'repeating' && data.durationPeriods
          ? { duration_periods: parseInt(data.durationPeriods, 10) }
          : {}),
        ...(data.stackable ? { stackable: true } : {}),
        ...(Object.keys(restrictions).length > 0 ? { restrictions } : {}),
      }
      return api.createCoupon(payload)
    },
    onSuccess: () => {
      form.reset()
      setIsPrivate(false)
      onCreated()
    },
    onError: (err) => {
      // Mirrors internal/coupon/service.go Create() validation paths.
      // Fields without a backend validation site (plan_ids, customer_id,
      // stackable, restrictions.first_time_customer_only, metadata) are
      // omitted — applyApiError falls through to a toast, which is the
      // honest UX when the server can't produce an inline error anyway.
      applyApiError(form, err, {
        code: 'code',
        name: 'name',
        type: 'type',
        percent_off_bp: 'discountValue',
        amount_off: 'discountValue',
        currency: 'currency',
        max_redemptions: 'maxRedemptions',
        expires_at: 'expiresAt',
        duration: 'duration',
        duration_periods: 'durationPeriods',
        'restrictions.min_amount_cents': 'minAmount',
        'restrictions.max_redemptions_per_customer': 'maxRedemptionsPerCustomer',
      })
    },
  })

  const onSubmit = form.handleSubmit((data) => {
    createMutation.mutate(data)
  })

  return (
    <Dialog open={open} onOpenChange={(o) => {
      onOpenChange(o)
      if (!o) { form.reset() }
    }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Create Coupon</DialogTitle>
          <DialogDescription>
            Create a new discount coupon for customers.
          </DialogDescription>
        </DialogHeader>
        <Form {...form}>
          <form onSubmit={onSubmit} noValidate className="space-y-4">
            <FormField
              control={form.control}
              name="code"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Code <span className="text-muted-foreground font-normal">(optional)</span></FormLabel>
                  <FormControl>
                    <div className="flex gap-2">
                      <Input
                        placeholder="Leave blank to auto-generate"
                        {...field}
                        onChange={e => field.onChange(e.target.value.toUpperCase())}
                      />
                      {field.value && (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={() => field.onChange('')}
                          title="Clear — auto-generate a random code on create"
                        >
                          <Sparkles size={14} />
                        </Button>
                      )}
                    </div>
                  </FormControl>
                  <FormDescription>Leave blank and we&apos;ll generate an unguessable code (recommended for private coupons)</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
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
              name="type"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Discount Type</FormLabel>
                  <FormControl>
                    <select
                      value={field.value}
                      onChange={field.onChange}
                      className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                    >
                      <option value="percentage">Percentage (%)</option>
                      <option value="fixed_amount">Fixed Amount</option>
                    </select>
                  </FormControl>
                </FormItem>
              )}
            />

            <div className="flex gap-3">
              <div className="flex-1">
                <FormField
                  control={form.control}
                  name="discountValue"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{type === 'percentage' ? 'Percent Off (%)' : 'Amount Off'}</FormLabel>
                      <FormControl>
                        <Input
                          type="number"
                          step="0.01"
                          min="0.01"
                          max={type === 'percentage' ? '100' : '999999.99'}
                          placeholder={type === 'percentage' ? '20' : '10.00'}
                          {...field}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>
              {type === 'fixed_amount' && (
                <div className="w-28">
                  <FormField
                    control={form.control}
                    name="currency"
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>Currency</FormLabel>
                        <FormControl>
                          <Input
                            placeholder="USD"
                            {...field}
                            onChange={e => field.onChange(e.target.value.toUpperCase())}
                          />
                        </FormControl>
                      </FormItem>
                    )}
                  />
                </div>
              )}
            </div>

            <FormField
              control={form.control}
              name="maxRedemptions"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Max Redemptions</FormLabel>
                  <FormControl>
                    <Input type="number" min="1" step="1" placeholder="Unlimited" {...field} />
                  </FormControl>
                  <FormDescription>Leave empty for unlimited</FormDescription>
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
                </FormItem>
              )}
            />

            {/* Duration — controls how many billing cycles the discount
                applies for. Defaults to "once" so the simplest coupon
                (launch discount on first invoice only) requires zero
                extra clicks. "repeating" reveals the periods input. */}
            <div className="flex gap-3">
              <div className="flex-1">
                <FormField
                  control={form.control}
                  name="duration"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Duration</FormLabel>
                      <FormControl>
                        <select
                          value={field.value}
                          onChange={field.onChange}
                          className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                        >
                          <option value="once">Once — applied to one invoice</option>
                          <option value="repeating">Repeating — applied for N billing cycles</option>
                          <option value="forever">Forever — applied to every invoice</option>
                        </select>
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>
              {duration === 'repeating' && (
                <div className="w-32">
                  <FormField
                    control={form.control}
                    name="durationPeriods"
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>Periods</FormLabel>
                        <FormControl>
                          <Input type="number" min="1" step="1" placeholder="3" {...field} />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </div>
              )}
            </div>

            {/* Stackable — opt-in. The default (false) matches Stripe: a
                single coupon per invoice unless explicitly marked
                stackable. Flipping this on for an existing code is a
                write-lock decision, not a UI toggle, so it lives with
                the create form rather than inline on the row. */}
            <div className="border border-border rounded-lg p-3 bg-muted/30">
              <label className="flex items-start gap-3 cursor-pointer">
                <FormField
                  control={form.control}
                  name="stackable"
                  render={({ field }) => (
                    <Checkbox
                      checked={field.value}
                      onCheckedChange={(checked) => field.onChange(!!checked)}
                      className="mt-0.5"
                    />
                  )}
                />
                <div className="flex-1">
                  <div className="text-sm font-medium text-foreground">Stackable</div>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Allow this coupon to combine with other redemptions on the same invoice.
                    By default only one coupon applies per invoice.
                  </p>
                </div>
              </label>
            </div>

            {/* Restrictions — all optional. Grouped behind a simple
                section header rather than a collapse because the fields
                are short and operators need them visible to compare
                against policy. */}
            <div>
              <Label className="text-sm font-medium">Restrictions</Label>
              <p className="text-xs text-muted-foreground mt-0.5 mb-2">
                Optional — leave empty for no restriction.
              </p>
              <div className="space-y-3">
                <FormField
                  control={form.control}
                  name="minAmount"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel className="text-xs text-muted-foreground">Minimum purchase</FormLabel>
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

            {plans.length > 0 && (
              <div>
                <Label className="text-sm font-medium">Restrict to Plans</Label>
                <div className="border border-border rounded-lg divide-y divide-border max-h-40 overflow-y-auto mt-2">
                  {plans.map(p => (
                    <label key={p.id} className="flex items-center gap-3 px-3 py-2 text-sm cursor-pointer hover:bg-muted transition-colors">
                      <Checkbox
                        checked={planIds.includes(p.id)}
                        onCheckedChange={(checked) => {
                          form.setValue('planIds',
                            checked ? [...planIds, p.id] : planIds.filter(id => id !== p.id),
                            { shouldDirty: true }
                          )
                        }}
                      />
                      <span className="text-foreground">{p.name}</span>
                      <span className="text-muted-foreground font-mono text-xs ml-auto">{p.code}</span>
                    </label>
                  ))}
                </div>
                <p className="text-xs text-muted-foreground mt-1">Leave all unchecked for no restriction</p>
              </div>
            )}

            {/* Private coupon — enterprise-negotiated single-customer discounts */}
            <div className="border border-border rounded-lg p-3 bg-muted/30">
              <label className="flex items-start gap-3 cursor-pointer">
                <Checkbox
                  checked={isPrivate}
                  onCheckedChange={(checked) => {
                    setIsPrivate(!!checked)
                    if (!checked) form.setValue('customerId', '')
                  }}
                  className="mt-0.5"
                />
                <div className="flex-1">
                  <div className="flex items-center gap-1.5 text-sm font-medium text-foreground">
                    <Lock size={13} />
                    Private coupon
                  </div>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Restrict to a single customer. Only that customer can redeem this code.
                  </p>
                </div>
              </label>

              {isPrivate && (
                <FormField
                  control={form.control}
                  name="customerId"
                  render={({ field }) => (
                    <FormItem className="mt-3 ml-7">
                      <FormLabel>Customer</FormLabel>
                      <FormControl>
                        <CustomerCombobox
                          value={field.value}
                          onChange={field.onChange}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}
            </div>

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={createMutation.isPending}>
                {createMutation.isPending ? (
                  <>
                    <Loader2 size={14} className="animate-spin mr-2" />
                    Creating...
                  </>
                ) : (
                  'Create Coupon'
                )}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  )
}

// --- Redemptions Dialog ---

function RedemptionsDialog({ coupon, open, onOpenChange }: {
  coupon: Coupon; open: boolean; onOpenChange: (open: boolean) => void
}) {
  // Seek-cursor pagination: useInfiniteQuery accumulates pages, API
  // returns has_more/next_cursor. 25 per page keeps the dialog compact.
  const {
    data: redemptionsPages,
    isLoading: loading,
    isFetchingNextPage,
    fetchNextPage,
    hasNextPage,
  } = useInfiniteQuery({
    queryKey: ['coupon-redemptions', coupon.id],
    queryFn: ({ pageParam }) => {
      const params = new URLSearchParams({ limit: '25' })
      if (pageParam) params.set('cursor', pageParam)
      return api.listCouponRedemptions(coupon.id, params.toString())
    },
    initialPageParam: '' as string,
    getNextPageParam: (last) => (last.has_more ? last.next_cursor : undefined),
    enabled: open,
  })
  const redemptions = useMemo(
    () => (redemptionsPages?.pages ?? []).flatMap(p => p.data ?? []),
    [redemptionsPages],
  )

  // Join the redemption's customer_id against the customer list so
  // operators see "Acme Corp" instead of a raw cus_... UUID. Dialog's
  // query only runs when open, so no wasted request on closed rows.
  const { data: customersData } = useQuery({
    queryKey: ['customers'],
    queryFn: () => api.listCustomers(),
    staleTime: 30_000,
    enabled: open,
  })
  const customerById = useMemo(() => {
    const list = customersData?.data ?? []
    const map = new Map<string, { name: string; email: string }>()
    for (const c of list) {
      map.set(c.id, { name: c.display_name || c.external_id || c.id, email: c.email ?? '' })
    }
    return map
  }, [customersData])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Redemptions — {coupon.code}</DialogTitle>
        </DialogHeader>

        <div className="flex items-center gap-3 mb-3">
          <Badge variant="outline">{coupon.type === 'percentage' ? 'Percentage' : 'Fixed'}</Badge>
          <span className="text-sm text-foreground font-medium">{formatDiscount(coupon)}</span>
          <span className="text-sm text-muted-foreground">{coupon.times_redeemed} redemption{coupon.times_redeemed !== 1 ? 's' : ''}</span>
        </div>

        {loading ? (
          <div className="py-8 flex justify-center">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
          </div>
        ) : redemptions.length === 0 ? (
          <div className="py-8 text-center">
            <p className="text-sm font-medium text-foreground">No redemptions yet</p>
            <p className="text-sm text-muted-foreground mt-1">This coupon has not been used</p>
          </div>
        ) : (
          <div className="max-h-96 overflow-y-auto">
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
                        <Link to={`/customers/${r.customer_id}`} className="text-sm text-primary hover:underline">
                          {cust?.name ?? r.customer_id}
                        </Link>
                        {cust?.email && (
                          <div className="text-xs text-muted-foreground">{cust.email}</div>
                        )}
                      </TableCell>
                      <TableCell>
                        {r.invoice_id ? (
                          <Link to={`/invoices/${r.invoice_id}`} className="text-sm font-mono text-primary hover:underline">
                            {r.invoice_id.slice(0, 12)}…
                          </Link>
                        ) : (
                          <span className="text-xs text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right tabular-nums font-mono text-sm text-emerald-600">{formatCents(r.discount_cents)}</TableCell>
                      <TableCell className="text-sm text-muted-foreground">{formatDate(r.created_at)}</TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
            {hasNextPage && (
              <div className="border-t border-border py-3 flex justify-center">
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
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
