import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'

import {
  CreditCard,
  Plus,
  Trash2,
  CheckCircle2,
  ShieldCheck,
  Loader2,
  Clock,
  ExternalLink,
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

interface ListResponse {
  data: PaymentMethod[]
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

export default function CustomerPortalPage() {
  const [searchParams] = useSearchParams()
  const token = searchParams.get('token') || ''
  const api = useApi(token)

  const [pms, setPms] = useState<PaymentMethod[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [busyID, setBusyID] = useState('')
  const [addingCard, setAddingCard] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await api<ListResponse>('/v1/me/payment-methods')
      setPms(res.data || [])
      setError('')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load payment methods')
    } finally {
      setLoading(false)
    }
  }, [api])

  useEffect(() => {
    if (!token) {
      setError('No portal session token provided')
      setLoading(false)
      return
    }
    load()
  }, [token, load])

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
      toast.error(err instanceof Error ? err.message : 'Could not start card setup')
      setAddingCard(false)
    }
  }

  const handleSetDefault = async (pm: PaymentMethod) => {
    if (pm.is_default) return
    setBusyID(pm.id)
    try {
      await api(`/v1/me/payment-methods/${encodeURIComponent(pm.id)}/default`, { method: 'POST' })
      toast.success('Default payment method updated')
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Could not set default')
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
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Could not remove card')
    } finally {
      setBusyID('')
    }
  }

  return (
    <div className="min-h-screen bg-background flex items-center justify-center p-4">
      <div className="w-full max-w-lg">
        <div className="text-center mb-8">
          <h1 className="text-2xl font-bold text-foreground">Velox</h1>
          <p className="text-sm text-muted-foreground mt-1">Payment Methods</p>
        </div>

        <Card className="overflow-hidden">
          <CardContent className="p-0">
            {loading ? (
              <div className="p-12 text-center">
                <Loader2 className="h-8 w-8 animate-spin text-primary mx-auto" />
                <p className="text-sm text-muted-foreground mt-4">Loading your cards...</p>
              </div>
            ) : error ? (
              <div className="p-8 text-center">
                <div className="w-12 h-12 rounded-full bg-destructive/10 flex items-center justify-center mx-auto mb-4">
                  <Clock size={24} className="text-destructive/60" />
                </div>
                <p className="text-sm font-medium text-foreground">Session expired or invalid</p>
                <p className="text-sm text-muted-foreground mt-2">{error}</p>
                <p className="text-xs text-muted-foreground mt-4">
                  Please contact your billing provider for a new link.
                </p>
              </div>
            ) : (
              <div className="p-6 space-y-6">
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

                <Button
                  onClick={handleAddCard}
                  disabled={addingCard}
                  className="w-full"
                  size="lg"
                >
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
              </div>
            )}
          </CardContent>
        </Card>

        <p className="text-xs text-muted-foreground text-center mt-6">Powered by Velox Billing</p>
      </div>
    </div>
  )
}
