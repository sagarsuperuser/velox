import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { setApiKey } from '@/lib/api'
import { Zap, Eye, EyeOff, Loader2 } from 'lucide-react'

export function LoginPage() {
  const [key, setKey] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [showKey, setShowKey] = useState(false)
  const navigate = useNavigate()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    if (!key.startsWith('vlx_')) {
      setError('API key must start with vlx_secret_ or vlx_pub_')
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
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-gray-50 via-white to-velox-50 dark:from-gray-950 dark:via-gray-900 dark:to-gray-950 relative overflow-hidden">
      {/* Background pattern */}
      <div className="absolute inset-0 opacity-[0.03] dark:opacity-[0.05]" style={{
        backgroundImage: 'radial-gradient(circle at 1px 1px, currentColor 1px, transparent 0)',
        backgroundSize: '32px 32px',
      }} />

      <div className="w-full max-w-sm relative z-10">
        <div className="text-center mb-8">
          <div className="inline-flex items-center justify-center w-14 h-14 rounded-2xl bg-velox-600 shadow-lg shadow-velox-500/20 mb-4">
            <Zap size={28} className="text-white" />
          </div>
          <h1 className="text-3xl font-bold text-velox-900 dark:text-gray-100">Velox</h1>
          <p className="text-gray-500 mt-1">Billing Dashboard</p>
          <p className="text-sm text-gray-400 mt-0.5">Open-source usage-based billing</p>
        </div>

        <form onSubmit={handleSubmit} noValidate className="bg-white dark:bg-gray-900 rounded-xl shadow-lg shadow-gray-200/50 dark:shadow-black/20 border border-gray-200/60 dark:border-gray-800 p-6">
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
            API Key
          </label>
          <div className="relative">
            <input
              type={showKey ? 'text' : 'password'}
              value={key}
              onChange={e => setKey(e.target.value)}
              placeholder="vlx_secret_..."
              className="w-full px-3 py-2 pr-10 border border-gray-200 dark:border-gray-700 rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent dark:bg-gray-800 dark:text-gray-100"
              autoFocus
            />
            <button
              type="button"
              onClick={() => setShowKey(!showKey)}
              className="absolute right-2.5 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 transition-colors"
              tabIndex={-1}
              aria-label={showKey ? 'Hide API key' : 'Show API key'}
            >
              {showKey ? <EyeOff size={16} /> : <Eye size={16} />}
            </button>
          </div>
          {error && (
            <div className="mt-2 px-3 py-1.5 rounded-md bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800/40">
              <p className="text-red-600 dark:text-red-400 text-xs font-medium">{error}</p>
            </div>
          )}
          <button
            type="submit"
            disabled={loading}
            className="w-full mt-4 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50 transition-all flex items-center justify-center gap-2"
          >
            {loading ? (
              <>
                <Loader2 size={16} className="animate-spin" />
                Signing in...
              </>
            ) : (
              'Sign In'
            )}
          </button>
          <p className="text-xs text-gray-400 mt-3 text-center">
            Press <kbd className="px-1.5 py-0.5 bg-gray-100 dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded text-[10px] font-mono">Enter</kbd> to sign in
          </p>
          <p className="text-xs text-gray-500 mt-2 text-center">
            Run <code className="bg-gray-100 dark:bg-gray-800 px-1 py-0.5 rounded">make bootstrap</code> to get an API key
          </p>
        </form>

        <p className="text-center text-xs text-gray-400 dark:text-gray-600 mt-6">
          Powered by Velox
        </p>
      </div>
    </div>
  )
}
