import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api, formatCents, formatDate, type Invoice, type LineItem } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { useToast } from '@/components/Toast'

export function InvoiceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [invoice, setInvoice] = useState<Invoice | null>(null)
  const [lineItems, setLineItems] = useState<LineItem[]>([])
  const [loading, setLoading] = useState(true)
  const [acting, setActing] = useState(false)
  const toast = useToast()

  const loadInvoice = () => {
    if (!id) return
    api.getInvoice(id).then(res => {
      setInvoice(res.invoice)
      setLineItems(res.line_items)
      setLoading(false)
    })
  }

  useEffect(() => { loadInvoice() }, [id])

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
    if (!confirm('Are you sure you want to void this invoice?')) return
    setActing(true)
    try {
      const updated = await api.voidInvoice(id)
      setInvoice(updated)
      toast.success(`Invoice ${invoice.invoice_number} voided`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to void')
    } finally {
      setActing(false)
    }
  }

  if (loading) return <Layout><div className="animate-pulse text-gray-400">Loading...</div></Layout>
  if (!invoice) return <Layout><p>Invoice not found</p></Layout>

  return (
    <Layout>
      <div className="flex items-center gap-3 mb-6">
        <Link to="/invoices" className="text-sm text-gray-400 hover:text-gray-600">&larr; Invoices</Link>
      </div>

      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{invoice.invoice_number}</h1>
          <p className="text-sm text-gray-500 mt-0.5">
            {formatDate(invoice.billing_period_start)} — {formatDate(invoice.billing_period_end)}
          </p>
        </div>
        <div className="flex items-center gap-3">
          <Badge status={invoice.status} />
          <Badge status={invoice.payment_status} />

          {invoice.status === 'draft' && (
            <button
              onClick={handleFinalize}
              disabled={acting}
              className="px-3 py-1.5 bg-blue-600 text-white rounded-lg text-xs font-medium hover:bg-blue-700 disabled:opacity-50 transition-colors"
            >
              Finalize
            </button>
          )}

          {invoice.status !== 'voided' && invoice.status !== 'paid' && (
            <button
              onClick={handleVoid}
              disabled={acting}
              className="px-3 py-1.5 bg-red-600 text-white rounded-lg text-xs font-medium hover:bg-red-700 disabled:opacity-50 transition-colors"
            >
              Void
            </button>
          )}

          <button
            onClick={async () => {
              const res = await fetch(`/v1/invoices/${invoice.id}/pdf`, {
                headers: { Authorization: `Bearer ${localStorage.getItem('velox_api_key') || ''}` },
              })
              const blob = await res.blob()
              const url = URL.createObjectURL(blob)
              const a = document.createElement('a')
              a.href = url
              a.download = `${invoice.invoice_number}.pdf`
              a.click()
              URL.revokeObjectURL(url)
            }}
            className="px-3 py-1.5 bg-velox-600 text-white rounded-lg text-xs font-medium hover:bg-velox-700 transition-colors"
          >
            Download PDF
          </button>
        </div>
      </div>

      {/* Summary */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
        <div className="bg-white rounded-xl border border-gray-200 p-4">
          <p className="text-xs text-gray-500">Subtotal</p>
          <p className="text-lg font-semibold mt-0.5">{formatCents(invoice.subtotal_cents)}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-200 p-4">
          <p className="text-xs text-gray-500">Total</p>
          <p className="text-lg font-semibold mt-0.5">{formatCents(invoice.total_amount_cents)}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-200 p-4">
          <p className="text-xs text-gray-500">Amount Due</p>
          <p className="text-lg font-semibold mt-0.5">{formatCents(invoice.amount_due_cents)}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-200 p-4">
          <p className="text-xs text-gray-500">Currency</p>
          <p className="text-lg font-semibold mt-0.5">{invoice.currency}</p>
        </div>
      </div>

      {/* Line items */}
      <div className="bg-white rounded-xl border border-gray-200 mt-6">
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
                <td className="px-6 py-3"><Badge status={item.line_type} /></td>
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
    </Layout>
  )
}
