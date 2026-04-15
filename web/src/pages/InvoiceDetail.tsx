import { useEffect, useState, useMemo } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api, downloadPDF, formatCents, formatDate, formatDateTime, getCurrencySymbol, type Invoice, type LineItem, type Customer, type Subscription, type CreditNote, type TimelineEvent } from '@/lib/api'
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
  const [showAddLineItem, setShowAddLineItem] = useState(false)
  const [creditNotes, setCreditNotes] = useState<CreditNote[]>([])
  const [timeline, setTimeline] = useState<TimelineEvent[]>([])
  const [pdfPreviewUrl, setPdfPreviewUrl] = useState<string | null>(null)
  const toast = useToast()

  useEffect(() => {
    return () => { if (pdfPreviewUrl) URL.revokeObjectURL(pdfPreviewUrl) }
  }, [pdfPreviewUrl])

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

      // Fetch payment timeline (non-critical)
      if (res.invoice.status !== 'draft') {
        try {
          const tl = await api.getPaymentTimeline(id)
          setTimeline(tl.events || [])
        } catch {
          // non-critical
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
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card">
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
      <div className="sticky top-0 z-10 bg-white dark:bg-gray-950 pb-4 -mx-4 px-4 md:-mx-8 md:px-8 pt-2 border-b border-gray-100 dark:border-gray-800">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">{invoice.invoice_number}</h1>
            <div className="flex items-center gap-1.5 mt-1">
              <span className="text-xs font-mono text-gray-400">{invoice.id}</span>
              <CopyButton text={invoice.id} />
            </div>
          </div>
          <div className="flex items-center gap-2 border-l border-gray-200 pl-4">
            {invoice.status === 'draft' && (
              <>
                <button onClick={() => setShowAddLineItem(true)} disabled={acting}
                  className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 disabled:opacity-50 transition-colors">
                  Add Line Item
                </button>
                <button onClick={handleFinalize} disabled={acting}
                  className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm disabled:opacity-50 transition-colors">
                  Finalize
                </button>
              </>
            )}

            {invoice.status !== 'voided' && invoice.status !== 'paid' && (
              <button onClick={() => setShowVoidConfirm(true)} disabled={acting}
                className="px-4 py-2 border border-red-300 text-red-600 rounded-lg text-sm font-medium hover:bg-red-50 disabled:opacity-50 transition-colors">
                Void
              </button>
            )}

            {invoice.status !== 'voided' && (
              <button onClick={() => setShowEmailModal(true)} disabled={acting}
                className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 disabled:opacity-50 transition-colors inline-flex items-center gap-1.5">
                <Mail size={14} />
                Email
              </button>
            )}

            {(invoice.status === 'finalized' || invoice.status === 'paid') && (
              <button onClick={() => setShowCreditModal(true)} disabled={acting}
                className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 disabled:opacity-50 transition-colors inline-flex items-center gap-1.5">
                <CreditCard size={14} />
                Issue Credit
              </button>
            )}

            {invoice.status === 'finalized' && invoice.payment_status !== 'paid' && invoice.amount_due_cents > 0 && (
              <button onClick={async () => {
                setActing(true)
                try {
                  const updated = await api.collectPayment(invoice.id)
                  setInvoice(updated)
                  toast.success('Payment initiated')
                } catch (err) {
                  toast.error(err instanceof Error ? err.message : 'Payment failed')
                } finally {
                  setActing(false)
                }
              }} disabled={acting}
                className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm disabled:opacity-50 transition-colors">
                Collect Payment
              </button>
            )}

            <button
              onClick={async () => {
                try {
                  const res = await fetch(`${import.meta.env.VITE_API_URL || ''}/v1/invoices/${invoice.id}/pdf`, {
                    headers: { 'Authorization': `Bearer ${localStorage.getItem('velox_api_key') || ''}` },
                  })
                  const blob = await res.blob()
                  const url = URL.createObjectURL(blob)
                  setPdfPreviewUrl(url)
                } catch {
                  toast.error('Failed to load PDF preview')
                }
              }}
              className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">
              Preview PDF
            </button>
            <button
              onClick={() => downloadPDF(invoice.id, invoice.invoice_number)}
              className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
              Download PDF
            </button>
          </div>
        </div>
      </div>

      {/* Status banner */}
      <div className={`rounded-xl mt-6 px-6 py-4 flex items-center justify-between ${
        invoice.status === 'paid' ? 'bg-emerald-50 border border-emerald-200' :
        invoice.status === 'voided' ? 'bg-red-50 border border-red-200' :
        invoice.status === 'draft' ? 'bg-gray-100 border border-gray-200' :
        invoice.payment_status === 'failed' ? 'bg-red-50 border border-red-200' :
        'bg-blue-50 border border-blue-200'
      }`}>
        <div className="flex items-center gap-3">
          <Badge status={invoice.status} />
          {invoice.status === 'finalized' && <Badge status={invoice.payment_status} />}
          <span className={`text-sm font-medium ${
            invoice.status === 'paid' ? 'text-emerald-800' :
            invoice.status === 'voided' ? 'text-red-800' :
            invoice.payment_status === 'failed' ? 'text-red-800' :
            'text-gray-800'
          }`}>
            {invoice.status === 'paid' && invoice.paid_at ? `Paid on ${formatDate(invoice.paid_at)}` :
             invoice.status === 'voided' && invoice.voided_at ? `Voided on ${formatDate(invoice.voided_at)}` :
             invoice.status === 'draft' ? 'Draft — not yet finalized' :
             invoice.payment_status === 'failed' ? `Payment failed — ${formatCents(invoice.amount_due_cents, invoice.currency)} outstanding` :
             invoice.amount_due_cents > 0 ? `Due on ${invoice.due_at ? formatDate(invoice.due_at) : 'N/A'}` :
             'Finalized'}
          </span>
        </div>
        <span className="text-2xl font-semibold text-gray-900 dark:text-gray-100 tabular-nums">{formatCents(invoice.amount_due_cents, invoice.currency)}</span>
      </div>

      {/* Key metrics row */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        <div className="flex divide-x divide-gray-100 dark:divide-gray-800">
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-600 dark:text-gray-400">Subtotal</p>
            <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-0.5 tabular-nums">{formatCents(invoice.subtotal_cents, invoice.currency)}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-600 dark:text-gray-400">Total</p>
            <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-0.5 tabular-nums">{formatCents(invoice.total_amount_cents, invoice.currency)}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-600 dark:text-gray-400">Amount Due</p>
            <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-0.5 tabular-nums">{formatCents(invoice.amount_due_cents, invoice.currency)}</p>
          </div>
          <div className="flex-1 px-6 py-4">
            <p className="text-sm text-gray-600 dark:text-gray-400">Due Date</p>
            <p className="text-lg font-semibold text-gray-900 dark:text-gray-100 mt-0.5">{invoice.due_at ? formatDate(invoice.due_at) : '\u2014'}</p>
          </div>
        </div>
      </div>

      {/* Properties card */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Properties</h2>
        </div>
        <div className="divide-y divide-gray-100 dark:divide-gray-800">
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Invoice Number</span>
            <span className="text-sm font-medium text-gray-900 dark:text-gray-100">{invoice.invoice_number}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Customer</span>
            <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
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
              <span className="text-sm text-gray-600 dark:text-gray-400">Subscription</span>
              <Link to={`/subscriptions/${subscription.id}`} className="text-sm font-medium text-velox-600 hover:underline">{subscription.display_name}</Link>
            </div>
          )}
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Billing Period</span>
            <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
              {formatDate(invoice.billing_period_start)} — {formatDate(invoice.billing_period_end)}
            </span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Status</span>
            <Badge status={invoice.status} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Payment Status</span>
            <Badge status={invoice.payment_status} />
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Currency</span>
            <span className="text-sm font-medium text-gray-900 dark:text-gray-100 uppercase">{invoice.currency}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">Created</span>
            <span className="text-sm font-medium text-gray-900 dark:text-gray-100">{formatDateTime(invoice.created_at)}</span>
          </div>
          <div className="flex items-center justify-between px-6 py-3">
            <span className="text-sm text-gray-600 dark:text-gray-400">ID</span>
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-mono text-gray-500">{invoice.id}</span>
              <CopyButton text={invoice.id} />
            </div>
          </div>
        </div>
      </div>

      {/* Payment failed banner */}
      {invoice.payment_status === 'failed' && invoice.status !== 'voided' && (
        <div className="bg-red-50 border border-red-200 rounded-xl mt-6 px-6 py-4">
          <div className="flex items-start justify-between">
            <div className="flex items-start gap-3">
              <div className="w-8 h-8 rounded-lg bg-red-100 flex items-center justify-center shrink-0 mt-0.5">
                <CreditCard size={16} className="text-red-600" />
              </div>
              <div>
                <p className="text-sm font-semibold text-red-800">Payment failed — {formatCents(invoice.amount_due_cents, invoice.currency)} outstanding</p>
                {invoice.last_payment_error && (
                  <p className="text-sm text-red-700 mt-1">{invoice.last_payment_error}</p>
                )}
                {invoice.stripe_payment_intent_id && (
                  <p className="text-xs text-red-400 mt-1 font-mono">PI: {invoice.stripe_payment_intent_id}</p>
                )}
              </div>
            </div>
            <div className="flex items-center gap-2 ml-4 shrink-0">
              <button
                onClick={async () => {
                  try {
                    const res = await api.updatePaymentMethod(invoice.customer_id)
                    window.open(res.url, '_blank')
                    toast.success('Stripe payment update page opened in new tab')
                  } catch {
                    window.location.href = `/customers/${invoice.customer_id}`
                  }
                }}
                className="px-4 py-2 border border-red-300 text-red-700 rounded-lg text-sm font-medium hover:bg-red-100 transition-colors whitespace-nowrap"
              >
                Update Payment Method
              </button>
              {invoice.status === 'finalized' && invoice.amount_due_cents > 0 && (
                <button
                  onClick={async () => {
                    setActing(true)
                    try {
                      const updated = await api.collectPayment(invoice.id)
                      setInvoice(updated)
                      toast.success('Payment retry initiated')
                    } catch (err) {
                      toast.error(err instanceof Error ? err.message : 'Payment failed')
                    } finally {
                      setActing(false)
                    }
                  }}
                  disabled={acting}
                  className="px-4 py-2 bg-red-600 text-white rounded-lg text-sm font-medium hover:bg-red-700 shadow-sm disabled:opacity-50 transition-colors whitespace-nowrap"
                >
                  Retry Payment
                </button>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Voided banner */}
      {invoice.status === 'voided' && (
        <div className="bg-red-50 border border-red-200 rounded-xl mt-6 px-6 py-4 flex items-center gap-3">
          <span className="text-red-600 font-bold text-lg">VOID</span>
          <div>
            <p className="text-sm font-medium text-red-800">This invoice has been voided</p>
            <p className="text-xs text-red-600 mt-0.5">
              {invoice.voided_at ? `Voided on ${formatDate(invoice.voided_at)}` : 'All charges and credits have been reversed'}
            </p>
          </div>
        </div>
      )}

      {/* Payment Activity Timeline */}
      {timeline.length > 0 && invoice.status !== 'draft' && (
        <div className={`bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6 ${invoice.status === 'voided' ? 'opacity-60' : ''}`}>
          <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
            <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Payment Activity</h2>
          </div>
          <div className="px-6 py-4">
            <div className="relative">
              {timeline.map((event, i) => (
                <div key={i} className="flex gap-4 pb-4 last:pb-0">
                  <div className="flex flex-col items-center">
                    <div className={`w-2.5 h-2.5 rounded-full mt-1.5 ${
                      event.status === 'succeeded' || event.status === 'resolved' ? 'bg-emerald-500' :
                      event.status === 'failed' || event.status === 'canceled' ? 'bg-red-500' :
                      event.status === 'processing' || event.status === 'scheduled' ? 'bg-blue-500' :
                      event.status === 'escalated' ? 'bg-violet-500' :
                      'bg-amber-500'
                    }`} />
                    {i < timeline.length - 1 && <div className="w-px flex-1 bg-gray-200 mt-1" />}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center justify-between">
                      <p className="text-sm text-gray-900 dark:text-gray-100">{event.description}</p>
                      <span className="text-xs text-gray-500 ml-4 whitespace-nowrap">{formatDateTime(event.timestamp)}</span>
                    </div>
                    {event.error && event.status === 'failed' && (
                      <p className="text-xs text-red-600 mt-0.5">{event.error}</p>
                    )}
                    {event.amount_cents != null && event.amount_cents > 0 && (
                      <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">{formatCents(event.amount_cents, invoice.currency)}</p>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}

      {/* Line items */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
        <div className="px-6 py-4 border-b border-gray-100 dark:border-gray-800">
          <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Line Items</h2>
        </div>
        <div className="overflow-x-auto">
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
              <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Description</th>
              <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Type</th>
              <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Qty</th>
              <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Unit Price</th>
              <th className="text-right text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Amount</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {lineItems.map(item => (
              <tr key={item.id} className={`hover:bg-gray-50/50 ${invoice.status === 'voided' ? 'opacity-50' : ''}`}>
                <td className="px-6 py-3 text-sm text-gray-900 dark:text-gray-100">{item.description}</td>
                <td className="px-6 py-3"><Badge status={item.line_type} label={formatLineType(item.line_type)} /></td>
                <td className="px-6 py-3 text-sm text-gray-600 text-right">{item.quantity}</td>
                <td className="px-6 py-3 text-sm text-gray-600 text-right">{formatCents(item.unit_amount_cents, invoice.currency)}</td>
                <td className="px-6 py-3 text-sm font-medium text-gray-900 dark:text-gray-100 text-right">{formatCents(item.amount_cents, invoice.currency)}</td>
              </tr>
            ))}
          </tbody>
          <tfoot>
            {/* ── Subtotal ── */}
            <tr className="border-t border-gray-200 dark:border-gray-700">
              <td colSpan={4} className={`px-6 py-2.5 text-sm text-right ${invoice.status === 'voided' ? 'text-gray-400' : 'text-gray-500'}`}>Subtotal</td>
              <td className={`px-6 py-2.5 text-sm text-right ${invoice.status === 'voided' ? 'text-gray-400' : 'text-gray-900'}`}>{formatCents(invoice.subtotal_cents, invoice.currency)}</td>
            </tr>

            {/* ── Discount ── */}
            {invoice.discount_cents > 0 && (
              <tr>
                <td colSpan={4} className={`px-6 py-2.5 text-sm text-right ${invoice.status === 'voided' ? 'text-gray-400' : 'text-gray-500'}`}>Discount</td>
                <td className={`px-6 py-2.5 text-sm text-right ${invoice.status === 'voided' ? 'text-gray-400' : 'text-gray-900'}`}>-{formatCents(invoice.discount_cents, invoice.currency)}</td>
              </tr>
            )}

            {/* ── Tax ── */}
            {invoice.tax_amount_cents > 0 && (
              <tr>
                <td colSpan={4} className={`px-6 py-2.5 text-sm text-right ${invoice.status === 'voided' ? 'text-gray-400' : 'text-gray-500'}`}>
                  {invoice.tax_name || 'Tax'}{invoice.tax_rate > 0 ? ` (${invoice.tax_rate}%)` : ''}
                </td>
                <td className={`px-6 py-2.5 text-sm text-right ${invoice.status === 'voided' ? 'text-gray-400' : 'text-gray-900'}`}>{formatCents(invoice.tax_amount_cents, invoice.currency)}</td>
              </tr>
            )}

            {/* ── Total ── */}
            <tr className="border-t border-gray-100 dark:border-gray-800">
              <td colSpan={4} className={`px-6 py-2.5 text-sm font-medium text-right ${invoice.status === 'voided' ? 'text-gray-400' : 'text-gray-900'}`}>Total</td>
              <td className={`px-6 py-2.5 text-sm font-medium text-right ${invoice.status === 'voided' ? 'text-gray-400' : 'text-gray-900'}`}>{formatCents(invoice.total_amount_cents, invoice.currency)}</td>
            </tr>

            {/* ── Voided: simple Amount Due $0.00, no settlement rows ── */}
            {invoice.status === 'voided' ? (
              <tr className="border-t border-gray-200 dark:border-gray-700">
                <td colSpan={4} className="px-6 py-3 text-sm font-semibold text-gray-900 dark:text-gray-100 text-right">Amount Due</td>
                <td className="px-6 py-3 text-sm font-semibold text-gray-900 dark:text-gray-100 text-right">$0.00</td>
              </tr>
            ) : (() => {
              // Non-voided: full settlement waterfall
              const prePaymentCNs = invoice.status === 'paid'
                ? creditNotes.filter(cn => cn.refund_amount_cents === 0 && cn.credit_amount_cents === 0)
                : creditNotes
              const postPaymentCNs = invoice.status === 'paid'
                ? creditNotes.filter(cn => cn.refund_amount_cents > 0 || cn.credit_amount_cents > 0)
                : []

              return (
                <>
                  {prePaymentCNs.map(cn => (
                    <tr key={cn.id}>
                      <td colSpan={4} className="px-6 py-2.5 text-sm text-emerald-600 text-right">
                        Credit note {cn.credit_note_number} — {cn.reason}
                      </td>
                      <td className="px-6 py-2.5 text-sm text-emerald-600 text-right">-{formatCents(cn.total_cents, invoice.currency)}</td>
                    </tr>
                  ))}

                  {invoice.credits_applied_cents > 0 && (
                    <tr>
                      <td colSpan={4} className="px-6 py-2.5 text-sm text-emerald-600 text-right">Prepaid credits applied</td>
                      <td className="px-6 py-2.5 text-sm text-emerald-600 text-right">-{formatCents(invoice.credits_applied_cents, invoice.currency)}</td>
                    </tr>
                  )}

                  {invoice.amount_paid_cents > 0 && (
                    <tr>
                      <td colSpan={4} className="px-6 py-2.5 text-sm text-gray-600 text-right">Amount Paid</td>
                      <td className="px-6 py-2.5 text-sm text-gray-900 text-right">-{formatCents(invoice.amount_paid_cents, invoice.currency)}</td>
                    </tr>
                  )}

                  <tr className="border-t border-gray-200 dark:border-gray-700">
                    <td colSpan={4} className="px-6 py-3 text-sm font-semibold text-gray-900 dark:text-gray-100 text-right">Amount Due</td>
                    <td className="px-6 py-3 text-sm font-semibold text-gray-900 dark:text-gray-100 text-right">{formatCents(invoice.amount_due_cents, invoice.currency)}</td>
                  </tr>

                  {/* Post-payment adjustments (only successful ones) */}
                  {(() => {
                    const completedCNs = postPaymentCNs.filter(cn =>
                      cn.credit_amount_cents > 0 ||
                      (cn.refund_amount_cents > 0 && cn.refund_status === 'succeeded')
                    )
                    return completedCNs.length > 0 ? (
                      <>
                        <tr>
                          <td colSpan={5} className="px-6 pt-4 pb-2">
                            <span className="text-xs font-medium text-gray-400 uppercase tracking-wider">Post-payment adjustments</span>
                          </td>
                        </tr>
                        {completedCNs.map(cn => (
                          <tr key={cn.id}>
                            <td colSpan={4} className="px-6 py-2 text-sm text-gray-600 text-right">
                              {cn.credit_note_number} — {cn.reason}
                              <span className="ml-2 text-xs text-gray-500">
                                {cn.refund_amount_cents > 0 ? '(refunded)' : '(credited to balance)'}
                              </span>
                            </td>
                            <td className="px-6 py-2 text-sm text-gray-600 text-right">{formatCents(cn.total_cents, invoice.currency)}</td>
                          </tr>
                        ))}
                      </>
                    ) : null
                  })()}
                </>
              )
            })()}
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
            toast.success('Credit note issued')
            loadData()
          }}
          onError={(msg) => toast.error(msg)}
        />
      )}

      {showAddLineItem && (
        <AddLineItemModal
          invoiceId={invoice.id}
          onClose={() => setShowAddLineItem(false)}
          onAdded={() => {
            setShowAddLineItem(false)
            toast.success('Line item added')
            loadData()
          }}
        />
      )}

      {pdfPreviewUrl && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm" onClick={() => { URL.revokeObjectURL(pdfPreviewUrl); setPdfPreviewUrl(null) }}>
          <div className="relative w-full max-w-4xl h-[85vh] bg-white rounded-2xl shadow-2xl overflow-hidden" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between px-6 py-3 border-b border-gray-100 dark:border-gray-800">
              <h2 className="text-sm font-semibold text-gray-900 dark:text-gray-100">Invoice Preview — {invoice.invoice_number}</h2>
              <button onClick={() => { URL.revokeObjectURL(pdfPreviewUrl); setPdfPreviewUrl(null) }}
                className="w-8 h-8 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors">
                ✕
              </button>
            </div>
            <iframe src={pdfPreviewUrl} className="w-full h-full" title="Invoice PDF Preview" />
          </div>
        </div>
      )}
    </Layout>
  )
}

function AddLineItemModal({ invoiceId, onClose, onAdded }: {
  invoiceId: string
  onClose: () => void
  onAdded: () => void
}) {
  const [description, setDescription] = useState('')
  const [lineType, setLineType] = useState('add_on')
  const [quantity, setQuantity] = useState('1')
  const [unitAmount, setUnitAmount] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!description.trim()) { setError('Description is required'); return }
    if (!unitAmount || parseFloat(unitAmount) <= 0) { setError('Unit amount must be greater than 0'); return }
    setSaving(true); setError('')
    try {
      await api.addInvoiceLineItem(invoiceId, {
        description: description.trim(),
        line_type: lineType,
        quantity: parseInt(quantity) || 1,
        unit_amount_cents: Math.round(parseFloat(unitAmount) * 100),
      })
      onAdded()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add line item')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Add Line Item">
      <form onSubmit={handleSubmit} noValidate className="space-y-4">
        <FormField label="Description" required value={description} placeholder="e.g. Setup fee, Consulting hours"
          onChange={e => setDescription(e.target.value)} maxLength={500} />
        <FormSelect label="Type" value={lineType}
          onChange={e => setLineType(e.target.value)}
          options={[
            { value: 'add_on', label: 'Add-On' },
            { value: 'base_fee', label: 'Base Fee' },
            { value: 'usage', label: 'Usage' },
            { value: 'discount', label: 'Discount' },
          ]} />
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Quantity" required type="number" min={1} value={quantity}
            onChange={e => setQuantity(e.target.value)} />
          <FormField label={`Unit Price (${getCurrencySymbol()})`} required type="number" step="0.01" min={0.01}
            value={unitAmount} placeholder="10.00"
            onChange={e => setUnitAmount(e.target.value)} />
        </div>
        {unitAmount && quantity && (
          <p className="text-sm text-gray-600 dark:text-gray-400">
            Total: {getCurrencySymbol()}{((parseInt(quantity) || 1) * parseFloat(unitAmount || '0')).toFixed(2)}
          </p>
        )}
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Adding...' : 'Add Line Item'}
          </button>
        </div>
      </form>
    </Modal>
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
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
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
  const [amount, setAmount] = useState('')
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
        auto_issue: true,
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
          <p className="text-sm text-gray-600 dark:text-gray-400 font-mono">{invoice.invoice_number}</p>
        </div>
        <FormField label="Reason" required value={reason} placeholder="e.g. Service disruption, billing error" maxLength={500}
          ref={registerRef('reason')} error={fieldError('reason')}
          onChange={e => setReason(e.target.value)}
          onBlur={() => onBlur('reason', reason)} />
        <FormField label={`Amount (${getCurrencySymbol()})`} required type="number" step="0.01" min={0.01} max={999999.99} value={amount}
          ref={registerRef('amount')} error={fieldError('amount')}
          onChange={e => setAmount(e.target.value)}
          onBlur={() => onBlur('amount', amount)} />
        <FormSelect label="Type" value={type}
          onChange={e => setType(e.target.value as 'credit' | 'refund')}
          options={[
            { value: 'credit', label: 'Credit' },
            ...(invoice.status === 'paid' ? [{ value: 'refund', label: 'Refund — return to payment method' }] : []),
          ]} />
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Creating...' : 'Create Credit Note'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
