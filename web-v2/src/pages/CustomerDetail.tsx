import { useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm, Controller } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { api, formatCents, formatDate, formatDateTime, type Customer, type BillingProfile, type Plan, type Subscription, type PaymentSetup, type CustomerDunningOverride, type CustomerCouponAssignment } from '@/lib/api'
import { applyApiError, showApiError } from '@/lib/formErrors'
import { Layout } from '@/components/Layout'
import { CostDashboard } from '@/components/CostDashboard'
import { cn } from '@/lib/utils'
import { statusBadgeVariant } from '@/lib/status'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import { Checkbox } from '@/components/ui/checkbox'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger,
} from '@/components/ui/alert-dialog'

import { Loader2, Pencil, CreditCard, Archive, Wand2, Ticket } from 'lucide-react'
import { CopyButton } from '@/components/CopyButton'
import { DetailBreadcrumb } from '@/components/DetailBreadcrumb'
import { Combobox } from '@/components/Combobox'
import {
  COUNTRIES,
  COUNTRY_NAME,
  CURRENCIES as GEO_CURRENCIES,
  statesForCountry,
  stateLabelForCountry,
  postalPlaceholderForCountry,
} from '@/lib/geo'

const statusVariant = statusBadgeVariant

const editCustomerSchema = z.object({
  display_name: z.string().min(1, 'Display name is required'),
  email: z.string().email('Invalid email address').or(z.literal('')),
})
type EditCustomerData = z.infer<typeof editCustomerSchema>

const billingProfileSchema = z.object({
  legal_name: z.string(),
  email: z.string().email('Invalid email address').or(z.literal('')),
  phone: z.string().regex(/^[\+\d\s\-\(\)]{7,20}$/, 'Invalid phone number').or(z.literal('')),
  address_line1: z.string(), address_line2: z.string(),
  city: z.string(), state: z.string(), postal_code: z.string(),
  country: z.string(), currency: z.string(),
  tax_status: z.enum(['standard', 'exempt', 'reverse_charge']),
  tax_exempt_reason: z.string().max(500, 'Must be at most 500 characters'),
  tax_id: z.string(), tax_id_type: z.string(),
}).refine(
  d => d.tax_status !== 'exempt' || d.tax_exempt_reason.trim().length > 0,
  { message: 'Exempt reason is required when tax status is Exempt', path: ['tax_exempt_reason'] }
)
type BillingProfileData = z.infer<typeof billingProfileSchema>

export default function CustomerDetailPage() {
  const { id } = useParams<{ id: string }>()
  const queryClient = useQueryClient()

  const [showEditCustomer, setShowEditCustomer] = useState(false)
  const [showEditBilling, setShowEditBilling] = useState(false)
  const [showCreateSub, setShowCreateSub] = useState(false)
  const [showDunningOverride, setShowDunningOverride] = useState(false)
  const [showAssignCoupon, setShowAssignCoupon] = useState(false)
  const [settingUpPayment, setSettingUpPayment] = useState(false)

  const { data: customer, isLoading, error: loadError, refetch } = useQuery({
    queryKey: ['customer', id],
    queryFn: () => api.getCustomer(id!),
    enabled: !!id,
  })

  const { data: overview } = useQuery({
    queryKey: ['customer-overview', id],
    queryFn: () => api.customerOverview(id!),
    enabled: !!id,
  })

  const { data: balanceData } = useQuery({
    queryKey: ['customer-balance', id],
    queryFn: () => api.getBalance(id!).catch(() => ({ balance_cents: 0 })),
    enabled: !!id,
  })
  const balance = balanceData?.balance_cents ?? 0

  const { data: billingProfile } = useQuery({
    queryKey: ['customer-billing-profile', id],
    queryFn: () => api.getBillingProfile(id!).catch(() => null),
    enabled: !!id,
  })

  const { data: metersData } = useQuery({
    queryKey: ['meters'],
    queryFn: () => api.listMeters(),
  })

  const meterMap: Record<string, { name: string; unit: string }> = {}
  metersData?.data?.forEach(m => { meterMap[m.id] = { name: m.name, unit: m.unit }; meterMap[m.key] = { name: m.name, unit: m.unit } })

  const { data: plans } = useQuery({
    queryKey: ['plans-active'],
    queryFn: () => api.listPlans().then(r => r.data.filter(p => p.status === 'active')),
  })

  const { data: allSubs } = useQuery({
    queryKey: ['customer-subscriptions', id],
    queryFn: () => api.listSubscriptions().then(r => r.data.filter(s => s.customer_id === id)),
    enabled: !!id,
  })

  const { data: paymentSetup } = useQuery({
    queryKey: ['customer-payment-status', id],
    queryFn: () => api.getPaymentStatus(id!).catch(() => ({ customer_id: '', setup_status: 'missing' } as PaymentSetup)),
    enabled: !!id,
  })

  const { data: dunningOverride } = useQuery({
    queryKey: ['customer-dunning-override', id],
    queryFn: () => api.getCustomerDunningOverride(id!).catch(() => null),
    enabled: !!id,
  })

  // 404 on the GET = "no active assignment", so swallow it into null and
  // let the card render the empty state.
  const { data: activeCoupon } = useQuery({
    queryKey: ['customer-coupon', id],
    queryFn: () => api.getCustomerCoupon(id!).catch(() => null),
    enabled: !!id,
  })

  const isArchived = customer?.status === 'archived'

  const invalidateAll = () => {
    queryClient.invalidateQueries({ queryKey: ['customer', id] })
    queryClient.invalidateQueries({ queryKey: ['customer-overview', id] })
    queryClient.invalidateQueries({ queryKey: ['customer-balance', id] })
    queryClient.invalidateQueries({ queryKey: ['customer-billing-profile', id] })
    queryClient.invalidateQueries({ queryKey: ['customer-subscriptions', id] })
    queryClient.invalidateQueries({ queryKey: ['customer-usage', id] })
    queryClient.invalidateQueries({ queryKey: ['customer-payment-status', id] })
    queryClient.invalidateQueries({ queryKey: ['customer-dunning-override', id] })
    queryClient.invalidateQueries({ queryKey: ['customer-coupon', id] })
    queryClient.invalidateQueries({ queryKey: ['customers'] })
  }

  const handleSetupPayment = async () => {
    if (!id || !customer) return
    setSettingUpPayment(true)
    try {
      if (paymentSetup?.setup_status === 'ready') {
        const res = await api.updatePaymentMethod(id)
        window.open(res.url, '_blank')
        toast.success('Stripe payment update page opened in new tab')
      } else {
        const res = await api.setupPayment({
          customer_id: id,
          customer_name: customer.display_name,
          email: customer.email || billingProfile?.email || '',
          address_line1: billingProfile?.address_line1 || '',
          address_city: billingProfile?.city || '',
          address_state: billingProfile?.state || '',
          address_postal_code: billingProfile?.postal_code || '',
          address_country: billingProfile?.country || 'US',
        })
        window.open(res.url, '_blank')
        toast.success('Stripe checkout opened in new tab')
      }
    } catch (err) {
      showApiError(err, 'Failed to set up payment')
    } finally {
      setSettingUpPayment(false)
    }
  }

  const loading = isLoading
  const error = loadError instanceof Error ? loadError.message : loadError ? String(loadError) : null

  if (loading) {
    return (
      <Layout>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Link to="/customers" className="hover:text-foreground transition-colors">Customers</Link>
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

  if (!customer) {
    return (
      <Layout>
        <p className="text-sm text-muted-foreground py-16 text-center">Customer not found</p>
      </Layout>
    )
  }

  return (
    <Layout>
      <DetailBreadcrumb to="/customers" parentLabel="Customers" currentLabel={customer.display_name} />

      {/* Archived Banner */}
      {isArchived && (
        <Card className="mb-4 border-amber-200 bg-amber-50 dark:border-amber-900/50 dark:bg-amber-950/20">
          <CardContent className="px-5 py-3 flex items-center justify-between">
            <p className="text-sm text-amber-800 dark:text-amber-300">This customer has been archived. All data is read-only.</p>
            <Button variant="outline" size="sm"
              onClick={() => {
                api.updateCustomer(customer.id, { status: 'active' } as any).then(() => {
                  toast.success('Customer restored')
                  invalidateAll()
                }).catch((err: Error) => showApiError(err, 'Failed to update'))
              }}>
              Restore Customer
            </Button>
          </CardContent>
        </Card>
      )}

      {/* Header */}
      <div className="flex items-start justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">{customer.display_name}</h1>
          <div className="flex items-center gap-1.5 mt-1">
            <span className="text-xs text-muted-foreground font-mono">{customer.id}</span>
            <CopyButton text={customer.id} />
          </div>
        </div>
        <div className="flex items-center gap-3">
          {!isArchived && (
            <Button variant="outline" size="sm" onClick={() => setShowEditCustomer(true)}>
              <Pencil size={14} className="mr-1.5" />
              Edit
            </Button>
          )}
          {customer.status === 'active' && (
            <AlertDialog>
              <AlertDialogTrigger render={<Button variant="outline" size="sm" className="text-destructive hover:text-destructive" />}>
                <Archive size={14} className="mr-1.5" />
                Archive
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Archive {customer.display_name}?</AlertDialogTitle>
                  <AlertDialogDescription>
                    This customer will be archived. They won't appear in active lists and billing will stop for their subscriptions. This can be undone.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                    onClick={() => {
                      api.updateCustomer(customer.id, { status: 'archived' } as any).then(() => {
                        toast.success('Customer archived')
                        refetch()
                      }).catch((err: Error) => showApiError(err, 'Failed to update'))
                    }}
                  >
                    Archive Customer
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          )}
          <Badge variant={statusVariant(customer.status)}>{customer.status}</Badge>
        </div>
      </div>

      {/* Key Metrics */}
      <Card>
        <CardContent className="p-0">
          <div className="flex divide-x divide-border">
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Email</p>
              <div className="flex items-center gap-2 mt-1">
                <p className="text-sm font-medium text-foreground">{customer.email || '\u2014'}</p>
                {customer.email_status === 'bounced' && (
                  <Badge variant="destructive" className="text-xs" title={customer.email_bounce_reason || 'Permanent delivery failure'}>
                    Bounced
                  </Badge>
                )}
              </div>
            </div>
            <Link to={`/credits?customer=${id}`} className="flex-1 px-6 py-4 hover:bg-accent/50 transition-colors">
              <p className="text-sm text-muted-foreground">Credit Balance</p>
              <p className={cn('text-sm font-medium mt-1', balance > 0 ? 'text-emerald-600' : 'text-foreground')}>{formatCents(balance)}</p>
            </Link>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Subscriptions</p>
              <p className="text-sm font-medium text-foreground mt-1">{allSubs?.length ?? 0}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-muted-foreground">Created</p>
              <p className="text-sm font-medium text-foreground mt-1">{formatDateTime(customer.created_at)}</p>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Details */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">Details</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">External ID</span>
              <span className="text-sm text-foreground font-mono">{customer.external_id}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Email</span>
              <span className="text-sm text-foreground flex items-center gap-2">
                {customer.email || '\u2014'}
                {customer.email_status === 'bounced' && customer.email_last_bounced_at && (
                  <Badge variant="destructive" className="text-xs" title={customer.email_bounce_reason || 'Permanent delivery failure'}>
                    Bounced \u00b7 {formatDate(customer.email_last_bounced_at)}
                  </Badge>
                )}
              </span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Status</span>
              <Badge variant={statusVariant(customer.status)}>{customer.status}</Badge>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Created</span>
              <span className="text-sm text-foreground">{formatDateTime(customer.created_at)}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">ID</span>
              <div className="flex items-center gap-1.5">
                <span className="text-sm text-foreground font-mono">{customer.id}</span>
                <CopyButton text={customer.id} />
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Billing Profile */}
      <Card className="mt-6">
        {billingProfile ? (
          <>
            <CardHeader>
              <div className="flex items-center justify-between">
                <CardTitle className="text-sm">Billing Profile</CardTitle>
                {!isArchived && (
                  <Button variant="outline" size="sm" onClick={() => setShowEditBilling(true)}>
                    <Pencil size={12} className="mr-1.5" />
                    Edit
                  </Button>
                )}
              </div>
            </CardHeader>
            <CardContent className="space-y-6">
              {/* Contact */}
              <section>
                <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground mb-3">Contact</p>
                <div className="grid grid-cols-1 md:grid-cols-3 gap-x-8 gap-y-4">
                  <div>
                    <p className="text-xs text-muted-foreground">Legal Name</p>
                    <p className="text-sm text-foreground mt-1">{billingProfile.legal_name || '\u2014'}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Email</p>
                    <p className="text-sm text-foreground mt-1">{billingProfile.email || '\u2014'}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Phone</p>
                    <p className="text-sm text-foreground mt-1">{billingProfile.phone || '\u2014'}</p>
                  </div>
                </div>
              </section>

              <Separator />

              {/* Address */}
              <section>
                <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground mb-3">Address</p>
                {billingProfile.address_line1 || billingProfile.city || billingProfile.country ? (
                  <div className="text-sm text-foreground leading-relaxed">
                    {billingProfile.address_line1 && <p>{billingProfile.address_line1}</p>}
                    {billingProfile.address_line2 && <p>{billingProfile.address_line2}</p>}
                    {(billingProfile.city || billingProfile.state || billingProfile.postal_code) && (
                      <p>
                        {[billingProfile.city, billingProfile.state].filter(Boolean).join(', ')}
                        {billingProfile.postal_code && ` ${billingProfile.postal_code}`}
                      </p>
                    )}
                    {billingProfile.country && <p>{COUNTRY_NAME[billingProfile.country] || billingProfile.country}</p>}
                  </div>
                ) : (
                  <p className="text-sm text-muted-foreground">No address on file</p>
                )}
              </section>

              <Separator />

              {/* Tax */}
              <section>
                <div className="flex items-center justify-between mb-3">
                  <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Tax</p>
                  {billingProfile.tax_status === 'exempt' && <Badge variant="warning">Exempt</Badge>}
                  {billingProfile.tax_status === 'reverse_charge' && <Badge variant="outline">Reverse charge</Badge>}
                </div>
                {billingProfile.tax_status === 'exempt' ? (
                  <div className="space-y-2">
                    <p className="text-sm text-muted-foreground">Tax-exempt — invoices issue with zero tax and an audit note.</p>
                    {billingProfile.tax_exempt_reason && (
                      <p className="text-sm text-foreground">
                        <span className="text-xs text-muted-foreground">Reason: </span>
                        {billingProfile.tax_exempt_reason}
                      </p>
                    )}
                  </div>
                ) : billingProfile.tax_status === 'reverse_charge' ? (
                  <p className="text-sm text-muted-foreground">
                    B2B reverse charge — tax is zero on the invoice; the buyer self-accounts for VAT/GST in their jurisdiction.
                  </p>
                ) : (
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-x-8 gap-y-4">
                    <div>
                      <p className="text-xs text-muted-foreground">Tax ID Type</p>
                      <p className="text-sm text-foreground mt-1 uppercase">{billingProfile.tax_id_type || '\u2014'}</p>
                    </div>
                    <div>
                      <p className="text-xs text-muted-foreground">Tax ID</p>
                      <p className="text-sm text-foreground mt-1 font-mono">{billingProfile.tax_id || '\u2014'}</p>
                    </div>
                  </div>
                )}
              </section>

              <Separator />

              {/* Currency */}
              <section>
                <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground mb-3">Currency</p>
                <p className="text-sm text-foreground">
                  {billingProfile.currency
                    ? billingProfile.currency.toUpperCase()
                    : <span className="text-muted-foreground">Use tenant default</span>}
                </p>
              </section>
            </CardContent>
          </>
        ) : (
          <>
            <CardHeader>
              <CardTitle className="text-sm">Billing Profile</CardTitle>
            </CardHeader>
            <CardContent className="text-center py-8">
              <div className="w-10 h-10 rounded-full bg-muted flex items-center justify-center mx-auto mb-3">
                <CreditCard size={18} className="text-muted-foreground" />
              </div>
              <p className="text-sm text-foreground">No billing profile</p>
              <p className="text-sm text-muted-foreground mt-1 max-w-xs mx-auto">Set up billing details to enable invoicing and payments for this customer</p>
              {!isArchived && (
                <Button size="sm" className="mt-4" onClick={() => setShowEditBilling(true)}>
                  Set Up Billing Profile
                </Button>
              )}
            </CardContent>
          </>
        )}
      </Card>

      {/* Dunning Override */}
      <Card className="mt-6">
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle className="text-sm">Dunning Override</CardTitle>
            {!isArchived && (
              <Button variant="ghost" size="sm" onClick={() => setShowDunningOverride(true)}>
                {dunningOverride ? 'Edit' : 'Configure'}
              </Button>
            )}
          </div>
        </CardHeader>
        <CardContent className="p-0">
          {dunningOverride ? (
            <div className="divide-y divide-border">
              {dunningOverride.max_retry_attempts != null && (
                <div className="flex items-center justify-between px-6 py-3">
                  <span className="text-sm text-muted-foreground">Max Retry Attempts</span>
                  <span className="text-sm text-foreground">{dunningOverride.max_retry_attempts}</span>
                </div>
              )}
              {dunningOverride.grace_period_days != null && (
                <div className="flex items-center justify-between px-6 py-3">
                  <span className="text-sm text-muted-foreground">Grace Period</span>
                  <span className="text-sm text-foreground">{dunningOverride.grace_period_days} days</span>
                </div>
              )}
              {dunningOverride.final_action && (
                <div className="flex items-center justify-between px-6 py-3">
                  <span className="text-sm text-muted-foreground">Final Action</span>
                  <Badge variant="warning">{dunningOverride.final_action}</Badge>
                </div>
              )}
            </div>
          ) : (
            <p className="px-6 py-6 text-sm text-muted-foreground text-center">Using tenant default policy</p>
          )}
        </CardContent>
      </Card>

      {/* Active Discount — applies to every future invoice until revoked
          or the coupon's duration exhausts. Fires only when the subscription
          has no active coupon on the same invoice (Stripe's precedence rule).
          404 from the API collapses to activeCoupon === null. */}
      <Card className="mt-6">
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle className="text-sm">Active Discount</CardTitle>
            {!isArchived && !activeCoupon && (
              <Button variant="outline" size="sm" onClick={() => setShowAssignCoupon(true)}>
                <Ticket size={14} className="mr-1.5" />
                Apply Coupon
              </Button>
            )}
            {!isArchived && activeCoupon && (
              <AlertDialog>
                <AlertDialogTrigger render={<Button variant="outline" size="sm" className="text-destructive hover:text-destructive" />}>
                  Revoke
                </AlertDialogTrigger>
                <AlertDialogContent>
                  <AlertDialogHeader>
                    <AlertDialogTitle>Revoke {activeCoupon.coupon.code}?</AlertDialogTitle>
                    <AlertDialogDescription>
                      This customer's next invoice will bill at full price unless another coupon is assigned.
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>Cancel</AlertDialogCancel>
                    <AlertDialogAction
                      className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                      onClick={() => {
                        api.revokeCustomerCoupon(id!).then(() => {
                          toast.success('Discount revoked')
                          queryClient.invalidateQueries({ queryKey: ['customer-coupon', id] })
                        }).catch((err: Error) => toast.error(err.message))
                      }}
                    >
                      Revoke Discount
                    </AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            )}
          </div>
        </CardHeader>
        <CardContent className="p-0">
          {activeCoupon ? (
            <div className="divide-y divide-border">
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-muted-foreground">Coupon</span>
                <Link to={`/coupons/${activeCoupon.coupon.id}`} className="text-sm font-mono text-primary hover:underline">
                  {activeCoupon.coupon.code}
                </Link>
              </div>
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-muted-foreground">Discount</span>
                <span className="text-sm font-medium text-foreground">
                  {activeCoupon.coupon.type === 'percentage'
                    ? `${Number.isInteger(activeCoupon.coupon.percent_off_bp / 100) ? activeCoupon.coupon.percent_off_bp / 100 : (activeCoupon.coupon.percent_off_bp / 100).toFixed(2)}%`
                    : formatCents(activeCoupon.coupon.amount_off, activeCoupon.coupon.currency)} off
                </span>
              </div>
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-muted-foreground">Duration</span>
                <span className="text-sm text-foreground">
                  {activeCoupon.coupon.duration === 'once' && 'One invoice'}
                  {activeCoupon.coupon.duration === 'forever' && 'Every invoice'}
                  {activeCoupon.coupon.duration === 'repeating' && `${activeCoupon.coupon.duration_periods} invoice${activeCoupon.coupon.duration_periods === 1 ? '' : 's'} (${activeCoupon.periods_applied} applied)`}
                </span>
              </div>
              <div className="flex items-center justify-between px-6 py-3">
                <span className="text-sm text-muted-foreground">Assigned</span>
                <span className="text-sm text-foreground">{formatDateTime(activeCoupon.created_at)}</span>
              </div>
            </div>
          ) : (
            <p className="px-6 py-6 text-sm text-muted-foreground text-center">No active discount</p>
          )}
        </CardContent>
      </Card>

      {/* Usage This Period — multi-dim cost dashboard backed by
          GET /v1/customers/{id}/usage. Replaces the old quantity-only summary.
          Component is self-contained so the same surface drops into a future
          public iframe-able route once token-based access lands. */}
      <div className="mt-6">
        <CostDashboard customerId={id!} />
      </div>

      {/* Payment Method */}
      <Card className="mt-6">
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle className="text-sm">Payment Method</CardTitle>
            {!isArchived && (
              <Button
                variant={paymentSetup?.setup_status === 'ready' ? 'outline' : 'default'}
                size="sm"
                onClick={handleSetupPayment}
                disabled={settingUpPayment}
              >
                {settingUpPayment ? 'Setting up...' : paymentSetup?.setup_status === 'ready' ? 'Update Payment Method' : paymentSetup?.setup_status === 'pending' ? 'Complete Setup' : 'Set Up Payment'}
              </Button>
            )}
          </div>
        </CardHeader>
        <CardContent>
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              {paymentSetup?.setup_status === 'ready' && paymentSetup.card_last4 ? (
                <>
                  <div className="w-10 h-10 rounded-lg bg-foreground flex items-center justify-center">
                    <CreditCard size={18} className="text-background" />
                  </div>
                  <div>
                    <p className="text-sm font-medium text-foreground">
                      {(paymentSetup.card_brand || 'Card').charAt(0).toUpperCase() + (paymentSetup.card_brand || 'card').slice(1)} ending in {paymentSetup.card_last4}
                    </p>
                    <p className="text-sm text-muted-foreground">
                      Expires {String(paymentSetup.card_exp_month).padStart(2, '0')}/{paymentSetup.card_exp_year}
                    </p>
                  </div>
                </>
              ) : (
                <>
                  <div className={cn(
                    'w-10 h-10 rounded-lg flex items-center justify-center',
                    paymentSetup?.setup_status === 'ready' ? 'bg-emerald-50 dark:bg-emerald-950/20' :
                    paymentSetup?.setup_status === 'pending' ? 'bg-amber-50 dark:bg-amber-950/20' : 'bg-muted'
                  )}>
                    <CreditCard size={18} className={cn(
                      paymentSetup?.setup_status === 'ready' ? 'text-emerald-500' :
                      paymentSetup?.setup_status === 'pending' ? 'text-amber-500' : 'text-muted-foreground'
                    )} />
                  </div>
                  <div>
                    <p className="text-sm text-foreground">
                      {paymentSetup?.setup_status === 'ready' ? 'Payment method active' : paymentSetup?.setup_status === 'pending' ? 'Awaiting payment method setup' : 'No payment method'}
                    </p>
                    <p className="text-sm text-muted-foreground">
                      {paymentSetup?.setup_status === 'ready' ? 'Invoices will be charged automatically' : paymentSetup?.setup_status === 'pending' ? 'Customer needs to complete Stripe Checkout' : 'Set up a payment method to enable automatic billing'}
                    </p>
                  </div>
                </>
              )}
            </div>
            {paymentSetup?.setup_status === 'ready' && (
              <Badge variant="success">Active</Badge>
            )}
          </div>
        </CardContent>
      </Card>

      {/* Subscriptions & Invoices grid */}
      <div className="grid grid-cols-2 gap-6 mt-6">
        {/* Subscriptions */}
        <Card>
          <CardHeader>
            <div className="flex items-center justify-between">
              <CardTitle className="text-sm">Subscriptions ({allSubs?.length ?? 0})</CardTitle>
              {!isArchived && <Button size="sm" onClick={() => setShowCreateSub(true)}>Create Subscription</Button>}
            </div>
          </CardHeader>
          <CardContent className="p-0">
            <div className="divide-y divide-border">
              {allSubs?.map(sub => (
                <Link key={sub.id} to={`/subscriptions/${sub.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-accent/50 transition-colors">
                  <div>
                    <p className="text-sm font-medium text-foreground">{sub.display_name}</p>
                    <p className="text-xs text-muted-foreground">{sub.code}</p>
                  </div>
                  <Badge variant={statusVariant(sub.status)}>{sub.status}</Badge>
                </Link>
              ))}
              {(!allSubs || allSubs.length === 0) && (
                <p className="px-6 py-6 text-sm text-muted-foreground text-center">No subscriptions</p>
              )}
            </div>
          </CardContent>
        </Card>

        {/* Recent Invoices */}
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Recent Invoices</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            <div className="divide-y divide-border">
              {overview?.recent_invoices.map(inv => (
                <Link key={inv.id} to={`/invoices/${inv.id}`} className="flex items-center justify-between px-6 py-3 hover:bg-accent/50 transition-colors">
                  <div>
                    <p className="text-sm font-medium text-foreground">{inv.invoice_number}</p>
                    <p className="text-xs text-muted-foreground">{formatDate(inv.created_at)}</p>
                  </div>
                  <div className="flex items-center gap-3">
                    <Badge variant={statusVariant(inv.status)}>{inv.status}</Badge>
                    <span className="text-sm font-medium">{formatCents(inv.total_amount_cents)}</span>
                  </div>
                </Link>
              ))}
              {(!overview?.recent_invoices.length) && (
                <p className="px-6 py-4 text-sm text-muted-foreground">No invoices</p>
              )}
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Edit Customer Dialog */}
      {showEditCustomer && (
        <EditCustomerDialog
          customer={customer}
          onClose={() => setShowEditCustomer(false)}
          onSaved={() => {
            setShowEditCustomer(false)
            queryClient.invalidateQueries({ queryKey: ['customer', id] })
            toast.success('Customer updated')
          }}
        />
      )}

      {/* Create Subscription Dialog */}
      {showCreateSub && id && plans && (
        <CreateSubscriptionDialog
          customerId={id}
          plans={plans}
          onClose={() => setShowCreateSub(false)}
          onCreated={(sub) => {
            setShowCreateSub(false)
            toast.success(`Subscription "${sub.display_name}" created`)
            invalidateAll()
          }}
        />
      )}

      {/* Edit Billing Profile Dialog */}
      {showEditBilling && id && customer && (
        <EditBillingProfileDialog
          customerId={id}
          customer={customer}
          profile={billingProfile ?? null}
          onClose={() => setShowEditBilling(false)}
          onSaved={() => {
            setShowEditBilling(false)
            queryClient.invalidateQueries({ queryKey: ['customer-billing-profile', id] })
            toast.success('Billing profile saved')
          }}
        />
      )}

      {/* Dunning Override Dialog */}
      {showDunningOverride && id && (
        <DunningOverrideDialog
          customerId={id}
          override={dunningOverride ?? null}
          onClose={() => setShowDunningOverride(false)}
          onSaved={() => {
            setShowDunningOverride(false)
            queryClient.invalidateQueries({ queryKey: ['customer-dunning-override', id] })
            toast.success('Dunning override saved')
          }}
          onDeleted={() => {
            setShowDunningOverride(false)
            queryClient.invalidateQueries({ queryKey: ['customer-dunning-override', id] })
            toast.success('Dunning override removed')
          }}
        />
      )}

      {/* Assign Coupon Dialog */}
      {showAssignCoupon && id && (
        <AssignCouponDialog
          customerId={id}
          onClose={() => setShowAssignCoupon(false)}
          onAssigned={() => {
            setShowAssignCoupon(false)
            queryClient.invalidateQueries({ queryKey: ['customer-coupon', id] })
            toast.success('Discount applied')
          }}
        />
      )}
    </Layout>
  )
}

/* ─── Assign Coupon ───────────────────────────────────────────── */

const assignCouponSchema = z.object({
  code: z.string().min(1, 'Coupon code is required').max(64, 'Code is too long'),
})
type AssignCouponData = z.infer<typeof assignCouponSchema>

function AssignCouponDialog({ customerId, onClose, onAssigned }: {
  customerId: string; onClose: () => void; onAssigned: (a: CustomerCouponAssignment) => void
}) {
  const form = useForm<AssignCouponData>({
    resolver: zodResolver(assignCouponSchema),
    defaultValues: { code: '' },
  })
  const { register, handleSubmit, formState: { errors, isSubmitting } } = form

  const onSubmit = handleSubmit(async (data) => {
    try {
      const assignment = await api.assignCustomerCoupon(customerId, { code: data.code.trim().toUpperCase() })
      onAssigned(assignment)
    } catch (err) {
      applyApiError(form, err, ['code'], { toastTitle: 'Failed to apply coupon' })
    }
  })

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Apply Coupon</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} noValidate className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="code">Coupon Code</Label>
            <Input id="code" placeholder="SAVE20" autoFocus maxLength={64} {...register('code')} />
            {errors.code && <p className="text-xs text-destructive">{errors.code.message}</p>}
            <p className="text-xs text-muted-foreground">
              This discount will apply to every future invoice until revoked or the coupon's duration exhausts.
            </p>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting ? <><Loader2 size={14} className="animate-spin mr-2" />Applying...</> : 'Apply'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/* ─── Edit Customer ──────────────────────────────────────────── */

function EditCustomerDialog({ customer, onClose, onSaved }: {
  customer: Customer; onClose: () => void; onSaved: () => void
}) {
  const form = useForm<EditCustomerData>({
    resolver: zodResolver(editCustomerSchema),
    defaultValues: { display_name: customer.display_name, email: customer.email || '' },
  })
  const { register, handleSubmit, formState: { errors, isSubmitting, isDirty } } = form

  const onSubmit = handleSubmit(async (data) => {
    if (!isDirty) return
    try {
      await api.updateCustomer(customer.id, data)
      onSaved()
    } catch (err) {
      applyApiError(form, err, ['display_name', 'email'], { toastTitle: 'Failed to update customer' })
    }
  })

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Edit Customer</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} noValidate className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="display_name">Display Name</Label>
            <Input id="display_name" maxLength={255} {...register('display_name')} />
            {errors.display_name && <p className="text-xs text-destructive">{errors.display_name.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="email">Email</Label>
            <Input id="email" type="email" maxLength={254} {...register('email')} />
            {errors.email && <p className="text-xs text-destructive">{errors.email.message}</p>}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={isSubmitting || !isDirty}>
              {isSubmitting ? <><Loader2 size={14} className="animate-spin mr-2" />Saving...</> : isDirty ? 'Save' : 'No changes'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/* ─── Create Subscription ────────────────────────────────────── */

function CreateSubscriptionDialog({ customerId, plans, onClose, onCreated }: {
  customerId: string; plans: Plan[]; onClose: () => void; onCreated: (sub: Subscription) => void
}) {
  const [startNow, setStartNow] = useState(true)
  const [planId, setPlanId] = useState('')
  const [planError, setPlanError] = useState('')

  const form = useForm<{ display_name: string; code: string }>({
    resolver: zodResolver(z.object({
      display_name: z.string().min(1, 'Display name is required'),
      code: z.string().min(1, 'Code is required').regex(/^[a-zA-Z0-9_\-]+$/, 'Only letters, numbers, hyphens, and underscores'),
    })),
    defaultValues: { code: '', display_name: '' },
  })
  const { register, handleSubmit, formState: { errors, isSubmitting } } = form

  const onSubmit = handleSubmit(async (data) => {
    if (!planId) { setPlanError('Plan is required'); return }
    setPlanError('')
    try {
      const sub = await api.createSubscription({
        ...data,
        customer_id: customerId,
        items: [{ plan_id: planId }],
        start_now: startNow,
      })
      onCreated(sub)
    } catch (err) {
      applyApiError(form, err, ['display_name', 'code'], { toastTitle: 'Failed to create subscription' })
    }
  })

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Create Subscription</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} noValidate className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="sub_name">Display Name</Label>
            <Input id="sub_name" placeholder="Pro Monthly" maxLength={255} {...register('display_name')} />
            {errors.display_name && <p className="text-xs text-destructive">{errors.display_name.message}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="sub_code">Code</Label>
            <Input id="sub_code" placeholder="pro-monthly" maxLength={100} className="font-mono" {...register('code')} />
            {errors.code && <p className="text-xs text-destructive">{errors.code.message}</p>}
          </div>
          <div className="space-y-2">
            <Label>Plan</Label>
            <Select value={planId} onValueChange={(v) => { setPlanId(v ?? ''); setPlanError('') }}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Select plan...">
                  {(value: string) => {
                    const plan = plans.find(p => p.id === value)
                    return plan ? `${plan.name} (${plan.code})` : value
                  }}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {plans.map(p => (
                  <SelectItem key={p.id} value={p.id}>{p.name} ({p.code})</SelectItem>
                ))}
              </SelectContent>
            </Select>
            {planError && <p className="text-xs text-destructive">{planError}</p>}
          </div>
          <label className="flex items-center gap-2 text-sm cursor-pointer">
            <Checkbox checked={startNow} onCheckedChange={(checked) => setStartNow(checked === true)} />
            Start immediately (activate + set billing period)
          </label>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting ? <><Loader2 size={14} className="animate-spin mr-2" />Creating...</> : 'Create Subscription'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/* ─── Edit Billing Profile ───────────────────────────────────── */

const TAX_ID_HINTS: Record<string, string> = {
  gst: 'e.g. 29ABCDE1234F1Z5 (India GSTIN)',
  vat: 'e.g. GB123456789 (UK), DE123456789 (EU)',
  ein: 'e.g. 12-3456789 (US Employer ID)',
  abn: 'e.g. 12 345 678 901 (Australia)',
  other: 'Enter the identifier in your jurisdiction\u2019s format',
}

function EditBillingProfileDialog({ customerId, customer, profile, onClose, onSaved }: {
  customerId: string
  customer: Customer
  profile: BillingProfile | null
  onClose: () => void
  onSaved: () => void
}) {
  const defaultValues: BillingProfileData = {
    legal_name: profile?.legal_name || '',
    email: profile?.email || '',
    phone: profile?.phone || '',
    address_line1: profile?.address_line1 || '',
    address_line2: profile?.address_line2 || '',
    city: profile?.city || '',
    state: profile?.state || '',
    postal_code: profile?.postal_code || '',
    country: profile?.country || '',
    currency: profile?.currency || '',
    tax_status: profile?.tax_status || 'standard',
    tax_exempt_reason: profile?.tax_exempt_reason || '',
    tax_id: profile?.tax_id || '',
    tax_id_type: profile?.tax_id_type || '',
  }

  const formMethods = useForm<BillingProfileData>({
    resolver: zodResolver(billingProfileSchema),
    defaultValues,
  })
  const { register, handleSubmit, watch, setValue, control, formState: { errors: formErrors, isSubmitting, isDirty } } = formMethods
  const form = watch()

  const onSubmit = handleSubmit(async (data) => {
    if (!isDirty) return
    try {
      await api.upsertBillingProfile(customerId, data)
      onSaved()
    } catch (err) {
      applyApiError(formMethods, err, [
        'legal_name', 'email', 'phone',
        'address_line1', 'address_line2', 'city', 'state', 'postal_code', 'country',
        'currency', 'tax_status', 'tax_exempt_reason', 'tax_id', 'tax_id_type',
      ], { toastTitle: 'Failed to save billing profile' })
    }
  })

  const fillFromCustomer = () => {
    if (!form.legal_name && customer.display_name) {
      setValue('legal_name', customer.display_name, { shouldDirty: true })
    }
    if (!form.email && customer.email) {
      setValue('email', customer.email, { shouldDirty: true })
    }
  }

  const countryOptions = COUNTRIES.map(([code, name]) => ({
    value: code, label: name, keywords: [code],
  }))
  const statesList = statesForCountry(form.country)
  const stateLabel = stateLabelForCountry(form.country)
  const postalPlaceholder = postalPlaceholderForCountry(form.country)
  const stateOptions = statesList
    ? statesList.map(([code, name]) => ({ value: code, label: name, keywords: [code] }))
    : null

  const currencyOptions = [
    { value: '', label: 'Use tenant default', keywords: ['default', 'tenant', 'inherit'] },
    ...GEO_CURRENCIES.map(c => ({
      value: c.code.toLowerCase(),
      label: `${c.code} — ${c.label}`,
      keywords: [c.code, c.code.toLowerCase(), c.symbol, c.label],
      prefix: <span className="text-muted-foreground font-mono text-[11px] w-7 inline-block text-center">{c.symbol}</span>,
    })),
  ]

  const canFillFromCustomer = !profile && (
    (!form.legal_name && !!customer.display_name) || (!form.email && !!customer.email)
  )

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-2xl max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{profile ? 'Edit Billing Profile' : 'Set Up Billing Profile'}</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} noValidate className="space-y-6">

          {canFillFromCustomer && (
            <button
              type="button"
              onClick={fillFromCustomer}
              className="w-full flex items-center justify-between gap-3 px-3 py-2.5 rounded-md border border-dashed border-border bg-muted/30 hover:bg-muted text-sm text-foreground transition-colors"
            >
              <span className="flex items-center gap-2 min-w-0">
                <Wand2 size={14} className="shrink-0 text-muted-foreground" />
                <span>Use customer details for name and email</span>
              </span>
              <span className="text-xs text-muted-foreground truncate">
                {customer.display_name}{customer.email && ` \u00b7 ${customer.email}`}
              </span>
            </button>
          )}

          {/* Contact */}
          <div className="space-y-3">
            <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Contact</p>
            <div className="space-y-2">
              <Label>Legal Name</Label>
              <Input maxLength={255} placeholder="Acme Corporation Inc." {...register('legal_name')} />
            </div>
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>Email</Label>
                <Input type="email" maxLength={254} placeholder="billing@acme.com" {...register('email')} />
                {formErrors.email && <p className="text-xs text-destructive">{formErrors.email.message}</p>}
              </div>
              <div className="space-y-2">
                <Label>Phone</Label>
                <Input type="tel" placeholder="+1 (555) 123-4567" maxLength={20} {...register('phone')} />
                {formErrors.phone && <p className="text-xs text-destructive">{formErrors.phone.message}</p>}
              </div>
            </div>
          </div>

          <Separator />

          {/* Address */}
          <div className="space-y-3">
            <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Address</p>
            <div className="space-y-2">
              <Label>Country</Label>
              <Combobox
                value={form.country}
                onChange={(val) => {
                  setValue('country', val, { shouldDirty: true })
                  setValue('state', '', { shouldDirty: true })
                }}
                options={countryOptions}
                placeholder="Select country..."
                clearable
              />
            </div>
            <div className="space-y-2">
              <Label>Street Address</Label>
              <Input maxLength={200} placeholder="123 Main Street" {...register('address_line1')} />
            </div>
            <div className="space-y-2">
              <Label>Apt / Suite / Floor <span className="text-muted-foreground font-normal">(optional)</span></Label>
              <Input maxLength={200} placeholder="Suite 100" {...register('address_line2')} />
            </div>
            <div className="grid grid-cols-3 gap-4">
              <div className="space-y-2">
                <Label>City</Label>
                <Input maxLength={100} placeholder="San Francisco" {...register('city')} />
              </div>
              <div className="space-y-2">
                <Label>{stateLabel}</Label>
                {stateOptions ? (
                  <Combobox
                    value={form.state}
                    onChange={(val) => setValue('state', val, { shouldDirty: true })}
                    options={stateOptions}
                    placeholder={`Select ${stateLabel.toLowerCase()}...`}
                    clearable
                  />
                ) : (
                  <Input placeholder={stateLabel} maxLength={100} {...register('state')} />
                )}
              </div>
              <div className="space-y-2">
                <Label>Postal Code</Label>
                <Input placeholder={postalPlaceholder} maxLength={20} {...register('postal_code')} />
              </div>
            </div>
          </div>

          <Separator />

          {/* Tax — status picks a path: standard (tax ID fields), exempt (reason), reverse_charge (buyer's tax ID) */}
          <div className="space-y-3">
            <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Tax</p>

            <div className="space-y-2">
              <Label>Tax status</Label>
              <Controller
                name="tax_status"
                control={control}
                render={({ field }) => (
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="standard">Standard — tax applied per provider rules</SelectItem>
                      <SelectItem value="exempt">Exempt — no tax (certificate, non-profit, etc.)</SelectItem>
                      <SelectItem value="reverse_charge">Reverse charge — B2B buyer self-accounts</SelectItem>
                    </SelectContent>
                  </Select>
                )}
              />
              <p className="text-xs text-muted-foreground">
                {form.tax_status === 'exempt'
                  ? 'Invoice ships with zero tax and the exempt reason printed on the PDF audit trail.'
                  : form.tax_status === 'reverse_charge'
                    ? 'Invoice ships with zero tax and the reverse-charge VAT legend directing the buyer to self-assess.'
                    : 'Tax is calculated by the tenant\'s configured provider.'}
              </p>
            </div>

            {form.tax_status === 'exempt' && (
              <div className="space-y-2">
                <Label>Exempt reason</Label>
                <Input
                  maxLength={500}
                  placeholder="e.g. 501(c)(3) non-profit — certificate on file"
                  {...register('tax_exempt_reason')}
                />
                {formErrors.tax_exempt_reason
                  ? <p className="text-xs text-destructive">{formErrors.tax_exempt_reason.message}</p>
                  : <p className="text-xs text-muted-foreground">Printed on every invoice for this customer as audit trail.</p>}
              </div>
            )}

            {form.tax_status !== 'exempt' && (
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label>Tax ID Type</Label>
                  <Select value={form.tax_id_type} onValueChange={(val) => setValue('tax_id_type', val ?? '', { shouldDirty: true })}>
                    <SelectTrigger className="w-full">
                      <SelectValue placeholder="Select type..." />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="">None</SelectItem>
                      <SelectItem value="gst">GST</SelectItem>
                      <SelectItem value="vat">VAT</SelectItem>
                      <SelectItem value="ein">EIN</SelectItem>
                      <SelectItem value="abn">ABN</SelectItem>
                      <SelectItem value="other">Other</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label>Tax ID</Label>
                  <Input maxLength={50} className="font-mono" placeholder="Identifier" {...register('tax_id')} />
                  {form.tax_id_type && TAX_ID_HINTS[form.tax_id_type] && (
                    <p className="text-xs text-muted-foreground">{TAX_ID_HINTS[form.tax_id_type]}</p>
                  )}
                  {form.tax_status === 'reverse_charge' && (
                    <p className="text-xs text-muted-foreground">Required — reverse charge is only valid when the buyer has a registered tax ID.</p>
                  )}
                </div>
              </div>
            )}
          </div>

          <Separator />

          {/* Billing */}
          <div className="space-y-3">
            <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Billing</p>
            <div className="space-y-2">
              <Label>Currency</Label>
              <Combobox
                value={form.currency}
                onChange={(val) => setValue('currency', val, { shouldDirty: true })}
                options={currencyOptions}
                placeholder="Select currency..."
              />
              <p className="text-xs text-muted-foreground">Overrides the tenant default for invoices on this customer.</p>
            </div>
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={isSubmitting || !isDirty}>
              {isSubmitting ? <><Loader2 size={14} className="animate-spin mr-2" />Saving...</> : isDirty ? 'Save' : 'No changes'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/* ─── Dunning Override ───────────────────────────────────────── */

function DunningOverrideDialog({ customerId, override, onClose, onSaved, onDeleted }: {
  customerId: string; override: CustomerDunningOverride | null; onClose: () => void; onSaved: () => void; onDeleted: () => void
}) {
  const [form, setForm] = useState({
    max_retry_attempts: override?.max_retry_attempts != null ? String(override.max_retry_attempts) : '',
    grace_period_days: override?.grace_period_days != null ? String(override.grace_period_days) : '',
    final_action: override?.final_action || '',
  })
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    try {
      const payload: Partial<CustomerDunningOverride> = {}
      if (form.max_retry_attempts !== '') payload.max_retry_attempts = parseInt(form.max_retry_attempts, 10)
      if (form.grace_period_days !== '') payload.grace_period_days = parseInt(form.grace_period_days, 10)
      if (form.final_action) payload.final_action = form.final_action
      await api.upsertCustomerDunningOverride(customerId, payload)
      onSaved()
    } catch (err) {
      showApiError(err, 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    setDeleting(true)
    try {
      await api.deleteCustomerDunningOverride(customerId)
      onDeleted()
    } catch (err) {
      showApiError(err, 'Failed to delete')
    } finally {
      setDeleting(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Dunning Override</DialogTitle>
        </DialogHeader>
        <form onSubmit={handleSubmit} noValidate className="space-y-4">
          <div className="space-y-2">
            <Label>Max Retry Attempts</Label>
            <Input type="number" value={form.max_retry_attempts} placeholder="Leave empty for tenant default"
              onChange={e => setForm(f => ({ ...f, max_retry_attempts: e.target.value }))} />
            <p className="text-xs text-muted-foreground">How many times to retry the failed payment before escalating</p>
          </div>
          <div className="space-y-2">
            <Label>Grace Period (days)</Label>
            <Input type="number" value={form.grace_period_days} placeholder="Leave empty for tenant default"
              onChange={e => setForm(f => ({ ...f, grace_period_days: e.target.value }))} />
            <p className="text-xs text-muted-foreground">Days to wait after initial failure before first retry</p>
          </div>
          <div className="space-y-2">
            <Label>Final Action</Label>
            <Select value={form.final_action} onValueChange={(val) => setForm(f => ({ ...f, final_action: val ?? '' }))}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Tenant default" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">Tenant default</SelectItem>
                <SelectItem value="manual_review">Escalate for review</SelectItem>
                <SelectItem value="pause">Pause subscription</SelectItem>
                <SelectItem value="write_off_later">Mark uncollectible</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex justify-between pt-2">
            {override ? (
              <Button type="button" variant="outline" className="border-destructive text-destructive hover:bg-destructive/10" onClick={handleDelete} disabled={deleting}>
                {deleting ? 'Removing...' : 'Reset to Default'}
              </Button>
            ) : <div />}
            <div className="flex gap-2">
              <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
              <Button type="submit" disabled={saving}>
                {saving ? <><Loader2 size={14} className="animate-spin mr-2" />Saving...</> : 'Save'}
              </Button>
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
