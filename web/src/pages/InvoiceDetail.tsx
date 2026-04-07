import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api, downloadPDF, formatCents, formatDate, type Invoice, type LineItem, type Customer, type Subscription } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { useToast } from '@/components/Toast'

const LINE_TYPE_LABELS: Record<string, string> = {
  base_fee: 'Base Fee',
  usage: 'Usage',
  add_on: 'Add-On',
  discount: 'Discount',
  tax: 'Tax',
}

function formatLineType(raw: string): string {
  return LINE_TYPE_LABELS[raw] || raw.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
}

export function InvoiceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [invoice, setInvoice] = useState<Invoice | null>(null)
  const [lineItems, setLineItems] = useState<LineItem[]>([])
  const [customer, setCustomer] = useState<Customer | null>(null)
  const [subscription, setSubscription] = useState<Subscription | null>(null)
  const [loading, setLoading] = useState(true)
  const [acting, setActing] = useState(false)
  const [showVoidConfirm, setShowVoidConfirm] = useState(false)
  const toast = useToast()

  useEffect(() => {
    if (!id) return
    api.getInvoice(id).then(async (res) => {
      setInvoice(res.invoice)
      setLineItems(res.line_items)

      // Fetch customer name
      try {
        const c = await api.getCustomer(res.invoice.customer_id)
        setCustomer(c)
      } catch {
        // customer may not be accessible
      }

      // Fetch subscription name if present
      if (res.invoice.subscription_id) {
        try {
          const s = await api.getSubscription(res.invoice.subscription_id)
          setSubscription(s)
        } catch {
          // subscription may not be accessible
        }
      }

      setLoading(false)
    })
  }, [id])

  const handleFinalize = async () => {
    if (!id || !invoice) return
    setActing(true)
    try {
      const updated = await api.finalizeInvoice(id)
      setInvoice(updated)
      toast.success(`Invoice ${invoice.invoice_number} finalized`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to finalize')
    } finally {
      setActing(false)
    }
  }

  const handleVoid = async () => {
    if (!id || !invoice) return
    setActing(true)
    try {
      const updated = await api.voidInvoice(id)
      setInvoice(updated)
      toast.success(`Invoice ${invoice.invoice_number} voided`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to void')
    } finally {
      setActing(false)
      setShowVoidConfirm(false)
    }
  }

  if (loading) {
    return (
      <Layout>
        <Breadcrumbs items={[{ label: 'Invoices', to: '/invoices' }, { label: 'Loading...' }]} />
        <div className="bg-white rounded-xl shadow-card">
          <LoadingSkeleton rows={8} columns={5} />
        </div>
      </Layout>
    )
  }

  if (!invoice) return <Layout><p>Invoice not found</p></Layout>

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Invoices', to: '/invoices' }, { label: invoice.invoice_number }]} />

      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{invoice.invoice_number}</h1>
          {customer && (
            <p className="text-sm text-gray-600 mt-0.5">
              <Link to={`/customers/${customer.id}`} className="text-velox-600 hover:underline">
                {customer.display_name}
              </Link>
            </p>
          )}
          {subscription && (
            <p className="text-xs text-gray-400 mt-0.5">
              Subscription: {subscription.display_name}
            </p>
          )}
          <p className="text-sm text-gray-500 mt-0.5">
            {formatDate(invoice.billing_period_start)} — {formatDate(invoice.billing_period_end)}
          </p>
        </div>
        <div className="flex items-center gap-4">
          <div className="flex items-center gap-2">
            <Badge status={invoice.status} />
            <Badge status={invoice.payment_status} />
          </div>

          <div className="flex items-center gap-2 border-l border-gray-200 pl-4">
            {invoice.status === 'draft' && (
              <button onClick={handleFinalize} disabled={acting}
                className="px-3 py-1.5 border border-blue-600 text-blue-600 rounded-lg text-xs font-medium hover:bg-blue-50 disabled:opacity-50 transition-colors">
                Finalize
              </button>
            )}

            {invoice.status !== 'voided' && invoice.status !== 'paid' && (
              <button onClick={() => setShowVoidConfirm(true)} disabled={acting}
                className="px-3 py-1.5 border border-red-300 text-red-600 rounded-lg text-xs font-medium hover:bg-red-50 disabled:opacity-50 transition-colors">
                Void
              </button>
            )}

            <button
              onClick={() => downloadPDF(invoice.id, invoice.invoice_number)}
              className="px-3 py-1.5 bg-velox-600 text-white rounded-lg text-xs font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
              Download PDF
            </button>
          </div>
        </div>
      </div>

      {/* Summary */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
        <div className="bg-white rounded-xl shadow-card p-4">
          <p className="text-xs text-gray-500">Subtotal</p>
          <p className="text-lg font-semibold mt-0.5">{formatCents(invoice.subtotal_cents)}</p>
        </div>
        <div className="bg-white rounded-xl shadow-card p-4">
          <p className="text-xs text-gray-500">Total</p>
          <p className="text-lg font-semibold mt-0.5">{formatCents(invoice.total_amount_cents)}</p>
        </div>
        <div className="bg-white rounded-xl shadow-card p-4">
          <p className="text-xs text-gray-500">Amount Due</p>
          <p className="text-lg font-semibold mt-0.5">{formatCents(invoice.amount_due_cents)}</p>
        </div>
        <div className="bg-white rounded-xl shadow-card p-4">
          <p className="text-xs text-gray-500">Currency</p>
          <p className="text-lg font-semibold mt-0.5">{invoice.currency}</p>
        </div>
      </div>

      {/* Line items */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Line Items</h2>
        </div>
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-100">
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Description</th>
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Type</th>
              <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Qty</th>
              <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Unit Price</th>
              <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Amount</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {lineItems.map(item => (
              <tr key={item.id}>
                <td className="px-6 py-3 text-sm text-gray-900">{item.description}</td>
                <td className="px-6 py-3"><Badge status={item.line_type} label={formatLineType(item.line_type)} /></td>
                <td className="px-6 py-3 text-sm text-gray-500 text-right">{item.quantity}</td>
                <td className="px-6 py-3 text-sm text-gray-500 text-right">{formatCents(item.unit_amount_cents)}</td>
                <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">{formatCents(item.total_amount_cents)}</td>
              </tr>
            ))}
          </tbody>
          <tfoot>
            <tr className="border-t border-gray-200">
              <td colSpan={4} className="px-6 py-3 text-sm font-semibold text-gray-900 text-right">Total</td>
              <td className="px-6 py-3 text-sm font-semibold text-gray-900 text-right">{formatCents(invoice.total_amount_cents)}</td>
            </tr>
          </tfoot>
        </table>
      </div>

      <ConfirmDialog
        open={showVoidConfirm}
        title="Void Invoice"
        message="Are you sure you want to void this invoice? This action cannot be undone."
        confirmLabel="Void Invoice"
        variant="danger"
        onConfirm={handleVoid}
        onCancel={() => setShowVoidConfirm(false)}
      />
    </Layout>
  )
}
