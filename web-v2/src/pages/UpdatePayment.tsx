import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { formatCents } from '@/lib/api'

import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'

import { CreditCard, AlertTriangle, ExternalLink, ShieldCheck, Clock, Loader2 } from 'lucide-react'

interface TokenData {
  customer_name: string
  invoice_number: string
  amount_due_cents: number
  currency: string
}

export default function UpdatePaymentPage() {
  const [searchParams] = useSearchParams()
  const token = searchParams.get('token') || ''

  const [data, setData] = useState<TokenData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [redirecting, setRedirecting] = useState(false)
  // submitError surfaces failures from the POST /checkout call inline
  // on the page (Stripe not connected, payment setup missing, etc.) —
  // distinct from `error` which is the "link is invalid" terminal
  // state. We keep the rest of the UI rendered so the customer can see
  // the invoice context that the error refers to and retry.
  const [submitError, setSubmitError] = useState('')

  useEffect(() => {
    if (!token) {
      setError('No payment update token provided')
      setLoading(false)
      return
    }

    const apiBase = import.meta.env.VITE_API_URL || ''
    fetch(`${apiBase}/v1/public/payment-updates/${encodeURIComponent(token)}`)
      .then(async res => {
        if (!res.ok) {
          const body = await res.json().catch(() => ({}))
          throw new Error(body?.error?.message || 'This link has expired or is invalid')
        }
        return res.json()
      })
      .then(d => { setData(d); setLoading(false) })
      .catch(err => { setError(err.message || 'This link has expired or is invalid'); setLoading(false) })
  }, [token])

  // Update Payment Method click. The validate endpoint above only
  // resolves invoice context — it does not mint a Stripe Checkout
  // Session (those are billable + single-use, so the server defers
  // creation until the customer actually clicks). On click we POST
  // to /checkout, the server creates the session via the tenant's
  // connected Stripe account, and we redirect the browser to the
  // returned URL. Errors (Stripe not connected for this mode,
  // missing payment setup, etc.) surface inline so the customer
  // sees a specific reason instead of a dead button.
  const handleUpdate = async () => {
    if (!token || redirecting) return
    setSubmitError('')
    setRedirecting(true)
    try {
      const apiBase = import.meta.env.VITE_API_URL || ''
      const res = await fetch(
        `${apiBase}/v1/public/payment-updates/${encodeURIComponent(token)}/checkout`,
        { method: 'POST' },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        throw new Error(body?.error?.message || 'We could not start the payment update. Please try again or contact your billing administrator.')
      }
      const { url } = await res.json() as { url?: string }
      if (!url) throw new Error('Payment update could not be started — no checkout URL returned.')
      window.location.href = url
    } catch (err) {
      setSubmitError(err instanceof Error ? err.message : 'Unexpected error starting the payment update.')
      setRedirecting(false)
    }
  }

  return (
    <div className="min-h-screen bg-background flex items-center justify-center p-4">
      <div className="w-full max-w-md">
        {/* Brand header */}
        <div className="text-center mb-8">
          <h1 className="text-2xl font-bold text-foreground">Velox</h1>
          <p className="text-sm text-muted-foreground mt-1">Secure Payment Update</p>
        </div>

        <Card className="overflow-hidden">
          <CardContent className="p-0">
            {loading ? (
              <div className="p-12 text-center">
                <Loader2 className="h-8 w-8 animate-spin text-primary mx-auto" />
                <p className="text-sm text-muted-foreground mt-4">Verifying your link...</p>
              </div>
            ) : error ? (
              <div className="p-8 text-center">
                <div className="w-12 h-12 rounded-full bg-destructive/10 flex items-center justify-center mx-auto mb-4">
                  <Clock size={24} className="text-destructive/60" />
                </div>
                <p className="text-sm font-medium text-foreground">Link expired or invalid</p>
                <p className="text-sm text-muted-foreground mt-2">{error}</p>
                <p className="text-xs text-muted-foreground mt-4">Please contact your billing provider for a new link.</p>
              </div>
            ) : data ? (
              <>
                {/* Alert banner */}
                <div className="bg-amber-50 dark:bg-amber-500/10 px-6 py-4 border-b border-amber-100 dark:border-amber-500/20">
                  <div className="flex items-start gap-3">
                    <AlertTriangle size={18} className="text-amber-500 mt-0.5 shrink-0" />
                    <div>
                      <p className="text-sm font-medium text-amber-800 dark:text-amber-400">Payment method update required</p>
                      <p className="text-xs text-amber-600 dark:text-amber-500 mt-1">
                        We were unable to process your payment. Please update your card to avoid service interruption.
                      </p>
                    </div>
                  </div>
                </div>

                {/* Invoice details */}
                <div className="p-6 space-y-4">
                  <div>
                    <p className="text-xs text-muted-foreground uppercase tracking-wider">Customer</p>
                    <p className="text-sm font-medium text-foreground mt-1">{data.customer_name}</p>
                  </div>

                  <div className="bg-muted rounded-xl p-4 space-y-3">
                    <div className="flex items-center justify-between">
                      <span className="text-sm text-muted-foreground">Invoice</span>
                      <span className="text-sm font-mono text-foreground">{data.invoice_number}</span>
                    </div>
                    <div className="flex items-center justify-between">
                      <span className="text-sm text-muted-foreground">Amount Due</span>
                      <span className="text-lg font-semibold text-foreground">{formatCents(data.amount_due_cents, data.currency)}</span>
                    </div>
                  </div>

                  <Button
                    onClick={handleUpdate}
                    disabled={redirecting}
                    className="w-full"
                    size="lg"
                  >
                    {redirecting ? (
                      'Redirecting to Stripe...'
                    ) : (
                      <>
                        <CreditCard size={16} className="mr-2" />
                        Update Payment Method
                        <ExternalLink size={14} className="ml-2 opacity-50" />
                      </>
                    )}
                  </Button>

                  {submitError && (
                    <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
                      {submitError}
                    </div>
                  )}

                  <div className="flex items-center justify-center gap-1.5 text-xs text-muted-foreground">
                    <ShieldCheck size={12} />
                    <span>Secured by Stripe. Your card details are never stored on our servers.</span>
                  </div>
                </div>
              </>
            ) : null}
          </CardContent>
        </Card>

        <p className="text-xs text-muted-foreground text-center mt-6">
          Powered by Velox Billing
        </p>
      </div>
    </div>
  )
}
