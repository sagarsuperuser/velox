import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, formatDate, type Customer } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { useToast } from '@/components/Toast'
import { Plus } from 'lucide-react'

export function CustomersPage() {
  const [customers, setCustomers] = useState<Customer[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [creating, setCreating] = useState(false)
  const [form, setForm] = useState({ external_id: '', display_name: '', email: '' })
  const [error, setError] = useState('')
  const toast = useToast()

  const loadCustomers = () => {
    api.listCustomers().then(res => {
      setCustomers(res.data)
      setTotal(res.total)
      setLoading(false)
    })
  }

  useEffect(() => { loadCustomers() }, [])

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setCreating(true)
    try {
      const created = await api.createCustomer(form)
      setShowCreate(false)
      setForm({ external_id: '', display_name: '', email: '' })
      toast.success(`Customer "${created.display_name}" created`)
      loadCustomers()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create customer')
    } finally {
      setCreating(false)
    }
  }

  return (
    <Layout>
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Customers</h1>
          <p className="text-sm text-gray-500 mt-1">{total} total</p>
        </div>
        <button
          onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 transition-colors"
        >
          <Plus size={16} />
          Add Customer
        </button>
      </div>

      <div className="bg-white rounded-xl border border-gray-200 mt-6">
        {loading ? (
          <div className="p-8 text-gray-400 animate-pulse">Loading...</div>
        ) : customers.length === 0 ? (
          <div className="p-12 text-center">
            <p className="text-gray-400 text-sm">No customers yet</p>
            <button onClick={() => setShowCreate(true)} className="mt-3 text-sm text-velox-600 hover:underline">
              Create your first customer
            </button>
          </div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">External ID</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Email</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {customers.map(c => (
                <tr key={c.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-6 py-3">
                    <Link to={`/customers/${c.id}`} className="text-sm font-medium text-gray-900 hover:text-velox-600">
                      {c.display_name}
                    </Link>
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-500 font-mono">{c.external_id}</td>
                  <td className="px-6 py-3 text-sm text-gray-500">{c.email || '—'}</td>
                  <td className="px-6 py-3"><Badge status={c.status} /></td>
                  <td className="px-6 py-3 text-sm text-gray-400">{formatDate(c.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <Modal open={showCreate} onClose={() => setShowCreate(false)} title="Create Customer">
        <form onSubmit={handleCreate} className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Display Name</label>
            <input type="text" value={form.display_name} onChange={e => setForm(f => ({ ...f, display_name: e.target.value }))}
              className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
              placeholder="Acme Corporation" required />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">External ID</label>
            <input type="text" value={form.external_id} onChange={e => setForm(f => ({ ...f, external_id: e.target.value }))}
              className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 font-mono"
              placeholder="acme_corp" required />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Email</label>
            <input type="email" value={form.email} onChange={e => setForm(f => ({ ...f, email: e.target.value }))}
              className="w-full px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-velox-500"
              placeholder="billing@acme.com" />
          </div>
          {error && <p className="text-red-600 text-xs">{error}</p>}
          <div className="flex justify-end gap-3 pt-2">
            <button type="button" onClick={() => setShowCreate(false)} className="px-4 py-2 text-sm text-gray-600 hover:text-gray-900">Cancel</button>
            <button type="submit" disabled={creating}
              className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 disabled:opacity-50">
              {creating ? 'Creating...' : 'Create Customer'}
            </button>
          </div>
        </form>
      </Modal>
    </Layout>
  )
}
