import { useEffect, useState, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { api, formatCents, formatDate, type CreditNote, type Invoice } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Plus } from 'lucide-react'

export function CreditNotesPage() {
  const [notes, setNotes] = useState<CreditNote[]>([])
  const [invoiceMap, setInvoiceMap] = useState<Record<string, Invoice>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [confirmIssue, setConfirmIssue] = useState<string | null>(null)
  const [confirmVoid, setConfirmVoid] = useState<string | null>(null)
  const toast = useToast()

  const loadNotes = () => {
    setLoading(true)
    setError(null)
    Promise.all([
      api.listCreditNotes(),
      api.listInvoices().catch(() => ({ data: [] as Invoice[], total: 0 })),
    ]).then(([notesRes, invoicesRes]) => {
      setNotes(notesRes.data || [])
      const map: Record<string, Invoice> = {}
      invoicesRes.data.forEach(inv => { map[inv.id] = inv })
      setInvoiceMap(map)
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

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Credit Notes</h1>
          <p className="text-sm text-gray-500 mt-1">Issue credits and refunds against invoices</p>
        </div>
        <button onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
          <Plus size={16} /> Create Credit Note
        </button>
      </div>

      <div className="bg-white rounded-xl shadow-card mt-6">
        {error ? <ErrorState message={error} onRetry={loadNotes} />
        : loading ? <LoadingSkeleton rows={5} columns={6} />
        : notes.length === 0 ? <EmptyState title="No credit notes" description="Credit notes will appear here once created" />
        : (
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Number</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Invoice</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Reason</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Total</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Date</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {notes.map(note => (
                <tr key={note.id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-6 py-3 text-sm font-medium text-gray-900">{note.credit_note_number}</td>
                  <td className="px-6 py-3 text-sm">
                    <Link to={`/invoices/${note.invoice_id}`} className="text-velox-600 hover:underline">
                      {invoiceMap[note.invoice_id]?.invoice_number || note.invoice_id.slice(0, 8) + '...'}
                    </Link>
                  </td>
                  <td className="px-6 py-3"><Badge status={note.status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-500">{note.reason.length > 30 ? note.reason.slice(0, 30) + '...' : note.reason}</td>
                  <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">{formatCents(note.total_cents)}</td>
                  <td className="px-6 py-3 text-sm text-gray-500">{formatDate(note.created_at)}</td>
                  <td className="px-6 py-3 text-right space-x-2">
                    {note.status === 'draft' && (
                      <button onClick={() => setConfirmIssue(note.id)} className="text-xs font-medium text-velox-600 hover:text-velox-700 bg-velox-50 hover:bg-velox-100 px-2.5 py-1 rounded-md transition-colors">Issue</button>
                    )}
                    {note.status !== 'voided' && (
                      <button onClick={() => setConfirmVoid(note.id)} className="text-xs font-medium text-red-600 hover:text-red-700 bg-red-50 hover:bg-red-100 px-2.5 py-1 rounded-md transition-colors">Void</button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
      </div>

      {showCreate && (
        <CreateCreditNoteModal onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); loadNotes(); toast.success('Credit note created') }} />
      )}

      <ConfirmDialog
        open={confirmIssue !== null}
        title="Issue Credit Note"
        message="Issue this credit note? This cannot be undone."
        confirmLabel="Issue"
        onConfirm={() => { if (confirmIssue) handleIssue(confirmIssue); setConfirmIssue(null) }}
        onCancel={() => setConfirmIssue(null)}
      />
      <ConfirmDialog
        open={confirmVoid !== null}
        title="Void Credit Note"
        message="Void this credit note? This cannot be undone."
        confirmLabel="Void"
        variant="danger"
        onConfirm={() => { if (confirmVoid) handleVoid(confirmVoid); setConfirmVoid(null) }}
        onCancel={() => setConfirmVoid(null)}
      />
    </Layout>
  )
}

function CreateCreditNoteModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [invoiceId, setInvoiceId] = useState('')
  const [invoices, setInvoices] = useState<{ id: string; invoice_number: string; status: string; total_amount_cents: number }[]>([])
  const [reason, setReason] = useState('')
  const [refundType, setRefundType] = useState('credit')
  const [description, setDescription] = useState('')
  const [quantity, setQuantity] = useState('1')
  const [unitAmountDollars, setUnitAmountDollars] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const fieldRules = useMemo(() => ({
    invoice_id: [rules.required('Invoice')],
    reason: [rules.required('Reason')],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  useEffect(() => {
    api.listInvoices('status=finalized').then(res => {
      // Include both finalized and paid invoices
      api.listInvoices('status=paid').then(paidRes => {
        setInvoices([...res.data, ...paidRes.data])
      })
    }).catch(() => {
      // Fallback: load all invoices
      api.listInvoices().then(res => setInvoices(res.data))
    })
  }, [])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll({ invoice_id: invoiceId, reason })) return
    setSaving(true); setError('')
    try {
      await api.createCreditNote({
        invoice_id: invoiceId,
        reason,
        refund_type: refundType,
        lines: [{
          description: description || reason,
          quantity: parseInt(quantity) || 1,
          unit_amount_cents: Math.round(parseFloat(unitAmountDollars) * 100) || 0,
        }],
      })
      onCreated()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Create Credit Note">
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <FormSelect label="Invoice" required value={invoiceId} error={fieldError('invoice_id')}
          onChange={e => { setInvoiceId(e.target.value); onBlur('invoice_id', e.target.value) }}
          placeholder="Select invoice..."
          options={invoices.map(inv => ({ value: inv.id, label: `${inv.invoice_number} (${inv.status}) - ${formatCents(inv.total_amount_cents)}` }))} />
        <FormField label="Reason" required value={reason} placeholder="Billing error" maxLength={500}
          ref={registerRef('reason')} error={fieldError('reason')}
          onChange={e => setReason(e.target.value)}
          onBlur={() => onBlur('reason', reason)} />
        <FormSelect label="Refund Type" value={refundType}
          onChange={e => setRefundType(e.target.value)}
          options={[{ value: 'credit', label: 'Credit' }, { value: 'refund', label: 'Refund' }]} />

        <div className="border-t border-gray-100 pt-3">
          <p className="text-xs font-medium text-gray-500 uppercase tracking-wider mb-2">Line Item</p>
          <div className="space-y-3">
            <FormField label="Description" value={description} placeholder="Credit for overcharge" maxLength={500}
              onChange={e => setDescription(e.target.value)} />
            <div className="grid grid-cols-2 gap-3">
              <FormField label="Quantity" type="number" min={1} value={quantity}
                onChange={e => setQuantity(e.target.value)} />
              <FormField label="Unit Price ($)" required type="number" step="0.01" min="0" max={999999.99} value={unitAmountDollars}
                placeholder="10.00"
                onChange={e => setUnitAmountDollars(e.target.value)} />
            </div>
          </div>
        </div>

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
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
