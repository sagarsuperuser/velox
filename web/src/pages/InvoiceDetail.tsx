import { useEffect, useState, useMemo } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api, downloadPDF, formatCents, formatDate, type Invoice, type LineItem, type Customer, type Subscription, type CreditNote } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField } from '@/components/FormField'
import { FormSelect } from '@/components/FormField'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Mail, CreditCard } from 'lucide-react'
import { CopyButton } from '@/components/CopyButton'

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
  const [creditNotes, setCreditNotes] = useState<CreditNote[]>([])
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

      // Fetch credit notes for this invoice
      try {
        const cn = await api.listCreditNotes(`invoice_id=${id}`)
        setCreditNotes((cn.data || []).filter(n => n.status === 'issued'))
      } catch {
        // non-critical
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

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">{invoice.invoice_number}</h1>
          <div className="flex items-center gap-1.5 mt-1">
            <span className="text-xs font-mono text-gray-400">{invoice.id}</span>
            <CopyButton text={invoice.id} />
          </div>
        </div>
        <div className="flex items-center gap-2 border-l border-gray-200 pl-4">
          {invoice.status === 'draft' && (
            <button onClick={handleFinalize} disabled={acting}
              className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm disabled:opacity-50 transition-colors">
              Finalize
            </button>
          )}

          {invoice.status !== 'voided' && invoice.status !== 'paid' && (
            <button onClick={() => setShowVoidConfirm(true)} disabled={acting}
              className="px-4 py-2 border border-red-300 text-red-600 rounded-lg text-sm font-medium hover:bg-red-50 disabled:opacity-50 transition-colors">
              Void
            </button>
          )}

          <button onClick={() => setShowEmailModal(true)} disabled={acting}
            className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 disabled:opacity-50 transition-colors inline-flex items-center gap-1.5">
            <Mail size={14} />
            Email
          </button>

          {(invoice.status === 'finalized' || invoice.status === 'paid') && (
            <button onClick={() => setShowCreditModal(true)} disabled={acting}
              className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 disabled:opacity-50 transition-colors inline-flex items-center gap-1.5">
              <CreditCard size={14} />
              Issue Credit
            </button>
          )}

          <button
            onClick={() => downloadPDF(invoice.id, invoice.invoice_number)}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
            Download PDF
          </button>
        </div>
      </div>

      {/* Key metrics row */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="flex divide-x divide-gray-100">
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-500">Subtotal</p>
            <p className="text-lg font-semibold text-gray-900 mt-0.5">{formatCents(invoice.subtotal_cents)}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-500">Total</p>
            <p className="text-lg font-semibold text-gray-900 mt-0.5">{formatCents(invoice.total_amount_cents)}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-500">Amount Due</p>
            <p className="text-lg font-semibold text-gray-900 mt-0.5">{formatCents(invoice.amount_due_cents)}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-500">Status</p>
            <div className="mt-1"><Badge status={invoice.status} /></div>
          </div>
        </div>
      </div>

      {/* Properties card */}
      <div className="bg-white rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100">
          <h2 className="text-sm font-semibold text-gray-900">Properties</h2>
        </div>
        <div className="divide-y divide-gray-50">
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-500">Invoice Number</span>
            <span className="text-sm font-medium text-gray-900">{invoice.invoice_number}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-500">Customer</span>
            <span className="text-sm font-medium text-gray-900">
              {customer ? (
                <Link to={`/customers/${customer.id}`} className="text-velox-600 hover:underline">
                  {customer.display_name}
                </Link>
              ) : (
                invoice.customer_id
              )}
            </span>
          </div>
          {subscription && (
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-gray-500">Subscription</span>
              <span className="text-sm font-medium text-gray-900">{subscription.display_name}</span>
            </div>
          )}
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-500">Billing Period</span>
            <span className="text-sm font-medium text-gray-900">
              {formatDate(invoice.billing_period_start)} — {formatDate(invoice.billing_period_end)}
            </span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-500">Status</span>
            <Badge status={invoice.status} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-500">Payment Status</span>
            <Badge status={invoice.payment_status} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-500">Currency</span>
            <span className="text-sm font-medium text-gray-900 uppercase">{invoice.currency}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-500">Created</span>
            <span className="text-sm font-medium text-gray-900">{formatDate(invoice.created_at)}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-500">ID</span>
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-mono text-gray-500">{invoice.id}</span>
              <CopyButton text={invoice.id} />
            </div>
          </div>
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
            <tr className="border-b border-gray-100 bg-gray-50">
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Description</th>
              <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Type</th>
              <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Qty</th>
              <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Unit Price</th>
              <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Amount</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {lineItems.map(item => (
              <tr key={item.id} className="hover:bg-gray-50/50">
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
              <td colSpan={4} className="px-6 py-2.5 text-sm text-gray-500 text-right">Subtotal</td>
              <td className="px-6 py-2.5 text-sm text-gray-900 text-right">{formatCents(invoice.subtotal_cents)}</td>
            </tr>
            {creditNotes.map(cn => (
              <tr key={cn.id}>
                <td colSpan={4} className="px-6 py-2.5 text-sm text-emerald-600 text-right">
                  Credit note {cn.credit_note_number} — {cn.reason}
                </td>
                <td className="px-6 py-2.5 text-sm text-emerald-600 text-right">-{formatCents(cn.total_cents)}</td>
              </tr>
            ))}
            <tr>
              <td colSpan={4} className="px-6 py-2.5 text-sm font-semibold text-gray-900 text-right">Amount Due</td>
              <td className="px-6 py-2.5 text-sm font-semibold text-gray-900 text-right">{formatCents(invoice.amount_due_cents)}</td>
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

  const fieldRules = useMemo(() => ({
    email: [rules.required('Email'), rules.email()],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll({ email })) return
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
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <FormField label="Recipient Email" required type="email" value={email} placeholder="customer@example.com" maxLength={254}
          ref={registerRef('email')} error={fieldError('email')}
          onChange={e => setEmail(e.target.value)}
          onBlur={() => onBlur('email', email)} />
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-2">
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

  const fieldRules = useMemo(() => ({
    reason: [rules.required('Reason')],
    amount: [rules.required('Amount'), rules.minAmount(0.01), rules.maxAmount(999999.99)],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll({ reason, amount })) return
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
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Invoice</label>
          <p className="text-sm text-gray-500 font-mono">{invoice.invoice_number}</p>
        </div>
        <FormField label="Reason" required value={reason} placeholder="e.g. Service disruption, billing error" maxLength={500}
          ref={registerRef('reason')} error={fieldError('reason')}
          onChange={e => setReason(e.target.value)}
          onBlur={() => onBlur('reason', reason)} />
        <FormField label="Amount ($)" required type="number" step="0.01" min={0.01} max={999999.99} value={amount}
          ref={registerRef('amount')} error={fieldError('amount')}
          onChange={e => setAmount(e.target.value)}
          onBlur={() => onBlur('amount', amount)} />
        <FormSelect label="Type" value={type}
          onChange={e => setType(e.target.value as 'credit' | 'refund')}
          options={[
            { value: 'credit', label: 'Credit' },
            ...(invoice.payment_status === 'paid' ? [{ value: 'refund', label: 'Refund' }] : []),
          ]} />
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-2">
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
