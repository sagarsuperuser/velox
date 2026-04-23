import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { showApiError } from '@/lib/formErrors'
import { formatCents, formatDate } from '@/lib/api'
import { statusBadgeVariant } from '@/lib/status'
import { TypedConfirmDialog } from '@/components/TypedConfirmDialog'

import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import {
  CreditCard,
  Plus,
  Trash2,
  CheckCircle2,
  ShieldCheck,
  Loader2,
  Clock,
  ExternalLink,
  FileText,
  Download,
  Layers,
  HelpCircle,
} from 'lucide-react'

interface PaymentMethod {
  id: string
  type: string
  card_brand?: string
  card_last4?: string
  card_exp_month?: number
  card_exp_year?: number
  is_default: boolean
  created_at: string
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
  due_at?: string
  paid_at?: string
}

interface PortalSubscription {
  id: string
  display_name: string
  status: string
  items: { id: string; plan_id: string; quantity: number }[]
  current_period_start?: string
  current_period_end?: string
  next_billing_at?: string
  trial_end_at?: string
  canceled_at?: string
  started_at?: string
}

interface Branding {
  company_name?: string
  logo_url?: string
  support_url?: string
}

interface ListResponse<T> {
  data: T[]
}

interface SetupSessionResponse {
  url: string
  session_id: string
}

const apiBase = import.meta.env.VITE_API_URL || ''

function useApi(token: string) {
  return useCallback(
    async <T,>(path: string, init?: RequestInit): Promise<T> => {
      const res = await fetch(`${apiBase}${path}`, {
        ...init,
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${token}`,
          ...(init?.headers || {}),
        },
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        throw new Error(body?.error?.message || `Request failed (${res.status})`)
      }
      if (res.status === 204) return undefined as T
      return (await res.json()) as T
    },
    [token]
  )
}

function formatExp(mo?: number, yr?: number) {
  if (!mo || !yr) return ''
  return `${String(mo).padStart(2, '0')}/${String(yr).slice(-2)}`
}

function brandLabel(b?: string) {
  if (!b) return 'Card'
  return b.charAt(0).toUpperCase() + b.slice(1)
}

function prettyStatus(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1).replaceAll('_', ' ')
}

export default function CustomerPortalPage() {
  const [searchParams] = useSearchParams()
  const token = searchParams.get('token') || ''
  const api = useApi(token)

  const [branding, setBranding] = useState<Branding>({})
  const [pms, setPms] = useState<PaymentMethod[]>([])
  const [invoices, setInvoices] = useState<PortalInvoice[]>([])
  const [subs, setSubs] = useState<PortalSubscription[]>([])

  const [loading, setLoading] = useState(true)
  const [sessionError, setSessionError] = useState('')

  const [busyID, setBusyID] = useState('')
  const [pdfBusyID, setPdfBusyID] = useState('')
  const [addingCard, setAddingCard] = useState(false)

  const [cancelTarget, setCancelTarget] = useState<PortalSubscription | null>(null)
  const [cancelBusy, setCancelBusy] = useState(false)

  const loadAll = useCallback(async () => {
    try {
      const [b, pmRes, invRes, subRes] = await Promise.all([
        api<Branding>('/v1/me/branding').catch(() => ({})),
        api<ListResponse<PaymentMethod>>('/v1/me/payment-methods'),
        api<ListResponse<PortalInvoice>>('/v1/me/invoices').catch(() => ({ data: [] })),
        api<ListResponse<PortalSubscription>>('/v1/me/subscriptions').catch(() => ({ data: [] })),
      ])
      setBranding(b)
      setPms(pmRes.data || [])
      setInvoices(invRes.data || [])
      setSubs(subRes.data || [])
      setSessionError('')
    } catch (err) {
      setSessionError(err instanceof Error ? err.message : 'Failed to load portal data')
    } finally {
      setLoading(false)
    }
  }, [api])

  useEffect(() => {
    if (!token) {
      setSessionError('No portal session token provided')
      setLoading(false)
      return
    }
    loadAll()
  }, [token, loadAll])

  const handleAddCard = async () => {
    setAddingCard(true)
    try {
      const returnURL = window.location.href
      const res = await api<SetupSessionResponse>('/v1/me/payment-methods/setup-session', {
        method: 'POST',
        body: JSON.stringify({ return_url: returnURL }),
      })
      window.location.href = res.url
    } catch (err) {
      showApiError(err, 'Could not start card setup')
      setAddingCard(false)
    }
  }

  const handleSetDefault = async (pm: PaymentMethod) => {
    if (pm.is_default) return
    setBusyID(pm.id)
    try {
      await api(`/v1/me/payment-methods/${encodeURIComponent(pm.id)}/default`, { method: 'POST' })
      toast.success('Default payment method updated')
      await loadAll()
    } catch (err) {
      showApiError(err, 'Could not set default')
    } finally {
      setBusyID('')
    }
  }

  const handleRemove = async (pm: PaymentMethod) => {
    if (!window.confirm(`Remove ${brandLabel(pm.card_brand)} ending ${pm.card_last4}?`)) return
    setBusyID(pm.id)
    try {
      await api(`/v1/me/payment-methods/${encodeURIComponent(pm.id)}`, { method: 'DELETE' })
      toast.success('Card removed')
      await loadAll()
    } catch (err) {
      showApiError(err, 'Could not remove card')
    } finally {
      setBusyID('')
    }
  }

  // PDF download: the endpoint is bearer-auth protected, so we can't use an
  // <a href> — we fetch as a blob and trigger a download from a temp URL.
  const handleDownloadPDF = async (inv: PortalInvoice) => {
    setPdfBusyID(inv.id)
    try {
      const res = await fetch(`${apiBase}/v1/me/invoices/${encodeURIComponent(inv.id)}/pdf`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        throw new Error(body?.error?.message || `Download failed (${res.status})`)
      }
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${inv.invoice_number}.pdf`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch (err) {
      showApiError(err, 'Could not download invoice')
    } finally {
      setPdfBusyID('')
    }
  }

  const handleCancelConfirm = async () => {
    if (!cancelTarget) return
    setCancelBusy(true)
    try {
      await api(`/v1/me/subscriptions/${encodeURIComponent(cancelTarget.id)}/cancel`, {
        method: 'POST',
      })
      toast.success('Subscription canceled')
      setCancelTarget(null)
      await loadAll()
    } catch (err) {
      showApiError(err, 'Could not cancel subscription')
    } finally {
      setCancelBusy(false)
    }
  }

  const companyName = branding.company_name || 'Velox'
  const logoURL = branding.logo_url || ''
  const supportURL = branding.support_url || ''

  const activeSubCount = useMemo(
    () => subs.filter(s => s.status === 'active' || s.status === 'trialing' || s.status === 'paused').length,
    [subs]
  )

  if (loading) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center p-4">
        <Loader2 className="h-8 w-8 animate-spin text-primary" />
      </div>
    )
  }

  if (sessionError) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center p-4">
        <Card className="w-full max-w-md">
          <CardContent className="p-8 text-center">
            <div className="w-12 h-12 rounded-full bg-destructive/10 flex items-center justify-center mx-auto mb-4">
              <Clock size={24} className="text-destructive/60" />
            </div>
            <p className="text-sm font-medium text-foreground">Session expired or invalid</p>
            <p className="text-sm text-muted-foreground mt-2">{sessionError}</p>
            <p className="text-xs text-muted-foreground mt-4">
              Please contact your billing provider for a new link.
            </p>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-background">
      <header className="border-b border-border bg-card">
        <div className="max-w-4xl mx-auto px-4 py-4 flex items-center justify-between">
          <div className="flex items-center gap-3">
            {logoURL ? (
              <img
                src={logoURL}
                alt={companyName}
                className="h-8 w-auto max-w-[160px] object-contain"
                onError={e => ((e.currentTarget as HTMLImageElement).style.display = 'none')}
              />
            ) : (
              <div className="h-8 w-8 rounded-md bg-primary/10 flex items-center justify-center">
                <CreditCard size={16} className="text-primary" />
              </div>
            )}
            <div>
              <p className="text-sm font-semibold text-foreground">{companyName}</p>
              <p className="text-xs text-muted-foreground">Customer Portal</p>
            </div>
          </div>
          {supportURL && (
            <a
              href={supportURL}
              target="_blank"
              rel="noopener noreferrer"
              className="text-xs text-muted-foreground hover:text-foreground flex items-center gap-1"
            >
              <HelpCircle size={14} />
              Support
            </a>
          )}
        </div>
      </header>

      <main className="max-w-4xl mx-auto px-4 py-6">
        <Tabs defaultValue="invoices" className="w-full">
          <TabsList className="mb-6">
            <TabsTrigger value="invoices">
              <FileText size={14} />
              Invoices
              {invoices.length > 0 && (
                <span className="ml-1.5 text-[10px] text-muted-foreground">({invoices.length})</span>
              )}
            </TabsTrigger>
            <TabsTrigger value="subscriptions">
              <Layers size={14} />
              Subscriptions
              {activeSubCount > 0 && (
                <span className="ml-1.5 text-[10px] text-muted-foreground">({activeSubCount})</span>
              )}
            </TabsTrigger>
            <TabsTrigger value="payment-methods">
              <CreditCard size={14} />
              Payment Methods
            </TabsTrigger>
          </TabsList>

          <TabsContent value="invoices">
            <Card>
              <CardContent className="p-0">
                {invoices.length === 0 ? (
                  <div className="p-10 text-center">
                    <div className="w-12 h-12 rounded-full bg-muted flex items-center justify-center mx-auto mb-3">
                      <FileText size={22} className="text-muted-foreground" />
                    </div>
                    <p className="text-sm font-medium text-foreground">No invoices yet</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      Your invoices will appear here once they're issued.
                    </p>
                  </div>
                ) : (
                  <div className="divide-y divide-border">
                    {invoices.map(inv => (
                      <div
                        key={inv.id}
                        className="flex items-center gap-4 p-4 hover:bg-muted/40 transition-colors"
                      >
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-2">
                            <p className="text-sm font-medium text-foreground truncate">
                              {inv.invoice_number}
                            </p>
                            <Badge variant={statusBadgeVariant(inv.payment_status)} className="text-[10px] h-5">
                              {prettyStatus(inv.payment_status)}
                            </Badge>
                          </div>
                          <p className="text-xs text-muted-foreground mt-0.5">
                            {inv.issued_at ? `Issued ${formatDate(inv.issued_at)}` : 'Issued —'}
                            {inv.due_at ? ` · Due ${formatDate(inv.due_at)}` : ''}
                          </p>
                        </div>
                        <div className="text-right shrink-0">
                          <p className="text-sm font-semibold text-foreground">
                            {formatCents(inv.total_amount_cents, inv.currency)}
                          </p>
                          {inv.amount_due_cents > 0 && (
                            <p className="text-xs text-amber-600 dark:text-amber-400">
                              {formatCents(inv.amount_due_cents, inv.currency)} due
                            </p>
                          )}
                        </div>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => handleDownloadPDF(inv)}
                          disabled={pdfBusyID === inv.id}
                          title="Download PDF"
                        >
                          {pdfBusyID === inv.id ? (
                            <Loader2 size={14} className="animate-spin" />
                          ) : (
                            <Download size={14} />
                          )}
                          <span className="ml-1 text-xs hidden sm:inline">PDF</span>
                        </Button>
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="subscriptions">
            <Card>
              <CardContent className="p-0">
                {subs.length === 0 ? (
                  <div className="p-10 text-center">
                    <div className="w-12 h-12 rounded-full bg-muted flex items-center justify-center mx-auto mb-3">
                      <Layers size={22} className="text-muted-foreground" />
                    </div>
                    <p className="text-sm font-medium text-foreground">No subscriptions</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      You don't have any active subscriptions.
                    </p>
                  </div>
                ) : (
                  <div className="divide-y divide-border">
                    {subs.map(sub => {
                      const cancelable =
                        sub.status === 'active' ||
                        sub.status === 'trialing' ||
                        sub.status === 'paused'
                      return (
                        <div key={sub.id} className="p-4">
                          <div className="flex items-start justify-between gap-4">
                            <div className="flex-1 min-w-0">
                              <div className="flex items-center gap-2">
                                <p className="text-sm font-medium text-foreground truncate">
                                  {sub.display_name || 'Subscription'}
                                </p>
                                <Badge variant={statusBadgeVariant(sub.status)} className="text-[10px] h-5">
                                  {prettyStatus(sub.status)}
                                </Badge>
                              </div>
                              <p className="text-xs text-muted-foreground mt-1">
                                {sub.items.length} {sub.items.length === 1 ? 'item' : 'items'}
                                {sub.current_period_end
                                  ? ` · Renews ${formatDate(sub.current_period_end)}`
                                  : ''}
                                {sub.canceled_at
                                  ? ` · Canceled ${formatDate(sub.canceled_at)}`
                                  : ''}
                              </p>
                            </div>
                            {cancelable && (
                              <Button
                                variant="outline"
                                size="sm"
                                onClick={() => setCancelTarget(sub)}
                                className="shrink-0 text-destructive hover:text-destructive"
                              >
                                Cancel
                              </Button>
                            )}
                          </div>
                        </div>
                      )
                    })}
                  </div>
                )}
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="payment-methods">
            <Card>
              <CardContent className="p-6 space-y-6">
                {pms.length === 0 ? (
                  <div className="text-center py-6">
                    <div className="w-12 h-12 rounded-full bg-muted flex items-center justify-center mx-auto mb-3">
                      <CreditCard size={22} className="text-muted-foreground" />
                    </div>
                    <p className="text-sm font-medium text-foreground">No payment methods yet</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      Add a card to keep your subscription active.
                    </p>
                  </div>
                ) : (
                  <div className="space-y-2">
                    {pms.map(pm => (
                      <div
                        key={pm.id}
                        className="flex items-center gap-3 p-3 rounded-lg border border-border bg-card"
                      >
                        <div className="w-10 h-10 rounded-md bg-muted flex items-center justify-center shrink-0">
                          <CreditCard size={18} className="text-muted-foreground" />
                        </div>
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-2">
                            <p className="text-sm font-medium text-foreground truncate">
                              {brandLabel(pm.card_brand)} ···· {pm.card_last4 || '????'}
                            </p>
                            {pm.is_default && (
                              <Badge variant="secondary" className="text-[10px] h-5 px-1.5">
                                Default
                              </Badge>
                            )}
                          </div>
                          <p className="text-xs text-muted-foreground mt-0.5">
                            Expires {formatExp(pm.card_exp_month, pm.card_exp_year)}
                          </p>
                        </div>
                        <div className="flex items-center gap-1 shrink-0">
                          {!pm.is_default && (
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => handleSetDefault(pm)}
                              disabled={busyID === pm.id}
                              title="Make this card default"
                            >
                              {busyID === pm.id ? (
                                <Loader2 size={14} className="animate-spin" />
                              ) : (
                                <CheckCircle2 size={14} />
                              )}
                              <span className="ml-1 text-xs">Set default</span>
                            </Button>
                          )}
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => handleRemove(pm)}
                            disabled={busyID === pm.id}
                            title="Remove this card"
                            className="text-destructive hover:text-destructive"
                          >
                            <Trash2 size={14} />
                          </Button>
                        </div>
                      </div>
                    ))}
                  </div>
                )}

                <Button onClick={handleAddCard} disabled={addingCard} className="w-full" size="lg">
                  {addingCard ? (
                    <>
                      <Loader2 size={16} className="mr-2 animate-spin" />
                      Redirecting to Stripe...
                    </>
                  ) : (
                    <>
                      <Plus size={16} className="mr-2" />
                      Add a new card
                      <ExternalLink size={14} className="ml-2 opacity-50" />
                    </>
                  )}
                </Button>

                <div className="flex items-center justify-center gap-1.5 text-xs text-muted-foreground">
                  <ShieldCheck size={12} />
                  <span>Secured by Stripe. Card details are never stored on our servers.</span>
                </div>
              </CardContent>
            </Card>
          </TabsContent>
        </Tabs>

        <p className="text-xs text-muted-foreground text-center mt-8">Powered by Velox Billing</p>
      </main>

      <TypedConfirmDialog
        open={cancelTarget !== null}
        onOpenChange={open => !open && setCancelTarget(null)}
        title="Cancel Subscription"
        description={
          cancelTarget
            ? `Canceling ends ${cancelTarget.display_name || 'this subscription'} immediately. You will not be charged again. This action cannot be undone from the portal.`
            : ''
        }
        confirmWord="CANCEL"
        confirmLabel="Cancel Subscription"
        onConfirm={handleCancelConfirm}
        loading={cancelBusy}
      />
    </div>
  )
}
