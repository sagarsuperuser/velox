import { useEffect, useState, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useForm, Controller } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { api, formatCents, formatDate, formatDateTime, getCurrencySymbol, type CreditNote, type Invoice, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { SortableHeader } from '@/components/SortableHeader'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { SearchSelect } from '@/components/SearchSelect'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { toast } from 'sonner'
import { useSortable } from '@/hooks/useSortable'
import { Plus, Search, Download, Loader2 } from 'lucide-react'
import { Pagination } from '@/components/Pagination'
import { Breadcrumbs } from '@/components/Breadcrumbs'
import { downloadCSV } from '@/lib/csv'

const creditNoteSchema = z.object({
  invoice_id: z.string().min(1, 'Invoice is required'),
  reason: z.string().min(1, 'Reason is required'),
  amount: z.string().min(1, 'Amount is required').refine(v => parseFloat(v) >= 0.01, 'Must be at least 0.01'),
  refundType: z.string(),
})

type CreditNoteFormData = z.infer<typeof creditNoteSchema>

export function CreditNotesPage() {
  const [notes, setNotes] = useState<CreditNote[]>([])
  const [invoiceMap, setInvoiceMap] = useState<Record<string, Invoice>>({})
  const [customerMap, setCustomerMap] = useState<Record<string, Customer>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [confirmIssue, setConfirmIssue] = useState<string | null>(null)
  const [confirmVoid, setConfirmVoid] = useState<string | null>(null)
  const [filterStatus, setFilterStatus] = useState('')
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const pageSize = 25

  const loadNotes = () => {
    setLoading(true)
    setError(null)
    Promise.all([
      api.listCreditNotes(),
      api.listInvoices().catch(() => ({ data: [] as Invoice[], total: 0 })),
      api.listCustomers().catch(() => ({ data: [] as Customer[], total: 0 })),
    ]).then(([notesRes, invoicesRes, custRes]) => {
      setNotes(notesRes.data || [])
      const iMap: Record<string, Invoice> = {}
      invoicesRes.data.forEach(inv => { iMap[inv.id] = inv })
      setInvoiceMap(iMap)
      const cMap: Record<string, Customer> = {}
      custRes.data.forEach(c => { cMap[c.id] = c })
      setCustomerMap(cMap)
      setLoading(false)
    }).catch(err => { setError(err instanceof Error ? err.message : 'Failed to load credit notes'); setNotes([]); setLoading(false) })
  }

  useEffect(() => { loadNotes() }, [])

  const handleIssue = async (id: string) => {
    try {
      await api.issueCreditNote(id)
      toast.success('Credit note issued')
      loadNotes()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to issue')
    }
  }

  const handleVoid = async (id: string) => {
    try {
      await api.voidCreditNote(id)
      toast.success('Credit note voided')
      loadNotes()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to void')
    }
  }

  // Stats
  const stats = useMemo(() => {
    const draft = notes.filter(n => n.status === 'draft').length
    const issued = notes.filter(n => n.status === 'issued')
    const voided = notes.filter(n => n.status === 'voided').length
    const totalCredited = issued.reduce((sum, n) => sum + n.credit_amount_cents, 0)
    const totalRefunded = issued.reduce((sum, n) => sum + n.refund_amount_cents, 0)
    const totalAmount = issued.reduce((sum, n) => sum + n.total_cents, 0)
    return { draft, issued: issued.length, voided, totalCredited, totalRefunded, totalAmount }
  }, [notes])

  // Filter + search
  const filtered = useMemo(() => notes.filter(n => {
    if (filterStatus && n.status !== filterStatus) return false
    if (search) {
      const q = search.toLowerCase()
      const custName = customerMap[n.customer_id]?.display_name?.toLowerCase() || ''
      const invNum = invoiceMap[n.invoice_id]?.invoice_number?.toLowerCase() || ''
      if (!n.credit_note_number.toLowerCase().includes(q) && !custName.includes(q) && !invNum.includes(q) && !n.reason.toLowerCase().includes(q)) return false
    }
    return true
  }), [notes, filterStatus, search, customerMap, invoiceMap])

  const { sorted, sortKey, sortDir, onSort } = useSortable(filtered, 'created_at', 'desc')

  const totalPages = Math.ceil(sorted.length / pageSize)
  const currentPage = Math.min(page, totalPages || 1)
  const paginated = sorted.slice((currentPage - 1) * pageSize, currentPage * pageSize)

  return (
    <Layout>
      <Breadcrumbs items={[{ label: 'Configuration' }, { label: 'Credit Notes' }]} />
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">Credit Notes</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Issue credits and refunds against invoices</p>
        </div>
        <div className="flex items-center gap-2">
          {notes.length > 0 && (
            <button onClick={() => {
              const rows = filtered.map(n => [
                n.credit_note_number,
                customerMap[n.customer_id]?.display_name || '',
                invoiceMap[n.invoice_id]?.invoice_number || '',
                n.status,
                n.refund_amount_cents > 0 ? 'Refund' : 'Credit',
                n.reason,
                (n.total_cents / 100).toFixed(2),
                n.issued_at || n.created_at,
              ])
              downloadCSV('credit-notes.csv', ['Number', 'Customer', 'Invoice', 'Status', 'Type', 'Reason', 'Amount', 'Date'], rows)
            }}
              className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 shadow-sm transition-colors">
              <Download size={16} /> Export
            </button>
          )}
          <button onClick={() => setShowCreate(true)}
            className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
            <Plus size={16} /> Issue Credit Note
          </button>
        </div>
      </div>

      {/* Summary cards */}
      {!loading && notes.length > 0 && (
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card px-5 py-4">
            <p className="text-xs font-medium text-gray-500 dark:text-gray-400">Total Credited</p>
            <p className="text-xl font-semibold text-gray-900 dark:text-gray-100 mt-1 tabular-nums">{formatCents(stats.totalAmount)}</p>
          </div>
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card px-5 py-4">
            <p className="text-xs font-medium text-gray-500 dark:text-gray-400">Refunded to Card</p>
            <p className="text-xl font-semibold text-indigo-600 mt-1 tabular-nums">{formatCents(stats.totalRefunded)}</p>
          </div>
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card px-5 py-4">
            <p className="text-xs font-medium text-gray-500 dark:text-gray-400">Applied to Balance</p>
            <p className="text-xl font-semibold text-emerald-600 mt-1 tabular-nums">{formatCents(stats.totalCredited)}</p>
          </div>
          <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card px-5 py-4">
            <p className="text-xs font-medium text-gray-500 dark:text-gray-400">Issued</p>
            <p className="text-xl font-semibold text-gray-900 dark:text-gray-100 mt-1">{stats.issued}</p>
            {stats.draft > 0 && <p className="text-xs text-amber-600 mt-0.5">{stats.draft} draft</p>}
          </div>
        </div>
      )}

      {/* Filter tabs + search */}
      {notes.length > 0 && (
        <div className="flex items-center gap-3 mt-6">
          <div className="flex gap-1 bg-gray-100 dark:bg-gray-800 rounded-lg p-1">
            {[
              { value: '', label: 'All', count: notes.length },
              { value: 'draft', label: 'Draft', count: stats.draft },
              { value: 'issued', label: 'Issued', count: stats.issued },
              { value: 'voided', label: 'Voided', count: stats.voided },
            ].map(f => (
              <button key={f.value} onClick={() => { setFilterStatus(f.value); setPage(1) }}
                className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${
                  filterStatus === f.value ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 shadow-sm' : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200'
                }`}>
                {f.label}
                {f.count > 0 && <span className="ml-1 text-gray-400">{f.count}</span>}
              </button>
            ))}
          </div>
          <div className="relative flex-1">
            <Search size={16} className="absolute left-3 top-2.5 text-gray-400" />
            <input type="text" value={search} onChange={e => { setSearch(e.target.value); setPage(1) }}
              placeholder="Search by number, customer, invoice, or reason..."
              className="w-full pl-9 pr-4 py-2 border border-gray-200 dark:border-gray-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white dark:bg-gray-800 dark:text-gray-100" />
          </div>
        </div>
      )}

      {/* Table */}
      <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-4">
        {error ? <ErrorState message={error} onRetry={loadNotes} />
        : loading ? <LoadingSkeleton rows={5} columns={6} />
        : notes.length === 0 ? (
          <div className="px-6 py-12 text-center">
            <p className="text-sm font-medium text-gray-900 dark:text-gray-100">No credit notes yet</p>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Issue a credit note to apply credits or refunds against invoices</p>
            <button onClick={() => setShowCreate(true)}
              className="mt-4 inline-flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm transition-colors">
              <Plus size={16} /> Issue Credit Note
            </button>
          </div>
        ) : filtered.length === 0 ? (
          <p className="px-6 py-8 text-sm text-gray-400 text-center">No credit notes match your search</p>
        ) : (
          <>
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
                <SortableHeader label="Number" sortKey="credit_note_number" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Customer</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Invoice</th>
                <th className="text-left text-xs font-medium text-gray-500 dark:text-gray-400 px-6 py-3">Reason</th>
                <SortableHeader label="Status" sortKey="status" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
                <SortableHeader label="Amount" sortKey="total_cents" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} align="right" />
                <SortableHeader label="Date" sortKey="created_at" activeSortKey={sortKey} sortDir={sortDir} onSort={onSort} />
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {paginated.map(note => {
                const isRefund = note.refund_amount_cents > 0
                return (
                  <tr key={note.id} className="hover:bg-gray-50/50 dark:hover:bg-gray-800/50 transition-colors">
                    <td className="px-6 py-3">
                      <p className="text-sm font-medium text-gray-900 dark:text-gray-100">{note.credit_note_number}</p>
                      <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">{isRefund ? 'Refund to card' : 'Credit to balance'}</p>
                    </td>
                    <td className="px-6 py-3 text-sm">
                      <Link to={`/customers/${note.customer_id}`} onClick={e => e.stopPropagation()} className="text-velox-600 dark:text-velox-400 hover:underline">
                        {customerMap[note.customer_id]?.display_name || 'Unknown'}
                      </Link>
                    </td>
                    <td className="px-6 py-3 text-sm">
                      <Link to={`/invoices/${note.invoice_id}`} onClick={e => e.stopPropagation()} className="text-velox-600 dark:text-velox-400 hover:underline font-mono">
                        {invoiceMap[note.invoice_id]?.invoice_number || note.invoice_id.slice(0, 12) + '...'}
                      </Link>
                    </td>
                    <td className="px-6 py-3 text-sm text-gray-600 max-w-[220px] truncate" title={note.reason}>
                      {note.reason}
                    </td>
                    <td className="px-6 py-3">
                      <Badge status={note.status} />
                      {isRefund && note.status === 'issued' && note.refund_status && note.refund_status !== 'none' && (
                        <span className="ml-1.5">
                          <Badge status={note.refund_status === 'succeeded' ? 'refunded' : note.refund_status === 'failed' ? 'refund_failed' : 'refund_pending'} />
                        </span>
                      )}
                    </td>
                    <td className="px-6 py-3 text-sm font-semibold text-gray-900 dark:text-gray-100 text-right tabular-nums">
                      {formatCents(note.total_cents)}
                    </td>
                    <td className="px-6 py-3">
                      <div className="flex items-center justify-between">
                        <span className="text-sm text-gray-600 whitespace-nowrap">
                          {note.issued_at ? formatDate(note.issued_at) : formatDateTime(note.created_at)}
                        </span>
                        {note.status === 'draft' && (
                          <div className="flex items-center gap-1 ml-3">
                            <button onClick={() => setConfirmIssue(note.id)}
                              className="text-xs font-medium text-velox-600 hover:text-velox-700 bg-velox-50 hover:bg-velox-100 px-2 py-0.5 rounded transition-colors">Issue</button>
                            <button onClick={() => setConfirmVoid(note.id)}
                              className="text-xs font-medium text-red-600 hover:text-red-700 bg-red-50 hover:bg-red-100 px-2 py-0.5 rounded transition-colors">Void</button>
                          </div>
                        )}
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
          </div>
          <Pagination page={currentPage} totalPages={totalPages} onPageChange={setPage} pageSize={pageSize} total={sorted.length} />
          </>
        )}
      </div>

      {showCreate && (
        <CreateCreditNoteModal
          customerMap={customerMap}
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); loadNotes(); toast.success('Credit note issued') }} />
      )}

      <ConfirmDialog
        open={confirmIssue !== null}
        title="Issue Credit Note"
        message="Issue this credit note? This will apply the credit or initiate the refund. Once issued, it cannot be reversed."
        confirmLabel="Issue Credit Note"
        onConfirm={() => { if (confirmIssue) handleIssue(confirmIssue); setConfirmIssue(null) }}
        onCancel={() => setConfirmIssue(null)}
      />
      <ConfirmDialog
        open={confirmVoid !== null}
        title="Void Credit Note"
        message="Void this draft credit note? This cannot be undone."
        confirmLabel="Void"
        variant="danger"
        onConfirm={() => { if (confirmVoid) handleVoid(confirmVoid); setConfirmVoid(null) }}
        onCancel={() => setConfirmVoid(null)}
      />
    </Layout>
  )
}

function CreateCreditNoteModal({ customerMap, onClose, onCreated }: {
  customerMap: Record<string, Customer>
  onClose: () => void
  onCreated: () => void
}) {
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [error, setError] = useState('')
  const [loadingInvoices, setLoadingInvoices] = useState(true)

  const { register, handleSubmit, watch, setValue, control, formState: { errors, isSubmitting, isDirty } } = useForm<CreditNoteFormData>({
    resolver: zodResolver(creditNoteSchema),
    defaultValues: { invoice_id: '', reason: '', amount: '', refundType: 'credit' },
  })

  const invoiceId = watch('invoice_id')
  const reason = watch('reason')
  const amount = watch('amount')
  const refundType = watch('refundType')

  useEffect(() => {
    Promise.all([
      api.listInvoices('status=finalized').catch(() => ({ data: [] as Invoice[], total: 0 })),
      api.listInvoices('status=paid').catch(() => ({ data: [] as Invoice[], total: 0 })),
    ]).then(([fin, paid]) => {
      setInvoices([...fin.data, ...paid.data])
      setLoadingInvoices(false)
    })
  }, [])

  const selectedInvoice = invoices.find(inv => inv.id === invoiceId)
  const isPaid = selectedInvoice?.status === 'paid'
  const maxAmount = selectedInvoice
    ? isPaid ? selectedInvoice.amount_paid_cents : selectedInvoice.amount_due_cents
    : 0

  const handleInvoiceChange = (id: string) => {
    setValue('invoice_id', id, { shouldValidate: true })
    const inv = invoices.find(i => i.id === id)
    if (inv?.status !== 'paid') setValue('refundType', 'credit')
  }

  const onSubmit = handleSubmit(async (data) => {
    setError('')
    try {
      const amountCents = Math.round(parseFloat(data.amount) * 100)
      await api.createCreditNote({
        invoice_id: data.invoice_id,
        reason: data.reason,
        refund_type: data.refundType,
        auto_issue: true,
        lines: [{ description: data.reason, quantity: 1, unit_amount_cents: amountCents }],
      })
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create credit note')
    }
  })

  return (
    <Modal open onClose={onClose} title="Issue Credit Note" dirty={isDirty}>
      {loadingInvoices ? (
        <div className="py-8 text-center">
          <div className="w-6 h-6 border-2 border-velox-600 border-t-transparent rounded-full animate-spin mx-auto" />
          <p className="text-sm text-gray-400 mt-3">Loading invoices...</p>
        </div>
      ) : (
      <form onSubmit={onSubmit} noValidate className="space-y-4">
        <Controller
          name="invoice_id"
          control={control}
          render={({ field }) => (
            <SearchSelect label="Invoice" required value={field.value} error={errors.invoice_id?.message}
              onChange={handleInvoiceChange}
              placeholder="Select invoice..."
              options={invoices.map(inv => {
                const custName = customerMap[inv.customer_id]?.display_name || ''
                return { value: inv.id, label: `${inv.invoice_number} — ${custName}`, sublabel: formatCents(inv.total_amount_cents) }
              })} />
          )}
        />

        {selectedInvoice && (
          <div className="bg-gray-50 dark:bg-gray-800 rounded-lg px-4 py-3">
            <div className="grid grid-cols-3 gap-4 text-sm">
              <div>
                <p className="text-gray-500">Customer</p>
                <p className="font-medium text-gray-900 mt-0.5">{customerMap[selectedInvoice.customer_id]?.display_name || 'Unknown'}</p>
              </div>
              <div>
                <p className="text-gray-500">{isPaid ? 'Amount Paid' : 'Amount Due'}</p>
                <p className="font-medium text-gray-900 mt-0.5">{formatCents(maxAmount)}</p>
              </div>
              <div>
                <p className="text-gray-500">Invoice Status</p>
                <div className="mt-0.5"><Badge status={selectedInvoice.status} /></div>
              </div>
            </div>
          </div>
        )}

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-2">Reason</label>
          <div className="flex flex-wrap gap-1.5 mb-2">
            {['Service disruption', 'Billing error', 'Goodwill credit', 'Feature downgrade', 'Contract adjustment'].map(r => (
              <button key={r} type="button" onClick={() => setValue('reason', r, { shouldValidate: true })}
                className={`px-2.5 py-1 rounded-md text-xs font-medium transition-colors ${
                  reason === r ? 'bg-velox-100 text-velox-700 ring-1 ring-velox-300' : 'bg-gray-100 text-gray-600 hover:bg-gray-200'
                }`}>{r}</button>
            ))}
          </div>
          <FormField label="" required placeholder="Or type a custom reason..." maxLength={500}
            error={errors.reason?.message}
            {...register('reason')} />
        </div>

        <div className="grid grid-cols-2 gap-4">
          <FormField label={`Amount (${getCurrencySymbol()})`} required type="number" step="0.01" min="0.01"
            max={maxAmount ? (maxAmount / 100).toFixed(2) : '999999.99'}
            placeholder="25.00"
            error={errors.amount?.message}
            hint={maxAmount ? `Max: ${formatCents(maxAmount)}` : undefined}
            {...register('amount')} />
          {isPaid ? (
            <FormSelect label="Credit Type"
              {...register('refundType')}
              options={[
                { value: 'credit', label: 'Credit to balance' },
                { value: 'refund', label: 'Refund to payment method' },
              ]} />
          ) : (
            <div className="pt-6">
              <p className="text-sm text-gray-600 dark:text-gray-400">Reduces amount due on invoice</p>
            </div>
          )}
        </div>

        {amount && selectedInvoice && (
          <div className="bg-blue-50 border border-blue-100 rounded-lg px-4 py-3 text-sm">
            <p className="text-blue-800">
              {isPaid && refundType === 'refund'
                ? `${getCurrencySymbol()}${parseFloat(amount || '0').toFixed(2)} will be refunded to the customer's payment method`
                : isPaid
                  ? `${getCurrencySymbol()}${parseFloat(amount || '0').toFixed(2)} will be added to the customer's credit balance`
                  : `Invoice amount due will be reduced by ${getCurrencySymbol()}${parseFloat(amount || '0').toFixed(2)}`
              }
            </p>
          </div>
        )}

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={isSubmitting}
            className="flex items-center justify-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {isSubmitting ? (<><Loader2 size={14} className="animate-spin" /> Issuing...</>) : 'Issue Credit Note'}
          </button>
        </div>
      </form>
      )}
    </Modal>
  )
}
