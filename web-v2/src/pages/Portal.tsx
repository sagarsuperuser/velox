import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { toast } from 'sonner'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
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
  issued_at?: string
  paid_at?: string
  public_token?: string
}

interface PortalBranding {
  company_name?: string
  brand_color?: string
  support_url?: string
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

export default function Portal() {
  const navigate = useNavigate()
  const [subs, setSubs] = useState<PortalSub[]>([])
  const [invoices, setInvoices] = useState<PortalInvoice[]>([])
  const [branding, setBranding] = useState<PortalBranding>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [cancelTarget, setCancelTarget] = useState<PortalSub | null>(null)
  const [acting, setActing] = useState(false)

  useEffect(() => {
    if (!getPortalSession()) {
      navigate('/portal/login', { replace: true })
      return
    }
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const load = async () => {
    setLoading(true)
    setError('')
    try {
      const [subRes, invRes, brandRes] = await Promise.all([
        portalFetch<{ data: PortalSub[]; total: number }>('GET', '/v1/me/subscriptions'),
        portalFetch<{ data: PortalInvoice[]; total: number }>('GET', '/v1/me/invoices'),
        portalFetch<PortalBranding>('GET', '/v1/me/branding'),
      ])
      setSubs(subRes.data ?? [])
      setInvoices(invRes.data ?? [])
      setBranding(brandRes ?? {})
    } catch (err) {
      if (err instanceof PortalAuthError) {
        navigate('/portal/login', { replace: true })
        return
      }
      setError(err instanceof Error ? err.message : 'Failed to load portal')
    } finally {
      setLoading(false)
    }
  }

  const handleCancel = async () => {
    if (!cancelTarget) return
    setActing(true)
    try {
      await portalFetch('POST', `/v1/me/subscriptions/${cancelTarget.id}/cancel`)
      toast.success('Subscription canceled')
      setCancelTarget(null)
      await load()
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Cancel failed'
      toast.error(msg)
    } finally {
      setActing(false)
    }
  }

  const handleUpdatePM = async () => {
    setActing(true)
    try {
      const res = await portalFetch<{ url: string }>(
        'POST',
        '/v1/me/payment-method/update',
        { return_url: window.location.href },
      )
      // Open Stripe Checkout in a new tab so the customer can return
      // here on cancel/success without losing portal context.
      window.open(res.url, '_blank', 'noopener,noreferrer')
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Could not start payment-method update'
      toast.error(msg)
    } finally {
      setActing(false)
    }
  }

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
    return (
      <div className="min-h-screen flex items-center justify-center">
        <Loader2 className="animate-spin h-8 w-8 text-muted-foreground" />
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
            <Button variant="outline" onClick={() => void load()}>Retry</Button>
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
          <div className="flex items-center gap-3">
            <VeloxLogo size="sm" />
            <span className="text-sm text-muted-foreground hidden sm:inline">
              {branding.company_name || 'Billing portal'}
            </span>
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
        {/* Payment method */}
        <section>
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-lg font-semibold text-foreground">Payment method</h2>
          </div>
          <Card>
            <CardContent className="px-6 py-5 flex items-center justify-between gap-4">
              <div className="flex items-center gap-3">
                <div className="h-9 w-9 rounded-lg bg-violet-500/10 flex items-center justify-center">
                  <CreditCard size={18} className="text-violet-500" />
                </div>
                <div>
                  <p className="text-sm font-medium text-foreground">Update payment method</p>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    We'll open a secure Stripe page to collect a new card.
                  </p>
                </div>
              </div>
              <Button onClick={handleUpdatePM} disabled={acting}>
                {acting ? <Loader2 size={14} className="animate-spin mr-2" /> : null}
                Update card
              </Button>
            </CardContent>
          </Card>
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
              {subs.map(s => (
                <Card key={s.id}>
                  <CardContent className="px-6 py-4 flex items-center justify-between gap-4">
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <p className="text-sm font-medium text-foreground truncate">
                          {s.display_name || s.code}
                        </p>
                        <Badge variant={statusToneSub(s.status)}>{s.status}</Badge>
                      </div>
                      <p className="text-xs text-muted-foreground mt-0.5">
                        {s.next_billing_at
                          ? <>Next bill on {formatDate(s.next_billing_at)}</>
                          : s.current_billing_period_end
                            ? <>Period ends {formatDate(s.current_billing_period_end)}</>
                            : '—'}
                      </p>
                    </div>
                    {s.status !== 'canceled' && s.status !== 'archived' && (
                      <Button
                        variant="outline"
                        size="sm"
                        className="text-destructive hover:text-destructive shrink-0"
                        onClick={() => setCancelTarget(s)}
                      >
                        Cancel
                      </Button>
                    )}
                  </CardContent>
                </Card>
              ))}
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
                      </p>
                    </div>
                    <div className="text-sm font-medium text-foreground tabular-nums shrink-0">
                      {formatCurrency(inv.total_amount_cents, inv.currency)}
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="shrink-0"
                      onClick={() => void downloadInvoicePDF(inv)}
                      title="Download PDF"
                    >
                      <Download size={14} />
                    </Button>
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
