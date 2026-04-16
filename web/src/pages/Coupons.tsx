import { useEffect, useState, useMemo } from 'react'
import { useForm, Controller } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { api, formatCents, formatDate, formatDateTime, type Coupon, type CouponRedemption } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { toast } from 'sonner'
import { Plus, Power, Eye, Copy, Search, Loader2 } from 'lucide-react'
import { DatePicker } from '@/components/DatePicker'
import { Breadcrumbs } from '@/components/Breadcrumbs'

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

export function CouponsPage() {
  const [coupons, setCoupons] = useState<Coupon[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [deactivateId, setDeactivateId] = useState<string | null>(null)
  const [redemptionsCoupon, setRedemptionsCoupon] = useState<Coupon | null>(null)
  const [filterStatus, setFilterStatus] = useState('')
  const [search, setSearch] = useState('')


  const loadData = () => {
    setLoading(true)
    setError(null)
    api.listCoupons().then(res => {
      setCoupons(res.data || [])
      setLoading(false)
    }).catch(err => {
      setError(err instanceof Error ? err.message : 'Failed to load coupons')
      setLoading(false)
    })
  }

  useEffect(() => { loadData() }, [])

  const handleDeactivate = async () => {
    if (!deactivateId) return
    try {
      await api.deactivateCoupon(deactivateId)
      toast.success('Coupon deactivated')
      setDeactivateId(null)
      loadData()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to deactivate')
    }
  }

  const stats = useMemo(() => {
    const active = coupons.filter(c => couponStatus(c) === 'active').length
    const expired = coupons.filter(c => couponStatus(c) === 'expired').length
    const maxed = coupons.filter(c => couponStatus(c) === 'maxed').length
    const inactive = coupons.filter(c => couponStatus(c) === 'inactive').length
    const totalRedemptions = coupons.reduce((sum, c) => sum + c.times_redeemed, 0)
    return { active, expired, maxed, inactive, totalRedemptions }
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
      <Breadcrumbs items={[{ label: 'Configuration' }, { label: 'Coupons' }]} />
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Coupons</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">
            {coupons.length > 0
              ? `${stats.active} active coupon${stats.active !== 1 ? 's' : ''} of ${coupons.length} total`
              : 'Create discount codes for your customers'}
          </p>
        </div>
        {coupons.length > 0 && (
          <button onClick={() => setShowCreate(true)}
            className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
            <Plus size={16} /> Create Coupon
          </button>
        )}
      </div>

      {/* Tab filters + search */}
      {coupons.length > 0 && (
        <div className="flex items-center gap-3 mt-6">
          <div className="flex gap-1 bg-gray-100 dark:bg-gray-800 rounded-lg p-1">
            {[
              { value: '', label: 'All', count: coupons.length },
              { value: 'active', label: 'Active', count: stats.active },
              { value: 'expired', label: 'Expired', count: stats.expired },
              { value: 'inactive', label: 'Inactive', count: stats.inactive },
            ].map(f => (
              <button key={f.value} onClick={() => setFilterStatus(f.value)}
                className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${
                  filterStatus === f.value ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm' : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
                }`}>
                {f.label}
                {f.count > 0 && <span className="ml-1 text-gray-400">{f.count}</span>}
              </button>
            ))}
          </div>
          <div className="relative flex-1">
            <Search size={16} className="absolute left-3 top-2.5 text-gray-400" />
            <input
              type="text"
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search by code or name..."
              className="w-full pl-9 pr-4 py-2 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white dark:bg-gray-800 dark:text-gray-100"
            />
          </div>
        </div>
      )}

      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        {error ? (
          <ErrorState message={error} onRetry={loadData} />
        ) : loading ? (
          <LoadingSkeleton rows={6} columns={7} />
        ) : coupons.length === 0 ? (
          <EmptyState title="No coupons" description="Create your first coupon to offer discounts" actionLabel="Create Coupon" onAction={() => setShowCreate(true)} />
        ) : filteredCoupons.length === 0 ? (
          <p className="px-6 py-8 text-sm text-gray-400 text-center">No coupons match your filters</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Code</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Name</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Type</th>
                  <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Discount</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Redemptions</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Expires</th>
                  <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Status</th>
                  <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {filteredCoupons.map(c => {
                  const status = couponStatus(c)
                  return (
                    <tr key={c.id} className="hover:bg-gray-50/50 dark:hover:bg-gray-800/50 transition-colors">
                      <td className="px-6 py-3">
                        <div className="flex items-center gap-1.5">
                          <span className="text-sm font-mono font-medium text-gray-900">{c.code}</span>
                          <button onClick={() => { navigator.clipboard.writeText(c.code); toast.success('Code copied') }}
                            className="p-1 rounded text-gray-300 hover:text-gray-500 hover:bg-gray-100 transition-colors" title="Copy code">
                            <Copy size={13} />
                          </button>
                        </div>
                      </td>
                      <td className="px-6 py-3 text-sm text-gray-700 dark:text-gray-300">{c.name || '---'}</td>
                      <td className="px-6 py-3"><Badge status={c.type} /></td>
                      <td className="px-6 py-3 text-sm font-medium text-gray-900 dark:text-gray-100 text-right tabular-nums">{formatDiscount(c)}</td>
                      <td className="px-6 py-3 text-sm text-gray-700 dark:text-gray-300">
                        {c.times_redeemed}{c.max_redemptions !== null ? ` / ${c.max_redemptions}` : ''}
                      </td>
                      <td className="px-6 py-3 text-sm text-gray-600 dark:text-gray-400">
                        {c.expires_at ? formatDate(c.expires_at) : 'Never'}
                      </td>
                      <td className="px-6 py-3"><Badge status={status} /></td>
                      <td className="px-6 py-3 text-right">
                        <div className="flex items-center justify-end gap-1">
                          <button onClick={() => setRedemptionsCoupon(c)}
                            className="p-1.5 rounded-lg text-gray-400 hover:text-velox-600 hover:bg-gray-100 transition-colors"
                            title="View redemptions">
                            <Eye size={16} />
                          </button>
                          {status === 'active' && (
                            <button onClick={() => setDeactivateId(c.id)}
                              className="p-1.5 rounded-lg text-gray-400 hover:text-red-600 hover:bg-red-50 transition-colors"
                              title="Deactivate">
                              <Power size={16} />
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {showCreate && (
        <CreateCouponModal
          onClose={() => setShowCreate(false)}
          onDone={() => { setShowCreate(false); loadData(); toast.success('Coupon created') }}
        />
      )}

      <ConfirmDialog
        open={!!deactivateId}
        title="Deactivate Coupon"
        message="This coupon will no longer be redeemable. Existing redemptions are not affected."
        confirmLabel="Deactivate"
        variant="danger"
        onConfirm={handleDeactivate}
        onCancel={() => setDeactivateId(null)}
      />

      {redemptionsCoupon && (
        <RedemptionsModal
          coupon={redemptionsCoupon}
          onClose={() => setRedemptionsCoupon(null)}
        />
      )}
    </Layout>
  )
}

// --- Create Coupon Modal ---

function CreateCouponModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [plans, setPlans] = useState<{ id: string; name: string; code: string }[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    api.listPlans().then(res => setPlans(res.data || [])).catch(() => {})
  }, [])

  const { register, handleSubmit, watch, setValue, control, formState: { errors, isSubmitting, isDirty } } = useForm<CreateCouponData>({
    resolver: zodResolver(createCouponSchema),
    defaultValues: { code: '', name: '', type: 'percentage', discountValue: '', currency: 'USD', maxRedemptions: '', expiresAt: '', planIds: [] },
  })

  const type = watch('type')
  const planIds = watch('planIds')
  const expiresAt = watch('expiresAt')

  const onSubmit = handleSubmit(async (data) => {
    setError('')
    try {
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
      await api.createCoupon(payload)
      onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create coupon')
    }
  })

  return (
    <Modal open onClose={onClose} title="Create Coupon" dirty={isDirty}>
      <form onSubmit={onSubmit} noValidate className="space-y-4">
        <Controller
          name="code"
          control={control}
          render={({ field }) => (
            <FormField label="Code" required placeholder="LAUNCH20"
              error={errors.code?.message}
              hint="3-50 characters, alphanumeric and dashes"
              name={field.name} ref={field.ref} onBlur={field.onBlur}
              value={field.value}
              onChange={e => field.onChange(e.target.value.toUpperCase())} />
          )}
        />

        <FormField label="Name" required placeholder="Launch Discount"
          error={errors.name?.message}
          {...register('name')} />

        <FormSelect label="Discount Type" required
          value={type}
          onChange={e => setValue('type', e.target.value as 'percentage' | 'fixed_amount')}
          options={[
            { value: 'percentage', label: 'Percentage (%)' },
            { value: 'fixed_amount', label: 'Fixed Amount' },
          ]} />

        <div className="flex gap-3">
          <div className="flex-1">
            <FormField
              label={type === 'percentage' ? 'Percent Off (%)' : 'Amount Off'}
              required type="number" step={type === 'percentage' ? '0.01' : '0.01'}
              min="0.01" max={type === 'percentage' ? '100' : '999999.99'}
              placeholder={type === 'percentage' ? '20' : '10.00'}
              error={errors.discountValue?.message}
              {...register('discountValue')} />
          </div>
          {type === 'fixed_amount' && (
            <div className="w-28">
              <Controller
                name="currency"
                control={control}
                render={({ field }) => (
                  <FormField label="Currency" required
                    placeholder="USD"
                    name={field.name} ref={field.ref} onBlur={field.onBlur}
                    value={field.value}
                    onChange={e => field.onChange(e.target.value.toUpperCase())} />
                )}
              />
            </div>
          )}
        </div>

        <FormField label="Max Redemptions" type="number" min="1" step="1"
          placeholder="Unlimited"
          hint="Leave empty for unlimited"
          {...register('maxRedemptions')} />

        <DatePicker
          value={expiresAt ?? ''}
          onChange={v => setValue('expiresAt', v, { shouldDirty: true })}
          label="Expiry Date"
          includeTime
          hint="Leave empty for no expiration" />

        {plans.length > 0 && (
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-2">Restrict to Plans</label>
            <div className="border border-gray-200 dark:border-gray-700 rounded-lg divide-y divide-gray-100 dark:divide-gray-800 max-h-40 overflow-y-auto">
              {plans.map(p => (
                <label key={p.id} className="flex items-center gap-3 px-3 py-2 text-sm cursor-pointer hover:bg-gray-50 transition-colors">
                  <input type="checkbox" checked={planIds.includes(p.id)}
                    className="rounded border-gray-300 text-velox-600 focus:ring-velox-500"
                    onChange={e => setValue('planIds', e.target.checked ? [...planIds, p.id] : planIds.filter(id => id !== p.id), { shouldDirty: true })} />
                  <span className="text-gray-900 dark:text-gray-100">{p.name}</span>
                  <span className="text-gray-400 font-mono text-xs ml-auto">{p.code}</span>
                </label>
              ))}
            </div>
            <p className="text-xs text-gray-500 mt-1">Leave all unchecked for no restriction (applies to all plans)</p>
          </div>
        )}

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}

        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose}
            className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">
            Cancel
          </button>
          <button type="submit" disabled={isSubmitting}
            className="flex items-center justify-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50 transition-colors">
            {isSubmitting ? (<><Loader2 size={14} className="animate-spin" /> Creating...</>) : 'Create Coupon'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

// --- Redemptions Modal ---

function RedemptionsModal({ coupon, onClose }: { coupon: Coupon; onClose: () => void }) {
  const [redemptions, setRedemptions] = useState<CouponRedemption[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    api.listCouponRedemptions(coupon.id).then(res => {
      setRedemptions(res.data || [])
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [coupon.id])

  return (
    <Modal open onClose={onClose} title={`Redemptions -- ${coupon.code}`}>
      <div className="mb-3 flex items-center gap-3">
        <Badge status={coupon.type} />
        <span className="text-sm text-gray-700 dark:text-gray-300 font-medium">{formatDiscount(coupon)}</span>
        <span className="text-sm text-gray-600 dark:text-gray-400">{coupon.times_redeemed} redemption{coupon.times_redeemed !== 1 ? 's' : ''}</span>
      </div>

      {loading ? (
        <div className="py-8 text-center text-sm text-gray-600 dark:text-gray-400">Loading...</div>
      ) : redemptions.length === 0 ? (
        <div className="py-8 text-center">
          <p className="text-sm text-gray-900 dark:text-gray-100">No redemptions yet</p>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">This coupon has not been used</p>
        </div>
      ) : (
        <div className="max-h-80 overflow-y-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-2">Customer</th>
                <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-2">Discount</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-4 py-2">Date</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {redemptions.map(r => (
                <tr key={r.id} className="hover:bg-gray-50/50">
                  <td className="px-4 py-2 text-sm text-gray-900 dark:text-gray-100 font-mono">{r.customer_id.slice(0, 20)}...</td>
                  <td className="px-4 py-2 text-sm font-medium text-emerald-600 text-right tabular-nums">{formatCents(r.discount_cents)}</td>
                  <td className="px-4 py-2 text-sm text-gray-600 dark:text-gray-400">{formatDateTime(r.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="flex justify-end pt-4 border-t border-gray-100 dark:border-gray-800 mt-4">
        <button onClick={onClose}
          className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">
          Close
        </button>
      </div>
    </Modal>
  )
}
