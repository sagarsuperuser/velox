import { useState } from 'react'
import { Link } from 'react-router-dom'
import { authApi } from '@/lib/auth'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Loader2, ArrowLeft, CheckCircle2 } from 'lucide-react'
import { VeloxLogo } from '@/components/VeloxLogo'

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const [loading, setLoading] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    if (!email.trim()) {
      setError('Email is required')
      return
    }

    setLoading(true)
    try {
      await authApi.requestPasswordReset(email.trim())
      // Backend always returns 202 regardless of whether the email matches a
      // real user — we show the same success screen either way.
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
        <p className="text-sm text-muted-foreground mt-2">Reset your password</p>
      </div>

      <Card className="w-full max-w-[360px]">
        <CardContent className="p-6">
          {submitted ? (
            <div className="flex flex-col items-center text-center space-y-4">
              <CheckCircle2 size={40} className="text-primary" />
              <h2 className="text-base font-semibold">Check your email</h2>
              <p className="text-sm text-muted-foreground">
                If an account exists for <span className="text-foreground font-medium">{email}</span>,
                we sent a link to reset your password. It expires in 1 hour.
              </p>
              <Link to="/login" className="w-full">
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
                  autoComplete="email"
                  autoFocus
                />
                <p className="text-xs text-muted-foreground">
                  We'll email you a link to reset your password.
                </p>
              </div>

              {error && (
                <div className="px-3 py-2.5 rounded-lg bg-destructive/10 border border-destructive/20">
                  <p className="text-destructive text-sm">{error}</p>
                </div>
              )}

              <Button type="submit" disabled={loading} className="w-full">
                {loading ? <Loader2 size={16} className="animate-spin mr-2" /> : null}
                {loading ? 'Sending...' : 'Send reset link'}
              </Button>

              <Link
                to="/login"
                className="flex items-center justify-center gap-1 text-sm text-muted-foreground hover:text-foreground transition-colors"
              >
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
