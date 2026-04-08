import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api, downloadPDF, formatCents, formatDate, type Invoice, type LineItem, type Customer, type Subscription } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { useToast } from '@/components/Toast'
import { Mail, CreditCard } from 'lucide-react'

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
  const [error, setError] = useState<string | null>(null)
  const [acting, setActing] = useState(false)
  const [showVoidConfirm, setShowVoidConfirm] = useState(false)
  const [showEmailModal, setShowEmailModal] = useState(false)
  const [showCreditModal, setShowCreditModal] = useState(false)
  const toast = useToast()

  const loadData = () => {
    if (!id) return
    setLoading(true)
    setError(null)
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
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load invoice'); setLoading(false) })
  }

  useEffect(() => { loadData() }, [id])

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

  if (error) return <Layout><ErrorState message={error} onRetry={loadData} /></Layout>

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

            <button onClick={() => setShowEmailModal(true)} disabled={acting}
              className="px-3 py-1.5 border border-gray-300 text-gray-600 rounded-lg text-xs font-medium hover:bg-gray-50 disabled:opacity-50 transition-colors inline-flex items-center gap-1.5">
              <Mail size={14} />
              Email Invoice
            </button>

            {(invoice.status === 'finalized' || invoice.status === 'paid') && (
              <button onClick={() => setShowCreditModal(true)} disabled={acting}
                className="px-3 py-1.5 border border-amber-300 text-amber-600 rounded-lg text-xs font-medium hover:bg-amber-50 disabled:opacity-50 transition-colors inline-flex items-center gap-1.5">
                <CreditCard size={14} />
                Issue Credit
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
        <div className="overflow-x-auto">
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

      {showEmailModal && (
        <EmailInvoiceModal
          invoiceId={invoice.id}
          defaultEmail={customer?.email || ''}
          onClose={() => setShowEmailModal(false)}
          onSent={() => {
            setShowEmailModal(false)
            toast.success('Invoice email sent')
          }}
          onError={(msg) => toast.error(msg)}
        />
      )}

      {showCreditModal && (
        <IssueCreditModal
          invoice={invoice}
          onClose={() => setShowCreditModal(false)}
          onCreated={() => {
            setShowCreditModal(false)
            toast.success('Credit note created')
          }}
          onError={(msg) => toast.error(msg)}
        />
      )}
    </Layout>
  )
}

function EmailInvoiceModal({ invoiceId, defaultEmail, onClose, onSent, onError }: {
  invoiceId: string
  defaultEmail: string
  onClose: () => void
  onSent: () => void
  onError: (msg: string) => void
}) {
  const [email, setEmail] = useState(defaultEmail)
  const [sending, setSending] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSending(true)
    try {
      await api.sendInvoiceEmail(invoiceId, email)
      onSent()
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to send email')
    } finally {
      setSending(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Email Invoice">
      <form onSubmit={handleSubmit} className="space-y-3">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Recipient Email <span className="text-red-500">*</span></label>
          <input type="email" value={email} onChange={e => setEmail(e.target.value)} required
            placeholder="customer@example.com"
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
            maxLength={254} pattern="[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}" title="Enter a valid email address" />
        </div>
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-1">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
          <button type="submit" disabled={sending}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {sending ? 'Sending...' : 'Send Email'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

function IssueCreditModal({ invoice, onClose, onCreated, onError }: {
  invoice: Invoice
  onClose: () => void
  onCreated: () => void
  onError: (msg: string) => void
}) {
  const [reason, setReason] = useState('')
  const [amount, setAmount] = useState((invoice.total_amount_cents / 100).toFixed(2))
  const [type, setType] = useState<'credit' | 'refund'>('credit')
  const [saving, setSaving] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    try {
      const amountCents = Math.round(parseFloat(amount) * 100)
      await api.createCreditNote({
        invoice_id: invoice.id,
        reason,
        refund_type: type,
        lines: [{ description: reason, quantity: 1, unit_amount_cents: amountCents }],
      })
      onCreated()
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to create credit note')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Issue Credit / Refund">
      <form onSubmit={handleSubmit} className="space-y-3">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Invoice</label>
          <p className="text-sm text-gray-500 font-mono">{invoice.invoice_number}</p>
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Reason <span className="text-red-500">*</span></label>
          <input type="text" value={reason} onChange={e => setReason(e.target.value)} required
            placeholder="e.g. Service disruption, billing error"
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
            maxLength={500} />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Amount ($) <span className="text-red-500">*</span></label>
          <input type="number" step="0.01" min="0.01" max={999999.99} value={amount} onChange={e => setAmount(e.target.value)} required
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Type</label>
          <select value={type} onChange={e => setType(e.target.value as 'credit' | 'refund')}
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
            <option value="credit">Credit</option>
            {invoice.payment_status === 'paid' && <option value="refund">Refund</option>}
          </select>
        </div>
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-1">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Creating...' : 'Create Credit Note'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
