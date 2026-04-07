import { useEffect, useState } from 'react'
import { api, formatCents, formatDate, type Customer, type CreditBalance, type CreditLedgerEntry } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { useToast } from '@/components/Toast'
import { Plus } from 'lucide-react'

export function CreditsPage() {
  const [customers, setCustomers] = useState<Customer[]>([])
  const [selectedCustomer, setSelectedCustomer] = useState('')
  const [balance, setBalance] = useState<CreditBalance | null>(null)
  const [ledger, setLedger] = useState<CreditLedgerEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [showGrant, setShowGrant] = useState(false)
  const toast = useToast()

  useEffect(() => {
    api.listCustomers().then(res => setCustomers(res.data))
  }, [])

  const loadCredits = (customerId: string) => {
    if (!customerId) return
    setLoading(true)
    Promise.all([
      api.getBalance(customerId),
      api.listLedger(customerId),
    ]).then(([b, l]) => {
      setBalance(b)
      setLedger(l.data || [])
      setLoading(false)
    }).catch(() => {
      setBalance({ customer_id: customerId, balance_cents: 0, total_granted: 0, total_used: 0 })
      setLedger([])
      setLoading(false)
    })
  }

  const handleSelectCustomer = (id: string) => {
    setSelectedCustomer(id)
    loadCredits(id)
  }

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Credits</h1>
          <p className="text-sm text-gray-500 mt-1">Customer prepaid balances</p>
        </div>
        {selectedCustomer && (
          <button onClick={() => setShowGrant(true)}
            className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 transition-colors">
            <Plus size={16} /> Grant Credits
          </button>
        )}
      </div>

      {/* Customer selector */}
      <div className="mt-6">
        <select value={selectedCustomer} onChange={e => handleSelectCustomer(e.target.value)}
          className="w-full max-w-sm px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 bg-white">
          <option value="">Select a customer...</option>
          {customers.map(c => <option key={c.id} value={c.id}>{c.display_name} ({c.external_id})</option>)}
        </select>
      </div>

      {selectedCustomer && balance && !loading && (
        <>
          {/* Balance cards */}
          <div className="grid grid-cols-3 gap-4 mt-6">
            <div className="bg-white rounded-xl border border-gray-200 p-5">
              <p className="text-xs text-gray-500">Current Balance</p>
              <p className="text-2xl font-semibold mt-1 text-gray-900">{formatCents(balance.balance_cents)}</p>
            </div>
            <div className="bg-white rounded-xl border border-gray-200 p-5">
              <p className="text-xs text-gray-500">Total Granted</p>
              <p className="text-2xl font-semibold mt-1 text-emerald-600">{formatCents(balance.total_granted)}</p>
            </div>
            <div className="bg-white rounded-xl border border-gray-200 p-5">
              <p className="text-xs text-gray-500">Total Used</p>
              <p className="text-2xl font-semibold mt-1 text-gray-500">{formatCents(balance.total_used)}</p>
            </div>
          </div>

          {/* Ledger */}
          <div className="bg-white rounded-xl border border-gray-200 mt-6">
            <div className="px-6 py-4 border-b border-gray-100">
              <h2 className="text-sm font-semibold text-gray-900">Credit Ledger</h2>
            </div>
            {ledger.length === 0 ? (
              <p className="px-6 py-8 text-sm text-gray-400 text-center">No credit history</p>
            ) : (
              <table className="w-full">
                <thead>
                  <tr className="border-b border-gray-100">
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Type</th>
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Description</th>
                    <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Amount</th>
                    <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Balance After</th>
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Date</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-50">
                  {ledger.map(entry => (
                    <tr key={entry.id}>
                      <td className="px-6 py-3"><Badge status={entry.entry_type} /></td>
                      <td className="px-6 py-3 text-sm text-gray-900">{entry.description}</td>
                      <td className={`px-6 py-3 text-sm font-medium text-right ${entry.amount_cents >= 0 ? 'text-emerald-600' : 'text-red-600'}`}>
                        {entry.amount_cents >= 0 ? '+' : ''}{formatCents(entry.amount_cents)}
                      </td>
                      <td className="px-6 py-3 text-sm text-gray-500 text-right">{formatCents(entry.balance_after)}</td>
                      <td className="px-6 py-3 text-sm text-gray-400">{formatDate(entry.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </>
      )}

      {loading && <div className="mt-6 text-gray-400 animate-pulse">Loading...</div>}

      {showGrant && (
        <GrantModal customerId={selectedCustomer} onClose={() => setShowGrant(false)}
          onGranted={() => { setShowGrant(false); loadCredits(selectedCustomer); toast.success('Credits granted') }} />
      )}
    </Layout>
  )
}

function GrantModal({ customerId, onClose, onGranted }: { customerId: string; onClose: () => void; onGranted: () => void }) {
  const [amount, setAmount] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [showConfirm, setShowConfirm] = useState(false)

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    setShowConfirm(true)
  }

  const handleConfirmedGrant = async () => {
    setShowConfirm(false)
    setSaving(true); setError('')
    try {
      await api.grantCredits({
        customer_id: customerId,
        amount_cents: Math.round(parseFloat(amount) * 100),
        description: description || 'Credit grant',
      })
      onGranted()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <Modal open onClose={onClose} title="Grant Credits">
        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Amount ($)</label>
            <input type="number" step="0.01" min="0.01" value={amount} onChange={e => setAmount(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
              placeholder="50.00" required />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Description</label>
            <input type="text" value={description} onChange={e => setDescription(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
              placeholder="Welcome credit" />
          </div>
          {error && <p className="text-red-600 text-xs">{error}</p>}
          <div className="flex justify-end gap-3 pt-2">
            <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-gray-600 hover:text-gray-900">Cancel</button>
            <button type="submit" disabled={saving}
              className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 disabled:opacity-50">
              {saving ? 'Granting...' : 'Grant Credits'}
            </button>
          </div>
        </form>
      </Modal>
      <ConfirmDialog
        open={showConfirm}
        title="Confirm Credit Grant"
        message={`Grant $${parseFloat(amount || '0').toFixed(2)} to this customer?`}
        confirmLabel="Grant Credits"
        onConfirm={handleConfirmedGrant}
        onCancel={() => setShowConfirm(false)}
      />
    </>
  )
}
