import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '@/contexts/AuthContext'
import { ApiError } from '@/lib/api'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Loader2 } from 'lucide-react'
import { VeloxLogo } from '@/components/VeloxLogo'

export default function LoginPage() {
  const [apiKey, setApiKeyInput] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const navigate = useNavigate()
  const { login } = useAuth()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    const trimmed = apiKey.trim()
    if (!trimmed) {
      setError('Paste your Velox API key')
      return
    }
    if (!trimmed.startsWith('vlx_')) {
      setError('That doesn’t look like a Velox key — it should start with vlx_')
      return
    }

    setLoading(true)
    try {
      await login(trimmed)
      navigate('/', { replace: true })
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 401) {
          setError('Invalid or revoked API key')
        } else {
          setError(err.message)
        }
      } else {
        setError('Cannot connect to Velox API')
      }
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex flex-col items-center justify-center px-4 bg-background">
      <div className="flex flex-col items-center mb-8">
        <VeloxLogo size="lg" />
        <p className="text-sm text-muted-foreground mt-2">Sign in with your API key</p>
      </div>

      <Card className="w-full max-w-[420px]">
        <CardContent className="p-6">
          <form onSubmit={handleSubmit} noValidate className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="api-key">Secret API key</Label>
              <Input
                id="api-key"
                type="password"
                value={apiKey}
                onChange={e => setApiKeyInput(e.target.value)}
                placeholder="vlx_secret_test_..."
                autoComplete="off"
                autoFocus
                spellCheck={false}
              />
              <p className="text-xs text-muted-foreground">
                The Secret Key printed by <code className="font-mono">make bootstrap</code>, or any
                key on the API Keys page after you&rsquo;re in.
              </p>
            </div>

            {error && (
              <div className="px-3 py-2.5 rounded-lg bg-destructive/10 border border-destructive/20">
                <p className="text-destructive text-sm">{error}</p>
              </div>
            )}

            <Button type="submit" disabled={loading} className="w-full">
              {loading ? <Loader2 size={16} className="animate-spin mr-2" /> : null}
              {loading ? 'Signing in…' : 'Sign In'}
            </Button>
          </form>
        </CardContent>
      </Card>
      <p className="text-xs text-muted-foreground mt-6">
        Trouble signing in?{' '}
        <a
          href={`mailto:support@velox.dev?subject=${encodeURIComponent(
            'Velox sign-in issue',
          )}&body=${encodeURIComponent(
            `What happened:\n\n\n--- context ---\nurl: ${typeof window !== 'undefined' ? window.location.href : ''}\nuser_agent: ${typeof navigator !== 'undefined' ? navigator.userAgent : ''}\n`,
          )}`}
          className="text-foreground hover:underline"
        >
          Contact support
        </a>
      </p>
    </div>
  )
}
