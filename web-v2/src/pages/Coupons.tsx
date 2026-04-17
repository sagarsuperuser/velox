import { useState, useMemo, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatCents, formatDate } from '@/lib/api'
import type { Coupon, CouponRedemption } from '@/lib/api'
import { Layout } from '@/components/Layout'
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

import { Plus, Power, Eye, Copy, Search, Loader2 } from 'lucide-react'

const createCouponSchema = z.object({
  code: z.string().min(1, 'Code is required'),
  name: z.string().min(1, 'Name is required'),
  type: z.enum(['percentage', 'fixed_amount']),
  discountValue: z.string().min(1, 'Discount value is required').refine(v => parseFloat(v) >= 0.01, 'Must be at least 0.01'),
  currency: z.string(),
  maxRedemptions: z.string(),
  expiresAt: z.string(),
  planIds: z.array(z.string()),
})

type CreateCouponData = z.infer<typeof createCouponSchema>

function couponStatus(c: Coupon): string {
  if (!c.active) return 'inactive'
  if (c.expires_at && new Date(c.expires_at) < new Date()) return 'expired'
  if (c.max_redemptions !== null && c.times_redeemed >= c.max_redemptions) return 'maxed'
  return 'active'
}

function formatDiscount(c: Coupon): string {
  if (c.type === 'percentage') return `${c.percent_off}%`
  return formatCents(c.amount_off, c.currency)
}

function couponStatusVariant(status: string): 'success' | 'secondary' | 'danger' | 'warning' | 'outline' {
  switch (status) {
    case 'active': return 'success'
    case 'expired': return 'secondary'
    case 'maxed': return 'warning'
    case 'inactive': return 'danger'
    default: return 'outline'
  }
}

export default function CouponsPage() {
  const [showCreate, setShowCreate] = useState(false)
  const [deactivateId, setDeactivateId] = useState<string | null>(null)
  const [redemptionsCoupon, setRedemptionsCoupon] = useState<Coupon | null>(null)
  const [filterStatus, setFilterStatus] = useState('')
  const [search, setSearch] = useState('')
  const [error, setError] = useState('')
  const queryClient = useQueryClient()

  const { data: couponsData, isLoading: loading, error: loadErrorObj, refetch } = useQuery({
    queryKey: ['coupons'],
    queryFn: () => api.listCoupons(),
  })

  const coupons = couponsData?.data ?? []
  const loadError = loadErrorObj instanceof Error ? loadErrorObj.message : loadErrorObj ? String(loadErrorObj) : null

  const deactivateMutation = useMutation({
    mutationFn: (id: string) => api.deactivateCoupon(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['coupons'] })
      toast.success('Coupon deactivated')
      setDeactivateId(null)
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : 'Failed to deactivate')
    },
  })

  const stats = useMemo(() => {
    const active = coupons.filter(c => couponStatus(c) === 'active').length
    const expired = coupons.filter(c => couponStatus(c) === 'expired').length
    const inactive = coupons.filter(c => couponStatus(c) === 'inactive').length
    return { active, expired, inactive }
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
        {coupons.length > 0 && (
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus size={16} className="mr-2" />
            Create Coupon
          </Button>
        )}
      </div>

      {/* Tab filters + search */}
      {(coupons.length > 0 || filterStatus) && (
        <div className="flex items-center gap-3 mt-6">
          <div className="flex gap-1 bg-muted rounded-lg p-1">
            {[
              { value: '', label: 'All', count: coupons.length },
              { value: 'active', label: 'Active', count: stats.active },
              { value: 'expired', label: 'Expired', count: stats.expired },
              { value: 'inactive', label: 'Inactive', count: stats.inactive },
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
            <div className="p-8 flex justify-center">
              <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
            </div>
          ) : coupons.length === 0 ? (
            <div className="p-12 text-center">
              {filterStatus ? (
                <>
                  <p className="text-sm font-medium text-foreground">No {filterStatus} coupons</p>
                  <p className="text-sm text-muted-foreground mt-1">Try a different filter</p>
                  <Button variant="outline" size="sm" className="mt-4" onClick={() => setFilterStatus('')}>
                    Clear filter
                  </Button>
                </>
              ) : (
                <>
                  <p className="text-sm font-medium text-foreground">No coupons</p>
                  <p className="text-sm text-muted-foreground mt-1">Create your first coupon to offer discounts</p>
                  <Button size="sm" className="mt-4" onClick={() => setShowCreate(true)}>
                    <Plus size={16} className="mr-2" />
                    Create Coupon
                  </Button>
                </>
              )}
            </div>
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
                          <span className="text-sm font-mono font-medium text-foreground truncate max-w-[140px]" title={c.code}>{c.code}</span>
                          <button
                            onClick={() => { navigator.clipboard.writeText(c.code); toast.success('Code copied') }}
                            className="p-1 rounded text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
                            title="Copy code"
                          >
                            <Copy size={13} />
                          </button>
                        </div>
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground truncate max-w-[160px]" title={c.name || ''}>{c.name || '\u2014'}</TableCell>
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
                        {c.expires_at ? formatDate(c.expires_at) : 'Never'}
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
                          {status === 'active' && (
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => setDeactivateId(c.id)}
                              title="Deactivate"
                              className="text-destructive hover:text-destructive"
                            >
                              <Power size={16} />
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
        onOpenChange={(open) => { setShowCreate(open); if (!open) setError('') }}
        onCreated={() => {
          setShowCreate(false)
          queryClient.invalidateQueries({ queryKey: ['coupons'] })
          toast.success('Coupon created')
        }}
      />

      {/* Deactivate Confirm */}
      <AlertDialog open={!!deactivateId} onOpenChange={(open) => { if (!open) setDeactivateId(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Deactivate Coupon</AlertDialogTitle>
            <AlertDialogDescription>
              This coupon will no longer be redeemable. Existing redemptions are not affected.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => { if (deactivateId) deactivateMutation.mutate(deactivateId) }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Deactivate
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
  const [error, setError] = useState('')

  const form = useForm<CreateCouponData>({
    resolver: zodResolver(createCouponSchema),
    defaultValues: { code: '', name: '', type: 'percentage', discountValue: '', currency: 'USD', maxRedemptions: '', expiresAt: '', planIds: [] },
  })

  useEffect(() => {
    if (open) {
      api.listPlans().then(res => setPlans(res.data || [])).catch(() => {})
    }
  }, [open])

  const type = form.watch('type')
  const planIds = form.watch('planIds')

  const createMutation = useMutation({
    mutationFn: async (data: CreateCouponData) => {
      const payload: Parameters<typeof api.createCoupon>[0] = {
        code: data.code,
        name: data.name,
        type: data.type,
        ...(data.type === 'percentage'
          ? { percent_off: parseFloat(data.discountValue) }
          : { amount_off: Math.round(parseFloat(data.discountValue) * 100), currency: data.currency }),
        ...(data.maxRedemptions ? { max_redemptions: parseInt(data.maxRedemptions, 10) } : {}),
        ...(data.expiresAt ? { expires_at: new Date(data.expiresAt).toISOString() } : {}),
        ...(data.planIds.length > 0 ? { plan_ids: data.planIds } : {}),
      }
      return api.createCoupon(payload)
    },
    onSuccess: () => {
      form.reset()
      onCreated()
    },
    onError: (err) => {
      setError(err instanceof Error ? err.message : 'Failed to create coupon')
    },
  })

  const onSubmit = form.handleSubmit((data) => {
    setError('')
    createMutation.mutate(data)
  })

  return (
    <Dialog open={open} onOpenChange={(o) => {
      onOpenChange(o)
      if (!o) { form.reset(); setError('') }
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
                  <FormLabel>Code</FormLabel>
                  <FormControl>
                    <Input
                      placeholder="LAUNCH20"
                      {...field}
                      onChange={e => field.onChange(e.target.value.toUpperCase())}
                    />
                  </FormControl>
                  <FormDescription>3-50 characters, alphanumeric and dashes</FormDescription>
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

            {error && (
              <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
                <p className="text-destructive text-sm">{error}</p>
              </div>
            )}

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
  const { data: redemptionsData, isLoading: loading } = useQuery({
    queryKey: ['coupon-redemptions', coupon.id],
    queryFn: () => api.listCouponRedemptions(coupon.id),
    enabled: open,
  })

  const redemptions = redemptionsData?.data ?? []

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Redemptions - {coupon.code}</DialogTitle>
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
          <div className="max-h-80 overflow-y-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-xs font-medium">Customer</TableHead>
                  <TableHead className="text-xs font-medium text-right">Discount</TableHead>
                  <TableHead className="text-xs font-medium">Date</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {redemptions.map((r: CouponRedemption) => (
                  <TableRow key={r.id}>
                    <TableCell className="text-sm text-foreground font-mono">{r.customer_id.slice(0, 20)}...</TableCell>
                    <TableCell className="text-right tabular-nums font-mono text-sm text-emerald-600">{formatCents(r.discount_cents)}</TableCell>
                    <TableCell className="text-sm text-muted-foreground">{formatDate(r.created_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
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
