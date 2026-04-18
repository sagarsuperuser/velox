import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { setApiKey } from '@/lib/api'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Loader2, Eye, EyeOff } from 'lucide-react'
import { VeloxLogo } from '@/components/VeloxLogo'

export default function LoginPage() {
  const [key, setKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const navigate = useNavigate()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    if (!key.startsWith('vlx_')) {
      setError('API key must start with vlx_')
      return
    }

    setApiKey(key)
    setLoading(true)

    try {
      const res = await fetch('/v1/customers', {
        headers: { Authorization: `Bearer ${key}` },
      })
      if (res.status === 401) {
        const body = await res.json().catch(() => null)
        const msg = body?.message || ''
        if (msg.includes('expired')) {
          setError('This API key has expired. Please use a valid key.')
        } else {
          setError('Invalid API key')
        }
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
    <div className="min-h-screen flex flex-col items-center justify-center px-4 bg-background">
      <div className="flex flex-col items-center mb-8">
        <VeloxLogo size="lg" />
        <p className="text-sm text-muted-foreground mt-2">Sign in to your billing dashboard</p>
      </div>

      <Card className="w-full max-w-[360px]">
        <CardContent className="p-6">
          <form onSubmit={handleSubmit} noValidate className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="api-key">API Key</Label>
              <div className="relative">
                <Input
                  id="api-key"
                  type={showKey ? 'text' : 'password'}
                  value={key}
                  onChange={e => setKey(e.target.value)}
                  placeholder="vlx_secret_..."
                  autoFocus
                  className="pr-10"
                />
                <button type="button" tabIndex={-1} onClick={() => setShowKey(!showKey)}
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors">
                  {showKey ? <EyeOff size={16} /> : <Eye size={16} />}
                </button>
              </div>
            </div>

            {error && (
              <div className="px-3 py-2.5 rounded-lg bg-destructive/10 border border-destructive/20">
                <p className="text-destructive text-sm">{error}</p>
              </div>
            )}

            <Button type="submit" disabled={loading} className="w-full">
              {loading ? <Loader2 size={16} className="animate-spin mr-2" /> : null}
              {loading ? 'Signing in...' : 'Sign In'}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
