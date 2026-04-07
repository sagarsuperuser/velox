import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api, formatCents, formatDate, type Subscription, type Customer, type Plan, type Invoice, type InvoicePreview } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { useToast } from '@/components/Toast'

export function SubscriptionDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [sub, setSub] = useState<Subscription | null>(null)
  const [customer, setCustomer] = useState<Customer | null>(null)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [preview, setPreview] = useState<InvoicePreview | null>(null)
  const [previewError, setPreviewError] = useState('')
  const [loading, setLoading] = useState(true)
  const [acting, setActing] = useState(false)
  const toast = useToast()

  useEffect(() => {
    if (!id) return
    api.getSubscription(id).then(async (s) => {
      setSub(s)

      const promises: Promise<void>[] = []

      // Fetch customer
      promises.push(
        api.getCustomer(s.customer_id)
          .then(c => setCustomer(c))
          .catch(() => {})
      )

      // Fetch plan
      promises.push(
        api.listPlans()
          .then(res => {
            const found = res.data.find(p => p.id === s.plan_id)
            if (found) setPlan(found)
          })
          .catch(() => {})
      )

      // Fetch invoices
      promises.push(
        api.listInvoices('subscription_id=' + id)
          .then(res => setInvoices(res.data))
          .catch(() => {})
      )

      // Fetch invoice preview
      promises.push(
        api.invoicePreview(id)
          .then(p => setPreview(p))
          .catch(err => setPreviewError(err instanceof Error ? err.message : 'Preview unavailable'))
      )

      await Promise.all(promises)
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [id])

  const handlePause = async () => {
    if (!id || !sub) return
    setActing(true)
    try {
      const updated = await api.pauseSubscription(id)
      setSub(updated)
      toast.success('Subscription paused')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to pause')
    } finally {
      setActing(false)
    }
  }

  const handleResume = async () => {
    if (!id || !sub) return
    setActing(true)
    try {
      const updated = await api.resumeSubscription(id)
      setSub(updated)
      toast.success('Subscription resumed')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to resume')
    } finally {
      setActing(false)
    }
  }

  const handleCancel = async () => {
    if (!id || !sub) return
    if (!window.confirm('Are you sure you want to cancel this subscription? This cannot be undone.')) return
    setActing(true)
    try {
      const updated = await api.cancelSubscription(id)
      setSub(updated)
      toast.success('Subscription canceled')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to cancel')
    } finally {
      setActing(false)
    }
  }

  if (loading) {
    return (
      <Layout>
        <div className="flex items-center gap-3 mb-6">
          <Link to="/subscriptions" className="text-sm text-gray-400 hover:text-gray-600">&larr; Subscriptions</Link>
        </div>
        <div className="bg-white rounded-xl border border-gray-200">
          <LoadingSkeleton rows={8} columns={4} />
        </div>
      </Layout>
    )
  }

  if (!sub) return <Layout><p>Subscription not found</p></Layout>

  return (
    <Layout>
      {/* Back link */}
      <div className="flex items-center gap-3 mb-6">
        <Link to="/subscriptions" className="text-sm text-gray-400 hover:text-gray-600">&larr; Subscriptions</Link>
      </div>

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{sub.display_name}</h1>
          <p className="text-sm text-gray-500 mt-0.5 font-mono">{sub.code}</p>
        </div>
        <div className="flex items-center gap-3">
          {sub.status === 'active' && (
            <>
              <button onClick={handlePause} disabled={acting}
                className="px-3 py-1.5 border border-amber-300 text-amber-600 rounded-lg text-xs font-medium hover:bg-amber-50 disabled:opacity-50 transition-colors">
                Pause
              </button>
              <button onClick={handleCancel} disabled={acting}
                className="px-3 py-1.5 border border-red-300 text-red-600 rounded-lg text-xs font-medium hover:bg-red-50 disabled:opacity-50 transition-colors">
                Cancel
              </button>
            </>
          )}
          {sub.status === 'paused' && (
            <>
              <button onClick={handleResume} disabled={acting}
                className="px-3 py-1.5 border border-emerald-300 text-emerald-600 rounded-lg text-xs font-medium hover:bg-emerald-50 disabled:opacity-50 transition-colors">
                Resume
              </button>
              <button onClick={handleCancel} disabled={acting}
                className="px-3 py-1.5 border border-red-300 text-red-600 rounded-lg text-xs font-medium hover:bg-red-50 disabled:opacity-50 transition-colors">
                Cancel
              </button>
            </>
          )}
          <Badge status={sub.status} />
        </div>
      </div>

      {/* Info cards */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
        <div className="bg-white rounded-xl border border-gray-200 p-4">
          <p className="text-xs text-gray-500">Customer</p>
          {customer ? (
            <Link to={`/customers/${customer.id}`} className="text-sm font-semibold text-velox-600 hover:underline mt-0.5 block">
              {customer.display_name}
            </Link>
          ) : (
            <p className="text-sm font-semibold mt-0.5">{sub.customer_id.slice(0, 8)}...</p>
          )}
        </div>
        <div className="bg-white rounded-xl border border-gray-200 p-4">
          <p className="text-xs text-gray-500">Plan</p>
          <p className="text-sm font-semibold mt-0.5">{plan?.name || sub.plan_id.slice(0, 8) + '...'}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-200 p-4">
          <p className="text-xs text-gray-500">Billing Period</p>
          <p className="text-sm font-semibold mt-0.5">
            {sub.current_billing_period_start && sub.current_billing_period_end
              ? `${formatDate(sub.current_billing_period_start)} - ${formatDate(sub.current_billing_period_end)}`
              : 'Not set'}
          </p>
        </div>
        <div className="bg-white rounded-xl border border-gray-200 p-4">
          <p className="text-xs text-gray-500">Created</p>
          <p className="text-sm font-semibold mt-0.5">{formatDate(sub.created_at)}</p>
        </div>
      </div>

      {/* Invoice Preview */}
      <div className="bg-white rounded-xl border border-gray-200 mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Next Invoice Preview</h2>
        </div>
        {preview ? (
          <>
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-100">
                  <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Description</th>
                  <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Quantity</th>
                  <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Amount</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-50">
                {preview.lines.map((line, i) => (
                  <tr key={i}>
                    <td className="px-6 py-3 text-sm text-gray-900">{line.description}</td>
                    <td className="px-6 py-3 text-sm text-gray-500 text-right">{line.quantity}</td>
                    <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">{formatCents(line.amount_cents)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr className="border-t border-gray-200">
                  <td colSpan={2} className="px-6 py-3 text-sm font-semibold text-gray-900 text-right">Subtotal</td>
                  <td className="px-6 py-3 text-sm font-semibold text-gray-900 text-right">{formatCents(preview.subtotal_cents)}</td>
                </tr>
              </tfoot>
            </table>
          </>
        ) : previewError ? (
          <div className="px-6 py-8 text-center">
            <p className="text-sm text-gray-400">{previewError}</p>
          </div>
        ) : (
          <EmptyState title="No preview available" description="Invoice preview will appear once a billing period is set" />
        )}
      </div>

      {/* Related Invoices */}
      <div className="bg-white rounded-xl border border-gray-200 mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Related Invoices</h2>
        </div>
        {invoices.length > 0 ? (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Invoice</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Amount</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Date</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {invoices.map(inv => (
                <tr key={inv.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-6 py-3">
                    <Link to={`/invoices/${inv.id}`} className="text-sm font-medium text-velox-600 hover:underline">
                      {inv.invoice_number}
                    </Link>
                  </td>
                  <td className="px-6 py-3"><Badge status={inv.status} /></td>
                  <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">{formatCents(inv.total_amount_cents)}</td>
                  <td className="px-6 py-3 text-sm text-gray-400">{formatDate(inv.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <EmptyState title="No invoices" description="Invoices will appear here after billing runs" />
        )}
      </div>
    </Layout>
  )
}
