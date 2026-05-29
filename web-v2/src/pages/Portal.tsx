import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { Badge } from '@/components/ui/badge'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import {
  CreditCard, Download, FileText, LogOut, Loader2, AlertCircle, ExternalLink,
} from 'lucide-react'
import { VeloxLogo } from '@/components/VeloxLogo'
import {
  clearPortalSession, getPortalSession, portalFetch, PortalAuthError,
} from '@/lib/portalAuth'

// Customer-facing billing portal. Authenticated by the magic-link
// session token from PortalMagic; renders the customer's own subs,
// invoices, and payment-method controls. Operator-grade Velox
// dashboard chrome is intentionally absent — this is for end
// customers, not operators.
//
// Three sections: Subscriptions (with cancel), Invoices (with PDF
// download), Payment method (with Update card). All actions hit
// /v1/me/* so cross-customer access is impossible by construction.

interface PortalSub {
  id: string
  status: string
  code: string
  display_name: string
  current_billing_period_end?: string
  next_billing_at?: string
  cancel_at_period_end?: boolean
}

interface PortalInvoice {
  id: string
  invoice_number: string
  status: string
  payment_status: string
  currency: string
  total_amount_cents: number
  amount_due_cents: number
  amount_paid_cents: number
  credits_applied_cents?: number
  issued_at?: string
  paid_at?: string
  public_token?: string
}

interface PortalPaymentMethod {
  id: string
  type: string
  card_brand?: string
  card_last4?: string
  card_exp_month?: number
  card_exp_year?: number
  is_default?: boolean
}

interface PortalBranding {
  company_name?: string
  brand_color?: string
  support_url?: string
}

interface PortalProfile {
  customer_id: string
  display_name?: string
  email?: string
}

interface PortalCreditEntry {
  id: string
  entry_type: string
  amount_cents: number
  description?: string
  expires_at?: string
  created_at: string
}

interface PortalCreditBalance {
  balance_cents: number
  total_granted: number
  total_used: number
  total_expired: number
  recent_entries: PortalCreditEntry[]
}

const apiBase = import.meta.env.VITE_API_URL || ''

function formatCurrency(cents: number, currency: string): string {
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: currency.toUpperCase(),
    minimumFractionDigits: 2,
  }).format(cents / 100)
}

function formatDate(iso?: string): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleDateString('en-US', {
    year: 'numeric', month: 'short', day: 'numeric',
  })
}

// formatDateRelative returns "in 3 days" / "tomorrow" / "today" /
// "yesterday" / "3 days ago" for events within ±14 days; falls back
// to absolute formatting beyond that window. Aligns with the polish
// in Stripe + Linear UIs where near-term dates read naturally and
// far-term dates need precise context.
function formatDateRelative(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  const now = new Date()
  const diffMs = d.getTime() - now.getTime()
  const diffDays = Math.round(diffMs / (1000 * 60 * 60 * 24))
  if (diffDays === 0) return 'today'
  if (diffDays === 1) return 'tomorrow'
  if (diffDays === -1) return 'yesterday'
  if (diffDays > 1 && diffDays <= 14) return `in ${diffDays} days`
  if (diffDays < -1 && diffDays >= -14) return `${Math.abs(diffDays)} days ago`
  return formatDate(iso)
}

// resolveCurrency picks the right ISO code for money display. Prefers
// the customer's billing currency (from an invoice — invoices carry
// per-row currency at finalize time, so the most recent paid invoice
// is the most reliable signal of the customer's billing currency).
// Falls back to USD when nothing's been billed yet.
function resolveCurrency(invoices: PortalInvoice[]): string {
  for (const inv of invoices) {
    if (inv.currency) return inv.currency
  }
  return 'USD'
}

function statusToneSub(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (status) {
    case 'active': return 'default'
    case 'trialing': return 'secondary'
    case 'canceled': case 'paused': case 'archived': return 'destructive'
    default: return 'outline'
  }
}

function statusToneInvoice(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (status) {
    case 'paid': return 'default'
    case 'voided': return 'destructive'
    case 'finalized': return 'secondary'
    default: return 'outline'
  }
}

// Title-case a Stripe card brand for display. Stripe returns lower-case
// brand identifiers ("visa", "mastercard", "amex"); customers expect
// "Visa", "Mastercard", "Amex".
function formatCardBrand(brand?: string): string {
  if (!brand) return 'Card'
  if (brand === 'amex') return 'Amex'
  return brand.charAt(0).toUpperCase() + brand.slice(1).toLowerCase()
}

// "12 / 29" format for card expiry, matching Stripe Dashboard convention.
function formatCardExp(m?: number, y?: number): string {
  if (!m || !y) return ''
  const mm = String(m).padStart(2, '0')
  const yy = String(y).slice(-2)
  return `${mm} / ${yy}`
}

// Portal data layer is split across six React Query keys, one per
// /v1/me/* endpoint. Each section of the page subscribes to exactly
// what it needs — `useQuery(['portal','subscriptions'])` etc. —
// rather than waiting on a single `load()` that blocks on the slowest
// fetch. Migrated 2026-05-27 from raw `useEffect` + `useState` +
// `portalFetch` to match the operator dashboard's pattern and fix
// the StrictMode-induced 2× fetch in dev. RQ also gives us cache,
// dedupe, stale-while-revalidate, and one-line invalidation on
// mutation for free.
const PORTAL_KEYS = {
  subscriptions: ['portal', 'subscriptions'] as const,
  invoices: ['portal', 'invoices'] as const,
  paymentMethods: ['portal', 'payment-methods'] as const,
  profile: ['portal', 'profile'] as const,
  creditBalance: ['portal', 'credit-balance'] as const,
  branding: ['portal', 'branding'] as const,
}

export default function Portal() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  // hasSession gates every query: don't fire fetches until the
  // localStorage token is present. The effect below redirects to
  // /portal/login if it's missing; the queries stay disabled in the
  // meantime so we don't see 401-redirect storms during the
  // unmount-then-remount StrictMode dance.
  const hasSession = !!getPortalSession()
  useEffect(() => {
    if (!hasSession) navigate('/portal/login', { replace: true })
  }, [hasSession, navigate])

  // onError handler shared by every portal query: PortalAuthError
  // (401 from /v1/me/*) means the session token is no longer valid
  // — drop straight to the login page. Other errors surface in the
  // "Couldn't load portal" fallback. Wrapped in useRef so the
  // useQuery options don't churn on every render.
  const handleQueryError = useRef((err: unknown) => {
    if (err instanceof PortalAuthError) {
      navigate('/portal/login', { replace: true })
    }
  })

  const subsQuery = useQuery({
    queryKey: PORTAL_KEYS.subscriptions,
    queryFn: () => portalFetch<{ data: PortalSub[]; total: number }>('GET', '/v1/me/subscriptions'),
    enabled: hasSession,
  })
  const invoicesQuery = useQuery({
    queryKey: PORTAL_KEYS.invoices,
    queryFn: () => portalFetch<{ data: PortalInvoice[]; total: number }>('GET', '/v1/me/invoices'),
    enabled: hasSession,
  })
  const pmQuery = useQuery({
    queryKey: PORTAL_KEYS.paymentMethods,
    queryFn: () => portalFetch<{ data: PortalPaymentMethod[]; total: number }>('GET', '/v1/me/payment-methods'),
    enabled: hasSession,
  })
  const profileQuery = useQuery({
    queryKey: PORTAL_KEYS.profile,
    queryFn: () => portalFetch<PortalProfile>('GET', '/v1/me/profile'),
    enabled: hasSession,
  })
  const creditQuery = useQuery({
    queryKey: PORTAL_KEYS.creditBalance,
    queryFn: () => portalFetch<PortalCreditBalance>('GET', '/v1/me/credit-balance'),
    enabled: hasSession,
  })
  const brandingQuery = useQuery({
    queryKey: PORTAL_KEYS.branding,
    queryFn: () => portalFetch<PortalBranding>('GET', '/v1/me/branding'),
    enabled: hasSession,
  })

  // Auth-error redirect routing: a single useEffect watches every
  // query's error. PortalAuthError on any of them means the session
  // is gone; kick to /portal/login once. Cheaper than threading an
  // onError into each useQuery's options.
  useEffect(() => {
    const errors = [subsQuery.error, invoicesQuery.error, pmQuery.error, profileQuery.error, creditQuery.error, brandingQuery.error]
    for (const err of errors) {
      if (err) handleQueryError.current(err)
    }
  }, [subsQuery.error, invoicesQuery.error, pmQuery.error, profileQuery.error, creditQuery.error, brandingQuery.error])

  // Unwrap query results to the variable names the rest of the
  // component already uses. Defaults match the empty/initial state
  // the legacy load() established.
  const subs = subsQuery.data?.data ?? []
  const invoices = invoicesQuery.data?.data ?? []
  const paymentMethods = pmQuery.data?.data ?? []
  const profile = profileQuery.data ?? null
  const creditBalance = creditQuery.data ?? null
  const branding = brandingQuery.data ?? {}

  // First-paint loading: show skeletons until every section has its
  // first response. Subsequent refetches don't trigger the skeleton —
  // RQ's `isLoading` is true only when there's no cached data yet.
  const loading = subsQuery.isLoading || invoicesQuery.isLoading || pmQuery.isLoading ||
    profileQuery.isLoading || creditQuery.isLoading || brandingQuery.isLoading

  // Aggregate non-auth error message for the fallback card. Auth
  // errors are intercepted by the redirect effect above; anything
  // else surfaces here.
  const firstError = [subsQuery.error, invoicesQuery.error, pmQuery.error, profileQuery.error, creditQuery.error, brandingQuery.error]
    .find(e => e && !(e instanceof PortalAuthError))
  const error = firstError instanceof Error ? firstError.message : (firstError ? String(firstError) : '')

  const retryAll = () => {
    void subsQuery.refetch(); void invoicesQuery.refetch(); void pmQuery.refetch()
    void profileQuery.refetch(); void creditQuery.refetch(); void brandingQuery.refetch()
  }

  // Local UI state that doesn't belong in the server cache.
  const [editingProfile, setEditingProfile] = useState(false)
  const [profileForm, setProfileForm] = useState({ display_name: '', email: '' })
  const [showCreditHistory, setShowCreditHistory] = useState(false)
  const [cancelTarget, setCancelTarget] = useState<PortalSub | null>(null)

  // Derived view state for the above-the-fold summary. Currency is
  // resolved once and reused everywhere a money value renders so the
  // page reads in the customer's actual billing currency, not USD.
  const displayCurrency = resolveCurrency(invoices)
  const unpaidInvoices = invoices.filter(inv =>
    inv.payment_status !== 'paid' && inv.amount_due_cents > 0
  )
  const outstandingCents = unpaidInvoices.reduce((sum, inv) => sum + inv.amount_due_cents, 0)
  const nextDueInvoice = unpaidInvoices[0]
  const activeSubsCount = subs.filter(s => s.status === 'active' || s.status === 'trialing').length
  const defaultPM = paymentMethods.find(pm => pm.is_default)

  // Stripe Checkout return handling. When the customer adds a card via
  // the in-portal "Add payment method" flow, Stripe redirects back here
  // with ?status=success|cancel (see paymentmethods/service.go which
  // appends the query). Surface a toast + invalidate the PM list so
  // the new card appears without a manual reload.
  //
  // handledStatus dedupes: React StrictMode in dev runs effects twice
  // and setSearchParams below also triggers a re-render, so without the
  // ref guard the same status value fires the toast twice. Mirrors the
  // dedupe pattern in CustomerDetail.tsx's ?payment= handler.
  const [searchParams, setSearchParams] = useSearchParams()
  const handledStatus = useRef<string | null>(null)
  useEffect(() => {
    const status = searchParams.get('status')
    if (!status) return
    if (handledStatus.current === status) return
    handledStatus.current = status
    if (status === 'success') {
      toast.success('Payment method added')
      void queryClient.invalidateQueries({ queryKey: PORTAL_KEYS.paymentMethods })
    } else if (status === 'cancel') {
      toast.info('Setup canceled — no changes were made')
    }
    const next = new URLSearchParams(searchParams)
    next.delete('status')
    setSearchParams(next, { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams])

  // Mutations. Each invalidates only the query key(s) it actually
  // changes — RQ refetches just those subscriptions automatically.
  // onError funnels PortalAuthError to the login redirect for
  // consistency with the GET path.
  const handleMutationError = (err: unknown, fallback: string) => {
    if (err instanceof PortalAuthError) {
      navigate('/portal/login', { replace: true })
      return
    }
    toast.error(err instanceof Error ? err.message : fallback)
  }

  const cancelMutation = useMutation({
    mutationFn: (subID: string) => portalFetch('POST', `/v1/me/subscriptions/${subID}/cancel`),
    onSuccess: () => {
      toast.success('Subscription canceled')
      setCancelTarget(null)
      void queryClient.invalidateQueries({ queryKey: PORTAL_KEYS.subscriptions })
      void queryClient.invalidateQueries({ queryKey: PORTAL_KEYS.invoices })
    },
    onError: (err) => handleMutationError(err, 'Cancel failed'),
  })
  const handleCancel = () => {
    if (!cancelTarget) return
    cancelMutation.mutate(cancelTarget.id)
  }

  const resumeMutation = useMutation({
    mutationFn: (subID: string) => portalFetch('POST', `/v1/me/subscriptions/${subID}/resume`),
    onSuccess: () => {
      toast.success('Subscription resumed — renewal scheduled')
      void queryClient.invalidateQueries({ queryKey: PORTAL_KEYS.subscriptions })
    },
    onError: (err) => handleMutationError(err, 'Resume failed'),
  })
  const handleResume = (sub: PortalSub) => resumeMutation.mutate(sub.id)

  const payNowMutation = useMutation({
    mutationFn: (invID: string) => portalFetch('POST', `/v1/me/invoices/${invID}/pay`),
    onSuccess: () => {
      toast.success('Payment initiated — refreshing')
      void queryClient.invalidateQueries({ queryKey: PORTAL_KEYS.invoices })
    },
    onError: (err) => handleMutationError(err, 'Payment failed'),
  })
  const handlePayNow = (inv: PortalInvoice) => payNowMutation.mutate(inv.id)

  const setDefaultPMMutation = useMutation({
    mutationFn: (pm: PortalPaymentMethod) =>
      portalFetch('POST', `/v1/me/payment-methods/${pm.id}/default`).then(() => pm),
    onSuccess: (pm) => {
      toast.success(`${formatCardBrand(pm.card_brand)} ending in ${pm.card_last4 || '••••'} is now the default`)
      void queryClient.invalidateQueries({ queryKey: PORTAL_KEYS.paymentMethods })
    },
    onError: (err) => handleMutationError(err, 'Could not set default'),
  })
  const handleSetDefaultPM = (pm: PortalPaymentMethod) => setDefaultPMMutation.mutate(pm)

  const removePMMutation = useMutation({
    mutationFn: (pm: PortalPaymentMethod) => portalFetch('DELETE', `/v1/me/payment-methods/${pm.id}`),
    onSuccess: () => {
      toast.success('Card removed')
      void queryClient.invalidateQueries({ queryKey: PORTAL_KEYS.paymentMethods })
    },
    onError: (err) => handleMutationError(err, 'Remove failed'),
  })
  const handleRemovePM = (pm: PortalPaymentMethod) => removePMMutation.mutate(pm)

  const saveProfileMutation = useMutation({
    mutationFn: (body: { display_name?: string; email?: string }) =>
      portalFetch('PATCH', '/v1/me/profile', body),
    onSuccess: () => {
      toast.success('Profile updated')
      setEditingProfile(false)
      void queryClient.invalidateQueries({ queryKey: PORTAL_KEYS.profile })
    },
    onError: (err) => handleMutationError(err, 'Save failed'),
  })
  const handleSaveProfile = (newName: string, newEmail: string) => {
    const body: { display_name?: string; email?: string } = {}
    if (newName !== (profile?.display_name ?? '')) body.display_name = newName
    if (newEmail !== (profile?.email ?? '')) body.email = newEmail
    if (Object.keys(body).length === 0) {
      setEditingProfile(false)
      return
    }
    saveProfileMutation.mutate(body)
  }

  const setupSessionMutation = useMutation({
    mutationFn: () =>
      // /payment-methods/setup-session is bootstrap-aware via
      // paymentmethods.StripeAdapter.EnsureStripeCustomer — handles
      // both "first-time add card" and "update existing card" in one
      // call. No operator-initiated initial-setup step needed.
      portalFetch<{ url: string }>('POST', '/v1/me/payment-methods/setup-session', { return_url: window.location.href }),
    onSuccess: (res) => {
      // Open Stripe Checkout in a new tab so the customer can return
      // here on cancel/success without losing portal context.
      window.open(res.url, '_blank', 'noopener,noreferrer')
    },
    onError: (err) => handleMutationError(err, 'Could not start payment-method setup'),
  })
  const handleUpdatePM = () => setupSessionMutation.mutate()

  // Single "any mutation in flight" flag — preserves the legacy
  // `acting` semantics so existing JSX disable bindings keep working.
  const acting = cancelMutation.isPending || resumeMutation.isPending || payNowMutation.isPending ||
    setDefaultPMMutation.isPending || removePMMutation.isPending || saveProfileMutation.isPending ||
    setupSessionMutation.isPending

  const downloadInvoicePDF = async (inv: PortalInvoice) => {
    try {
      const session = getPortalSession()
      if (!session) {
        navigate('/portal/login', { replace: true })
        return
      }
      const res = await fetch(`${apiBase}/v1/me/invoices/${inv.id}/pdf`, {
        headers: { 'Authorization': `Bearer ${session.token}` },
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${inv.invoice_number}.pdf`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'PDF download failed')
    }
  }

  const handleSignOut = () => {
    clearPortalSession()
    navigate('/portal/login', { replace: true })
  }

  if (loading) {
    // Per-section skeletons match the rendered layout below so the
    // page doesn't jump on first paint. Industry standard (Linear,
    // Stripe, Vercel) — pulsing placeholder cards beat a spinner.
    return (
      <div className="min-h-screen bg-background">
        <header className="border-b border-border bg-card">
          <div className="max-w-4xl mx-auto px-4 py-4 h-[60px]" />
        </header>
        <main className="max-w-4xl mx-auto px-4 py-8 space-y-8">
          {[1, 2, 3, 4].map(i => (
            <section key={i}>
              <div className="h-5 w-32 bg-muted rounded animate-pulse mb-3" />
              <Card>
                <CardContent className="px-6 py-5 space-y-2">
                  <div className="h-4 w-3/4 bg-muted rounded animate-pulse" />
                  <div className="h-3 w-1/2 bg-muted rounded animate-pulse" />
                </CardContent>
              </Card>
            </section>
          ))}
        </main>
      </div>
    )
  }

  if (error) {
    return (
      <div className="min-h-screen flex items-center justify-center px-4">
        <Card className="w-full max-w-md">
          <CardContent className="p-6 text-center space-y-3">
            <AlertCircle className="mx-auto h-10 w-10 text-destructive" />
            <p className="text-sm font-medium text-foreground">Couldn't load portal</p>
            <p className="text-xs text-muted-foreground">{error}</p>
            <Button variant="outline" onClick={retryAll}>Retry</Button>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-background">
      {/* Header */}
      <header className="border-b border-border bg-card">
        <div className="max-w-4xl mx-auto px-4 py-4 flex items-center justify-between">
          <div className="flex items-center gap-3 min-w-0">
            <VeloxLogo size="sm" />
            <div className="hidden sm:flex flex-col min-w-0">
              <span className="text-sm text-muted-foreground truncate">
                {branding.company_name || 'Billing portal'}
              </span>
              {profile?.display_name && (
                <span className="text-[11px] text-muted-foreground/70 truncate">
                  {profile.display_name}
                  {profile.email && <span className="ml-1">· {profile.email}</span>}
                </span>
              )}
            </div>
          </div>
          <div className="flex items-center gap-2">
            {branding.support_url && (
              <Button asChild variant="ghost" size="sm">
                <a href={branding.support_url} target="_blank" rel="noopener noreferrer">
                  Support <ExternalLink size={12} className="ml-1.5" />
                </a>
              </Button>
            )}
            <Button variant="outline" size="sm" onClick={handleSignOut}>
              <LogOut size={14} className="mr-1.5" />
              Sign out
            </Button>
          </div>
        </div>
      </header>

      <main className="max-w-4xl mx-auto px-4 py-8 space-y-8">
        {/* Outstanding balance banner. Renders only when the customer
            owes money — quiet otherwise. Industry standard (Stripe
            customer portal "Amount due" header) is a focused, single-
            call-to-action attention strip above the dense data. */}
        {outstandingCents > 0 && (
          <div className="border border-destructive/30 bg-destructive/5 rounded-lg px-5 py-4 flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-start gap-3">
              <AlertCircle className="h-5 w-5 text-destructive shrink-0 mt-0.5" />
              <div>
                <div className="text-sm font-medium text-foreground">
                  {formatCurrency(outstandingCents, displayCurrency)} outstanding
                </div>
                <div className="text-xs text-muted-foreground mt-0.5">
                  {unpaidInvoices.length === 1
                    ? <>Invoice {nextDueInvoice?.invoice_number} · issued {formatDateRelative(nextDueInvoice?.issued_at)}</>
                    : <>{unpaidInvoices.length} unpaid invoices · oldest issued {formatDateRelative(nextDueInvoice?.issued_at)}</>}
                </div>
              </div>
            </div>
            {nextDueInvoice && (
              <Button
                size="sm"
                disabled={acting || !defaultPM}
                onClick={() => void handlePayNow(nextDueInvoice)}
                title={!defaultPM ? 'Add a card before paying' : undefined}
              >
                {unpaidInvoices.length === 1 ? 'Pay now' : 'Pay oldest'}
              </Button>
            )}
          </div>
        )}

        {/* Account summary — at-a-glance status. Mirrors the Linear
            "overview" pattern: dense, scan-friendly KPIs above the
            detailed sections. Keeps the rest of the page (billing
            details, credits, PMs, subs, invoices) for drill-down. */}
        <Card>
          <CardContent className="px-6 py-5">
            <div className="grid grid-cols-2 md:grid-cols-4 gap-6">
              <div>
                <div className="text-xs text-muted-foreground">Active subscriptions</div>
                <div className="text-xl font-semibold text-foreground mt-1 tabular-nums">
                  {activeSubsCount}
                </div>
              </div>
              <div>
                <div className="text-xs text-muted-foreground">Credit balance</div>
                <div className="text-xl font-semibold text-foreground mt-1 tabular-nums">
                  {creditBalance
                    ? formatCurrency(creditBalance.balance_cents, displayCurrency)
                    : '—'}
                </div>
              </div>
              <div>
                <div className="text-xs text-muted-foreground">Default card</div>
                <div className="text-sm font-medium text-foreground mt-1.5">
                  {defaultPM
                    ? <>{(defaultPM.card_brand || 'Card').toUpperCase()} ···· {defaultPM.card_last4}</>
                    : <span className="text-muted-foreground font-normal">None on file</span>}
                </div>
              </div>
              <div>
                <div className="text-xs text-muted-foreground">Next charge</div>
                <div className="text-sm font-medium text-foreground mt-1.5">
                  {(() => {
                    const next = subs
                      .filter(s => (s.status === 'active' || s.status === 'trialing') && s.next_billing_at)
                      .map(s => s.next_billing_at!)
                      .sort()[0]
                    return next ? formatDateRelative(next) : <span className="text-muted-foreground font-normal">—</span>
                  })()}
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Billing details — self-edit display_name + email. Narrow
            allow-list on the backend (PATCH /v1/me/profile only
            accepts these two fields); status / dunning / livemode
            stay operator-only. Industry parity: Stripe portal
            "Billing details" + Lago + Chargebee. */}
        {profile && (
          <section>
            <div className="flex items-center justify-between mb-3">
              <h2 className="text-lg font-semibold text-foreground">Billing details</h2>
              {!editingProfile && (
                <button
                  className="text-xs text-muted-foreground hover:text-foreground transition-colors"
                  onClick={() => {
                    setProfileForm({
                      display_name: profile.display_name ?? '',
                      email: profile.email ?? '',
                    })
                    setEditingProfile(true)
                  }}
                >
                  Edit
                </button>
              )}
            </div>
            <Card>
              <CardContent className="px-6 py-5 space-y-3">
                {!editingProfile ? (
                  <>
                    <div className="flex items-baseline justify-between gap-4">
                      <span className="text-xs text-muted-foreground w-24 shrink-0">Name</span>
                      <span className="text-sm text-foreground truncate">{profile.display_name || '—'}</span>
                    </div>
                    <div className="flex items-baseline justify-between gap-4">
                      <span className="text-xs text-muted-foreground w-24 shrink-0">Email</span>
                      <span className="text-sm text-foreground truncate">{profile.email || '—'}</span>
                    </div>
                  </>
                ) : (
                  <form
                    onSubmit={(e) => {
                      e.preventDefault()
                      void handleSaveProfile(profileForm.display_name.trim(), profileForm.email.trim())
                    }}
                    className="space-y-3"
                  >
                    <div className="flex items-center gap-4">
                      <label className="text-xs text-muted-foreground w-24 shrink-0">Name</label>
                      <input
                        type="text"
                        value={profileForm.display_name}
                        onChange={(e) => setProfileForm(f => ({ ...f, display_name: e.target.value }))}
                        className="flex-1 text-sm px-3 py-1.5 border border-input rounded-md bg-background"
                        placeholder="Your company or name"
                        maxLength={200}
                      />
                    </div>
                    <div className="flex items-center gap-4">
                      <label className="text-xs text-muted-foreground w-24 shrink-0">Email</label>
                      <input
                        type="email"
                        value={profileForm.email}
                        onChange={(e) => setProfileForm(f => ({ ...f, email: e.target.value }))}
                        className="flex-1 text-sm px-3 py-1.5 border border-input rounded-md bg-background"
                        placeholder="billing@…"
                        maxLength={254}
                      />
                    </div>
                    <div className="flex items-center justify-end gap-2 pt-1">
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => setEditingProfile(false)}
                        disabled={acting}
                      >
                        Cancel
                      </Button>
                      <Button
                        type="submit"
                        size="sm"
                        disabled={acting}
                      >
                        {acting ? <Loader2 size={14} className="animate-spin mr-2" /> : null}
                        Save
                      </Button>
                    </div>
                  </form>
                )}
              </CardContent>
            </Card>
          </section>
        )}

        {/* Credit balance — surfaces the customer's prepaid balance +
            collapsible recent ledger entries. Industry parity: Lago
            wallet view + Chargebee promo-credit. Hidden entirely when
            customer has zero balance AND no entries — avoids a useless
            empty card on accounts that never use credits. */}
        {creditBalance && (creditBalance.balance_cents !== 0 || creditBalance.recent_entries.length > 0) && (
          <section>
            <div className="flex items-center justify-between mb-3">
              <h2 className="text-lg font-semibold text-foreground">Credit balance</h2>
              {creditBalance.recent_entries.length > 0 && (
                <button
                  className="text-xs text-muted-foreground hover:text-foreground transition-colors"
                  onClick={() => setShowCreditHistory(v => !v)}
                >
                  {showCreditHistory ? 'Hide' : 'Show'} history
                </button>
              )}
            </div>
            <Card>
              <CardContent className="px-6 py-5">
                <div className="flex items-baseline gap-3">
                  <span className={`text-2xl font-semibold tabular-nums ${creditBalance.balance_cents < 0 ? 'text-destructive' : 'text-foreground'}`}>
                    {formatCurrency(creditBalance.balance_cents, displayCurrency)}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    available · {formatCurrency(creditBalance.total_granted, displayCurrency)} granted, {formatCurrency(creditBalance.total_used, displayCurrency)} used
                  </span>
                </div>
                {showCreditHistory && creditBalance.recent_entries.length > 0 && (
                  <div className="mt-4 pt-4 border-t border-border space-y-1.5">
                    {creditBalance.recent_entries.map(e => (
                      <div key={e.id} className="flex items-center justify-between text-xs">
                        <div className="flex items-center gap-2 min-w-0">
                          <Badge variant="outline" className="shrink-0 text-[10px]">{e.entry_type}</Badge>
                          <span className="text-muted-foreground truncate">{e.description || '—'}</span>
                        </div>
                        <div className="flex items-center gap-3 shrink-0">
                          <span className="text-muted-foreground text-[11px]">{formatDate(e.created_at)}</span>
                          <span className={`tabular-nums font-mono ${e.amount_cents < 0 ? 'text-destructive' : 'text-emerald-600'}`}>
                            {e.amount_cents > 0 ? '+' : ''}{formatCurrency(e.amount_cents, displayCurrency)}
                          </span>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          </section>
        )}

        {/* Payment method — render-aware: card details when present,
            empty-state CTA when none. Industry parity: Stripe portal +
            Chargebee self-serve both render brand + last4 + exp on the
            card row and offer Add/Update in the same affordance. */}
        <section>
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-lg font-semibold text-foreground">Payment methods</h2>
          </div>
          {paymentMethods.length === 0 ? (
            <Card>
              <CardContent className="px-6 py-5 flex items-center justify-between gap-4">
                <div className="flex items-center gap-3 min-w-0">
                  <div className="h-9 w-9 rounded-lg bg-muted flex items-center justify-center shrink-0">
                    <CreditCard size={18} className="text-muted-foreground" />
                  </div>
                  <div className="min-w-0">
                    <p className="text-sm font-medium text-foreground">No payment method on file</p>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      Add one to autopay invoices.
                    </p>
                  </div>
                </div>
                <Button onClick={handleUpdatePM} disabled={acting}>
                  {acting ? <Loader2 size={14} className="animate-spin mr-2" /> : null}
                  Add payment method
                </Button>
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-2">
              {paymentMethods.map(pm => (
                <Card key={pm.id || `${pm.card_brand}-${pm.card_last4}`}>
                  <CardContent className="px-6 py-4 flex items-center justify-between gap-4">
                    <div className="flex items-center gap-3 min-w-0">
                      <div className="h-9 w-9 rounded-lg bg-violet-500/10 flex items-center justify-center shrink-0">
                        <CreditCard size={18} className="text-violet-500" />
                      </div>
                      <div className="min-w-0">
                        <div className="flex items-center gap-2 flex-wrap">
                          <p className="text-sm font-medium text-foreground">
                            {formatCardBrand(pm.card_brand)} ending in {pm.card_last4 || '••••'}
                          </p>
                          {pm.is_default && (
                            <Badge variant="outline" className="text-[10px]">Default</Badge>
                          )}
                        </div>
                        <p className="text-xs text-muted-foreground mt-0.5">
                          {pm.card_exp_month && pm.card_exp_year
                            ? `Expires ${formatCardExp(pm.card_exp_month, pm.card_exp_year)}`
                            : 'Card on file'}
                        </p>
                      </div>
                    </div>
                    <div className="flex items-center gap-2 shrink-0">
                      {!pm.is_default && (
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => void handleSetDefaultPM(pm)}
                          disabled={acting}
                        >
                          Set default
                        </Button>
                      )}
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-destructive hover:text-destructive"
                            disabled={acting}
                          >
                            Remove
                          </Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>Remove this payment method?</AlertDialogTitle>
                            <AlertDialogDescription>
                              {paymentMethods.length === 1
                                ? `${formatCardBrand(pm.card_brand)} ending in ${pm.card_last4 || '••••'} will be detached. After this you'll have no payment method on file — future invoices won't be charged automatically, and you'll need to add a new card or pay manually from this portal.`
                                : `${formatCardBrand(pm.card_brand)} ending in ${pm.card_last4 || '••••'} will be detached. Future invoices will fall back to ${pm.is_default ? 'the next available payment method' : 'your default card'} on next charge.`}
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>Cancel</AlertDialogCancel>
                            <AlertDialogAction
                              onClick={() => void handleRemovePM(pm)}
                              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                            >
                              Remove
                            </AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    </div>
                  </CardContent>
                </Card>
              ))}
              <Button
                variant="outline"
                size="sm"
                onClick={handleUpdatePM}
                disabled={acting}
                className="w-full"
              >
                {acting ? <Loader2 size={14} className="animate-spin mr-2" /> : null}
                Add payment method
              </Button>
            </div>
          )}
        </section>

        {/* Subscriptions */}
        <section>
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-lg font-semibold text-foreground">Subscriptions</h2>
            <span className="text-xs text-muted-foreground">{subs.length} total</span>
          </div>
          {subs.length === 0 ? (
            <Card>
              <CardContent className="p-8 text-center text-sm text-muted-foreground">
                No subscriptions on this account.
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-2">
              {subs.map(s => {
                const scheduledCancel = !!s.cancel_at_period_end && s.status !== 'canceled' && s.status !== 'archived'
                return (
                <Card key={s.id}>
                  <CardContent className="px-6 py-4 flex items-center justify-between gap-4">
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 flex-wrap">
                        <p className="text-sm font-medium text-foreground truncate">
                          {s.display_name || s.code}
                        </p>
                        <Badge variant={statusToneSub(s.status)}>{s.status}</Badge>
                        {scheduledCancel && (
                          <Badge variant="outline" className="text-amber-600 border-amber-600/50">
                            cancels at period end
                          </Badge>
                        )}
                      </div>
                      <p className="text-xs text-muted-foreground mt-0.5">
                        {s.status === 'canceled' || s.status === 'archived'
                          ? s.current_billing_period_end
                            ? <>Ended {formatDate(s.current_billing_period_end)}</>
                            : 'Canceled'
                          : scheduledCancel && s.current_billing_period_end
                            ? <>Will end {formatDateRelative(s.current_billing_period_end)} · won't renew</>
                            : s.next_billing_at
                              ? <>Next bill {formatDateRelative(s.next_billing_at)}</>
                              : s.current_billing_period_end
                                ? <>Period ends {formatDateRelative(s.current_billing_period_end)}</>
                                : '—'}
                      </p>
                    </div>
                    {s.status !== 'canceled' && s.status !== 'archived' && (
                      scheduledCancel ? (
                        <Button
                          variant="outline"
                          size="sm"
                          className="shrink-0"
                          onClick={() => void handleResume(s)}
                          disabled={acting}
                        >
                          {acting ? <Loader2 size={14} className="animate-spin mr-2" /> : null}
                          Resume
                        </Button>
                      ) : (
                        <Button
                          variant="outline"
                          size="sm"
                          className="text-destructive hover:text-destructive shrink-0"
                          onClick={() => setCancelTarget(s)}
                        >
                          Cancel
                        </Button>
                      )
                    )}
                  </CardContent>
                </Card>
                )
              })}
            </div>
          )}
        </section>

        {/* Invoices */}
        <section>
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-lg font-semibold text-foreground">Invoices</h2>
            <span className="text-xs text-muted-foreground">{invoices.length} total</span>
          </div>
          {invoices.length === 0 ? (
            <Card>
              <CardContent className="p-8 text-center text-sm text-muted-foreground">
                No invoices yet.
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-2">
              {invoices.map(inv => (
                <Card key={inv.id}>
                  <CardContent className="px-6 py-3 flex items-center gap-4">
                    <div className="h-9 w-9 rounded-lg bg-muted flex items-center justify-center shrink-0">
                      <FileText size={16} className="text-muted-foreground" />
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 flex-wrap">
                        <p className="text-sm font-medium text-foreground">{inv.invoice_number}</p>
                        <Badge variant={statusToneInvoice(inv.status)}>{inv.status}</Badge>
                        {inv.status === 'finalized' && inv.payment_status !== 'paid' && (
                          <Badge variant="outline">{inv.payment_status}</Badge>
                        )}
                      </div>
                      <p className="text-xs text-muted-foreground mt-0.5">
                        {formatDate(inv.issued_at)}
                        {inv.paid_at && <> · paid {formatDate(inv.paid_at)}</>}
                        {(inv.credits_applied_cents ?? 0) > 0 && (
                          <span className="ml-1 italic opacity-80">
                            · {formatCurrency(inv.credits_applied_cents ?? 0, inv.currency)} from credits
                          </span>
                        )}
                      </p>
                    </div>
                    <div className="text-right shrink-0">
                      <div className="text-sm font-medium text-foreground tabular-nums">
                        {formatCurrency(inv.total_amount_cents, inv.currency)}
                      </div>
                      {(inv.credits_applied_cents ?? 0) > 0 && inv.amount_paid_cents !== inv.total_amount_cents && (
                        <div className="text-[11px] text-muted-foreground tabular-nums mt-0.5">
                          {formatCurrency(inv.amount_paid_cents, inv.currency)} card
                        </div>
                      )}
                    </div>
                    {/* Pay-now appears only for finalized-and-unpaid
                        invoices. status=paid hides it (already
                        settled); status=voided/draft hides it (not
                        payable). The button is disabled while a
                        charge is in flight (acting OR
                        payment_status=processing). */}
                    {inv.status === 'finalized' && inv.payment_status !== 'succeeded' && (
                      <Button
                        variant="default"
                        size="sm"
                        className="shrink-0"
                        onClick={() => void handlePayNow(inv)}
                        disabled={acting || inv.payment_status === 'processing'}
                      >
                        {acting ? <Loader2 size={14} className="animate-spin mr-1.5" /> : null}
                        {inv.payment_status === 'processing' ? 'Processing…' : 'Pay now'}
                      </Button>
                    )}
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="shrink-0"
                          onClick={() => void downloadInvoicePDF(inv)}
                        >
                          <Download size={14} />
                        </Button>
                      </TooltipTrigger>
                      <TooltipContent>Download PDF</TooltipContent>
                    </Tooltip>
                  </CardContent>
                </Card>
              ))}
            </div>
          )}
        </section>
      </main>

      <AlertDialog open={!!cancelTarget} onOpenChange={open => { if (!open) setCancelTarget(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Cancel subscription?</AlertDialogTitle>
            <AlertDialogDescription>
              {cancelTarget
                ? `"${cancelTarget.display_name || cancelTarget.code}" will be canceled immediately. You won't be charged for future billing periods.`
                : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={acting}>Keep subscription</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleCancel}
              disabled={acting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {acting ? 'Canceling…' : 'Cancel subscription'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
