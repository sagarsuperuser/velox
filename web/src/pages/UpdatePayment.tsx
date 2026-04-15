import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { formatCents } from '@/lib/api'
import { CreditCard, AlertTriangle, ExternalLink, ShieldCheck, Clock } from 'lucide-react'

interface TokenData {
  customer_name: string
  invoice_number: string
  amount_due_cents: number
  currency: string
  checkout_url: string
}

export function UpdatePaymentPage() {
  const [searchParams] = useSearchParams()
  const token = searchParams.get('token') || ''

  const [data, setData] = useState<TokenData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [redirecting, setRedirecting] = useState(false)

  useEffect(() => {
    if (!token) {
      setError('No payment update token provided')
      setLoading(false)
      return
    }

    // Call public endpoint — no auth required
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

  const handleUpdate = () => {
    if (!data?.checkout_url) return
    setRedirecting(true)
    window.location.href = data.checkout_url
  }

  return (
    <div className="min-h-screen bg-gray-50 flex items-center justify-center p-4">
      <div className="w-full max-w-md">
        {/* Brand header */}
        <div className="text-center mb-8">
          <h1 className="text-2xl font-bold text-gray-900">Velox</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Secure Payment Update</p>
        </div>

        <div className="bg-white rounded-2xl shadow-lg border border-gray-200 dark:border-gray-700 overflow-hidden">
          {loading ? (
            <div className="p-12 text-center">
              <div className="w-8 h-8 border-2 border-velox-600 border-t-transparent rounded-full animate-spin mx-auto" />
              <p className="text-sm text-gray-600 mt-4">Verifying your link...</p>
            </div>
          ) : error ? (
            <div className="p-8 text-center">
              <div className="w-12 h-12 rounded-full bg-red-50 flex items-center justify-center mx-auto mb-4">
                <Clock size={24} className="text-red-400" />
              </div>
              <p className="text-sm font-medium text-gray-900 dark:text-gray-100">Link expired or invalid</p>
              <p className="text-sm text-gray-600 mt-2">{error}</p>
              <p className="text-xs text-gray-500 mt-4">Please contact your billing provider for a new link.</p>
            </div>
          ) : data ? (
            <>
              {/* Alert banner */}
              <div className="bg-amber-50 px-6 py-4 border-b border-amber-100">
                <div className="flex items-start gap-3">
                  <AlertTriangle size={18} className="text-amber-500 mt-0.5 shrink-0" />
                  <div>
                    <p className="text-sm font-medium text-amber-800">Payment method update required</p>
                    <p className="text-xs text-amber-600 mt-1">
                      We were unable to process your payment. Please update your card to avoid service interruption.
                    </p>
                  </div>
                </div>
              </div>

              {/* Invoice details */}
              <div className="p-6 space-y-4">
                <div>
                  <p className="text-xs text-gray-500 uppercase tracking-wider">Customer</p>
                  <p className="text-sm font-medium text-gray-900 dark:text-gray-100 mt-1">{data.customer_name}</p>
                </div>

                <div className="bg-gray-50 rounded-xl p-4 space-y-3">
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-gray-600 dark:text-gray-400">Invoice</span>
                    <span className="text-sm font-mono text-gray-900">{data.invoice_number}</span>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-gray-600 dark:text-gray-400">Amount Due</span>
                    <span className="text-lg font-semibold text-gray-900 dark:text-gray-100">{formatCents(data.amount_due_cents, data.currency)}</span>
                  </div>
                </div>

                <button
                  onClick={handleUpdate}
                  disabled={redirecting}
                  className="w-full flex items-center justify-center gap-2 px-4 py-3 bg-velox-600 text-white rounded-xl text-sm font-medium hover:bg-velox-700 shadow-sm transition-colors disabled:opacity-50"
                >
                  {redirecting ? (
                    'Redirecting to Stripe...'
                  ) : (
                    <>
                      <CreditCard size={16} />
                      Update Payment Method
                      <ExternalLink size={14} className="opacity-50" />
                    </>
                  )}
                </button>

                <div className="flex items-center justify-center gap-1.5 text-xs text-gray-500">
                  <ShieldCheck size={12} />
                  <span>Secured by Stripe. Your card details are never stored on our servers.</span>
                </div>
              </div>
            </>
          ) : null}
        </div>

        <p className="text-xs text-gray-500 text-center mt-6">
          Powered by Velox Billing
        </p>
      </div>
    </div>
  )
}
