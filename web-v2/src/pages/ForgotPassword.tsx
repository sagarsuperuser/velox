import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Loader2, ArrowLeft, Mail } from 'lucide-react'
import { VeloxLogo } from '@/components/VeloxLogo'

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [submitted, setSubmitted] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    if (!email) {
      setError('Email is required')
      return
    }

    setLoading(true)

    try {
      const res = await fetch('/v1/auth/forgot-password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email }),
      })

      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: { message: 'Request failed' } }))
        const msg = typeof err.error === 'string' ? err.error : (err.error?.message || 'Something went wrong')
        setError(msg)
        return
      }

      setSubmitted(true)
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
        <p className="text-sm text-muted-foreground mt-2">
          {submitted ? 'Check your email' : 'Reset your password'}
        </p>
      </div>

      <Card className="w-full max-w-[360px]">
        <CardContent className="p-6">
          {submitted ? (
            <div className="space-y-4">
              <div className="flex flex-col items-center text-center py-2">
                <div className="w-10 h-10 rounded-full bg-emerald-100 dark:bg-emerald-900/30 flex items-center justify-center mb-3">
                  <Mail size={18} className="text-emerald-600 dark:text-emerald-400" />
                </div>
                <p className="text-sm text-foreground font-medium">Reset link sent</p>
                <p className="text-xs text-muted-foreground mt-1">
                  If an account exists with that email, you'll receive a password reset link.
                </p>
                <p className="text-xs text-muted-foreground mt-3 italic">
                  In development, the reset token is logged to the server console.
                </p>
              </div>
              <Link to="/login">
                <Button variant="outline" className="w-full">
                  <ArrowLeft size={14} className="mr-2" />
                  Back to sign in
                </Button>
              </Link>
            </div>
          ) : (
            <form onSubmit={handleSubmit} noValidate className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="email">Email</Label>
                <Input
                  id="email"
                  type="email"
                  value={email}
                  onChange={e => setEmail(e.target.value)}
                  placeholder="you@company.com"
                  autoFocus
                  autoComplete="email"
                />
              </div>

              {error && (
                <div className="px-3 py-2.5 rounded-lg bg-destructive/10 border border-destructive/20">
                  <p className="text-destructive text-sm">{error}</p>
                </div>
              )}

              <Button type="submit" disabled={loading} className="w-full">
                {loading ? <Loader2 size={16} className="animate-spin mr-2" /> : null}
                {loading ? 'Sending...' : 'Send Reset Link'}
              </Button>

              <Link to="/login" className="flex items-center justify-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors">
                <ArrowLeft size={14} />
                Back to sign in
              </Link>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
