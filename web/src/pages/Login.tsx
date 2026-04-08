import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { setApiKey } from '@/lib/api'

export function LoginPage() {
  const [key, setKey] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const navigate = useNavigate()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    if (!key.startsWith('vlx_')) {
      setError('Invalid API key format')
      return
    }

    setApiKey(key)
    setLoading(true)

    try {
      const res = await fetch('/v1/customers', {
        headers: { Authorization: `Bearer ${key}` },
      })
      if (res.status === 401) {
        setError('Invalid API key')
        return
      }
      navigate('/')
    } catch {
      setError('Cannot connect to Velox API')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50">
      <div className="w-full max-w-sm">
        <div className="text-center mb-8">
          <h1 className="text-3xl font-bold text-velox-900">Velox</h1>
          <p className="text-gray-500 mt-1">Billing Dashboard</p>
          <p className="text-sm text-gray-400 mt-0.5">Open-source usage-based billing</p>
        </div>

        <form onSubmit={handleSubmit} className="bg-white rounded-xl shadow-card p-6">
          <label className="block text-sm font-medium text-gray-700 mb-2">
            API Key
          </label>
          <input
            type="password"
            value={key}
            onChange={e => setKey(e.target.value)}
            placeholder="vlx_secret_..."
            className="w-full px-3 py-2 border border-gray-200 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent"
            autoFocus
          />
          {error && (
            <p className="text-red-600 text-xs mt-2">{error}</p>
          )}
          <button
            type="submit"
            disabled={loading}
            className="w-full mt-4 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50 transition-colors"
          >
            {loading ? 'Signing in...' : 'Sign In'}
          </button>
          <p className="text-xs text-gray-400 mt-3 text-center">
            Run <code className="bg-gray-100 px-1 py-0.5 rounded">make bootstrap</code> to get an API key
          </p>
        </form>
      </div>
    </div>
  )
}
