import { useEffect, useState } from 'react'
import { api, formatCents, formatDate, type CreditNote } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { useToast } from '@/components/Toast'
import { Plus } from 'lucide-react'

export function CreditNotesPage() {
  const [notes, setNotes] = useState<CreditNote[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const toast = useToast()

  const loadNotes = () => {
    setLoading(true)
    api.listCreditNotes()
      .then(res => { setNotes(res.data || []); setLoading(false) })
      .catch(() => { setNotes([]); setLoading(false) })
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
          className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 transition-colors">
          <Plus size={16} /> Create Credit Note
        </button>
      </div>

      <div className="bg-white rounded-xl border border-gray-200 mt-6">
        {loading ? <LoadingSkeleton rows={5} columns={6} />
        : notes.length === 0 ? <EmptyState title="No credit notes" description="Credit notes will appear here once created" />
        : (
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
                <tr key={note.id} className="hover:bg-gray-50">
                  <td className="px-6 py-3 text-sm font-medium text-gray-900">{note.credit_note_number}</td>
                  <td className="px-6 py-3 text-sm font-mono text-gray-500">{note.invoice_id.slice(0, 8)}...</td>
                  <td className="px-6 py-3"><Badge status={note.status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-500">{note.reason.length > 30 ? note.reason.slice(0, 30) + '...' : note.reason}</td>
                  <td className="px-6 py-3 text-sm font-medium text-gray-900 text-right">{formatCents(note.total_cents)}</td>
                  <td className="px-6 py-3 text-sm text-gray-400">{formatDate(note.created_at)}</td>
                  <td className="px-6 py-3 text-right space-x-2">
                    {note.status === 'draft' && (
                      <button onClick={() => handleIssue(note.id)} className="text-xs text-velox-600 hover:underline">Issue</button>
                    )}
                    {note.status !== 'voided' && (
                      <button onClick={() => handleVoid(note.id)} className="text-xs text-red-600 hover:underline">Void</button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {showCreate && (
        <CreateCreditNoteModal onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); loadNotes(); toast.success('Credit note created') }} />
      )}
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
  const [unitAmountCents, setUnitAmountCents] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

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
    setSaving(true); setError('')
    try {
      await api.createCreditNote({
        invoice_id: invoiceId,
        reason,
        refund_type: refundType,
        lines: [{
          description: description || reason,
          quantity: parseInt(quantity) || 1,
          unit_amount_cents: parseInt(unitAmountCents) || 0,
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
      <form onSubmit={handleSubmit} className="space-y-3">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Invoice</label>
          <select value={invoiceId} onChange={e => setInvoiceId(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white"
            required>
            <option value="">Select invoice...</option>
            {invoices.map(inv => (
              <option key={inv.id} value={inv.id}>
                {inv.invoice_number} ({inv.status}) - {formatCents(inv.total_amount_cents)}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Reason</label>
          <input type="text" value={reason} onChange={e => setReason(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
            placeholder="Billing error" required />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Refund Type</label>
          <select value={refundType} onChange={e => setRefundType(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
            <option value="credit">Credit</option>
            <option value="refund">Refund</option>
          </select>
        </div>

        <div className="border-t border-gray-100 pt-3">
          <p className="text-xs font-medium text-gray-500 uppercase tracking-wider mb-2">Line Item</p>
          <div className="space-y-3">
            <div>
              <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
              <input type="text" value={description} onChange={e => setDescription(e.target.value)}
                className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
                placeholder="Credit for overcharge" />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Quantity</label>
                <input type="number" min={1} value={quantity} onChange={e => setQuantity(e.target.value)}
                  className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-1">Unit Amount (cents)</label>
                <input type="number" min={0} value={unitAmountCents} onChange={e => setUnitAmountCents(e.target.value)}
                  className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
                  placeholder="1000" required />
              </div>
            </div>
          </div>
        </div>

        {error && <p className="text-red-600 text-xs">{error}</p>}
        <div className="flex justify-end gap-3 pt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-gray-600 hover:text-gray-900">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 disabled:opacity-50">
            {saving ? 'Creating...' : 'Create Credit Note'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
