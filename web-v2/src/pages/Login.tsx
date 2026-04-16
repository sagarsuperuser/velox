import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { setApiKey } from '@/lib/api'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Zap, Eye, EyeOff, Loader2 } from 'lucide-react'

export default function LoginPage() {
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
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-gray-50 via-white to-[#F7F7FF] dark:from-gray-950 dark:via-gray-900 dark:to-[#1A1523] relative overflow-hidden">
      {/* Background pattern */}
      <div className="absolute inset-0 opacity-[0.03]" style={{
        backgroundImage: 'radial-gradient(circle at 1px 1px, currentColor 1px, transparent 0)',
        backgroundSize: '32px 32px',
      }} />

      <div className="w-full max-w-sm relative z-10">
        <div className="text-center mb-8">
          <div className="inline-flex items-center justify-center w-14 h-14 rounded-2xl bg-primary shadow-lg mb-4">
            <Zap size={28} className="text-white" />
          </div>
          <h1 className="text-3xl font-bold text-foreground">Velox</h1>
          <p className="text-muted-foreground mt-1">Billing Dashboard</p>
          <p className="text-sm text-muted-foreground/70 mt-0.5">Open-source usage-based billing</p>
        </div>

        <Card className="shadow-lg">
          <CardHeader className="pb-4">
            <CardTitle className="text-base">Sign in</CardTitle>
            <CardDescription>Enter your API key to access the dashboard</CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSubmit} noValidate className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="api-key">API Key</Label>
                <div className="relative">
                  <Input
                    id="api-key"
                    type={showKey ? 'text' : 'password'}
                    value={key}
                    onChange={e => setKey(e.target.value)}
                    placeholder="vlx_secret_..."
                    className="pr-10"
                    autoFocus
                  />
                  <button
                    type="button"
                    onClick={() => setShowKey(!showKey)}
                    className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
                    tabIndex={-1}
                    aria-label={showKey ? 'Hide API key' : 'Show API key'}
                  >
                    {showKey ? <EyeOff size={16} /> : <Eye size={16} />}
                  </button>
                </div>
              </div>

              {error && (
                <div className="px-3 py-2 rounded-md bg-destructive/10 border border-destructive/20">
                  <p className="text-destructive text-xs font-medium">{error}</p>
                </div>
              )}

              <Button type="submit" disabled={loading} className="w-full">
                {loading ? (
                  <>
                    <Loader2 size={16} className="animate-spin mr-2" />
                    Signing in...
                  </>
                ) : (
                  'Sign In'
                )}
              </Button>

              <p className="text-xs text-muted-foreground text-center">
                Press <kbd className="px-1.5 py-0.5 bg-muted border border-border rounded text-[10px] font-mono">Enter</kbd> to sign in
              </p>
              <p className="text-xs text-muted-foreground text-center">
                Run <code className="bg-muted px-1 py-0.5 rounded text-[11px]">make bootstrap</code> to get an API key
              </p>
            </form>
          </CardContent>
        </Card>

        <p className="text-center text-xs text-muted-foreground/50 mt-6">
          Powered by Velox
        </p>
      </div>
    </div>
  )
}
