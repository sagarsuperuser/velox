import { useEffect, useState, useMemo } from 'react'
import { api, formatCents, formatDateTime, type Customer, type CreditBalance, type CreditLedgerEntry } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Plus, Wallet } from 'lucide-react'

export function CreditsPage() {
  const [customers, setCustomers] = useState<Customer[]>([])
  const [selectedCustomer, setSelectedCustomer] = useState('')
  const [balance, setBalance] = useState<CreditBalance | null>(null)
  const [ledger, setLedger] = useState<CreditLedgerEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showGrant, setShowGrant] = useState(false)
  const toast = useToast()

  useEffect(() => {
    api.listCustomers().then(res => setCustomers(res.data)).catch(err => setError(err instanceof Error ? err.message : 'Failed to load customers'))
  }, [])

  const selectedCustomerName = customers.find(c => c.id === selectedCustomer)?.display_name || ''

  const loadCredits = (customerId: string) => {
    if (!customerId) return
    setLoading(true)
    setError(null)
    Promise.all([
      api.getBalance(customerId),
      api.listLedger(customerId),
    ]).then(([b, l]) => {
      setBalance(b)
      setLedger(l.data || [])
      setLoading(false)
    }).catch(err => {
      setError(err instanceof Error ? err.message : 'Failed to load credits')
      setBalance({ customer_id: customerId, balance_cents: 0, total_granted: 0, total_used: 0 })
      setLedger([])
      setLoading(false)
    })
  }

  const handleSelectCustomer = (id: string) => {
    setSelectedCustomer(id)
    if (id) loadCredits(id)
  }

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Credits</h1>
          <p className="text-sm text-gray-500 mt-1">Manage customer prepaid balances</p>
        </div>
        {selectedCustomer && (
          <button onClick={() => setShowGrant(true)}
            className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
            <Plus size={16} /> Grant Credits
          </button>
        )}
      </div>

      {/* Customer selector */}
      <div className="mt-6 max-w-sm">
        <FormSelect label="" value={selectedCustomer}
          onChange={e => handleSelectCustomer(e.target.value)}
          placeholder="Select a customer..."
          options={customers.map(c => ({ value: c.id, label: `${c.display_name} (${c.external_id})` }))} />
      </div>

      {!selectedCustomer && (
        <div className="bg-white rounded-xl shadow-card mt-6 py-16 text-center">
          <Wallet size={32} className="text-gray-300 mx-auto mb-3" />
          <p className="text-sm text-gray-900">Select a customer to view their credit balance</p>
          <p className="text-sm text-gray-500 mt-1">Credits are automatically applied to invoices before payment</p>
        </div>
      )}

      {error && selectedCustomer && (
        <div className="mt-6">
          <ErrorState message={error} onRetry={() => loadCredits(selectedCustomer)} />
        </div>
      )}

      {loading && <div className="bg-white rounded-xl shadow-card mt-6 py-12 text-center text-sm text-gray-400 animate-pulse">Loading...</div>}

      {selectedCustomer && balance && !loading && !error && (
        <>
          {/* Balance strip */}
          <div className="bg-white rounded-xl shadow-card flex divide-x divide-gray-100 mt-6">
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-gray-500">Customer</p>
              <p className="text-sm font-medium text-gray-900 mt-1">{selectedCustomerName}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-gray-500">Current Balance</p>
              <p className="text-lg font-semibold text-gray-900 mt-1">{formatCents(balance.balance_cents)}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-gray-500">Total Granted</p>
              <p className="text-lg font-semibold text-emerald-600 mt-1">{formatCents(balance.total_granted)}</p>
            </div>
            <div className="flex-1 px-6 py-4">
              <p className="text-sm text-gray-500">Total Used</p>
              <p className="text-lg font-semibold text-gray-900 mt-1">{formatCents(balance.total_used)}</p>
            </div>
          </div>

          {/* Ledger */}
          <div className="bg-white rounded-xl shadow-card mt-6">
            <div className="px-6 py-4 border-b border-gray-100">
              <h2 className="text-sm font-semibold text-gray-900">Transaction History</h2>
            </div>
            {ledger.length === 0 ? (
              <div className="px-6 py-12 text-center">
                <p className="text-sm text-gray-900">No transactions yet</p>
                <p className="text-sm text-gray-500 mt-1">Grant credits to this customer to get started</p>
              </div>
            ) : (
              <div className="overflow-x-auto">
              <table className="w-full">
                <thead>
                  <tr className="border-b border-gray-100 bg-gray-50">
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Date</th>
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Type</th>
                    <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Description</th>
                    <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Amount</th>
                    <th className="text-right text-xs font-medium text-gray-500 px-6 py-3">Balance</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-50">
                  {ledger.map(entry => (
                    <tr key={entry.id} className="hover:bg-gray-50/50 transition-colors">
                      <td className="px-6 py-3 text-sm text-gray-500 whitespace-nowrap">{formatDateTime(entry.created_at)}</td>
                      <td className="px-6 py-3"><Badge status={entry.entry_type} /></td>
                      <td className="px-6 py-3 text-sm text-gray-900">{entry.description || '\u2014'}</td>
                      <td className={`px-6 py-3 text-sm font-medium text-right tabular-nums ${entry.amount_cents >= 0 ? 'text-emerald-600' : 'text-red-600'}`}>
                        {entry.amount_cents >= 0 ? '+' : ''}{formatCents(entry.amount_cents)}
                      </td>
                      <td className="px-6 py-3 text-sm text-gray-900 text-right tabular-nums">{formatCents(entry.balance_after)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              </div>
            )}
          </div>
        </>
      )}

      {showGrant && (
        <GrantModal customerId={selectedCustomer} customerName={selectedCustomerName}
          onClose={() => setShowGrant(false)}
          onGranted={() => { setShowGrant(false); loadCredits(selectedCustomer); toast.success('Credits granted') }} />
      )}
    </Layout>
  )
}

function GrantModal({ customerId, customerName, onClose, onGranted }: {
  customerId: string; customerName: string; onClose: () => void; onGranted: () => void
}) {
  const [amount, setAmount] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [showConfirm, setShowConfirm] = useState(false)

  const fieldRules = useMemo(() => ({
    amount: [rules.required('Amount'), rules.minAmount(0.01)],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll({ amount })) return
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
        <form onSubmit={handleSubmit} noValidate className="space-y-4">
          <div className="bg-gray-50 rounded-lg px-4 py-3">
            <p className="text-sm text-gray-500">Customer</p>
            <p className="text-sm font-medium text-gray-900 mt-0.5">{customerName}</p>
          </div>
          <FormField label="Amount ($)" required type="number" step="0.01" min="0.01" max={999999.99} value={amount}
            placeholder="50.00"
            ref={registerRef('amount')} error={fieldError('amount')}
            onChange={e => setAmount(e.target.value)}
            onBlur={() => onBlur('amount', amount)}
            hint="Added to the customer's prepaid balance" />
          <FormField label="Description" value={description} placeholder="e.g. Welcome credit, compensation" maxLength={500}
            onChange={e => setDescription(e.target.value)} />
          {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
          <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-2">
            <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
            <button type="submit" disabled={saving}
              className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
              {saving ? 'Granting...' : 'Grant Credits'}
            </button>
          </div>
        </form>
      </Modal>
      <ConfirmDialog
        open={showConfirm}
        title="Confirm Credit Grant"
        message={`Grant $${parseFloat(amount || '0').toFixed(2)} to ${customerName}?`}
        confirmLabel="Grant Credits"
        onConfirm={handleConfirmedGrant}
        onCancel={() => setShowConfirm(false)}
      />
    </>
  )
}
