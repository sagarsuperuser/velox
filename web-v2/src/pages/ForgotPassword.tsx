import { useState } from 'react'
import { Link } from 'react-router-dom'
import { authApi } from '@/lib/auth'
import { ApiError } from '@/lib/api'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Loader2 } from 'lucide-react'
import { VeloxLogo } from '@/components/VeloxLogo'

// ForgotPasswordPage is the dashboard's password-reset request flow.
// Always renders the same "if your email is on file, a reset link
// has been sent" message regardless of whether the email matched a
// user — defends against account enumeration via response timing or
// content. ADR-011.
//
// Self-hosted deployments without SMTP wired surface the reset link
// to server logs (internal/user/handler.go) — operators retrieve it
// from there.
export default function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

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
      setSubmitted(true)
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message)
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
        <p className="text-sm text-muted-foreground mt-2">Reset your password</p>
      </div>

      <Card className="w-full max-w-[420px]">
        <CardContent className="p-6">
          {submitted ? (
            <div className="space-y-4 text-sm">
              <p className="text-foreground">
                If an account exists for <strong>{email}</strong>, we&rsquo;ve sent a password-reset link.
                The link expires in 1 hour.
              </p>
              <Link to="/login">
                <Button variant="outline" className="w-full">
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
                  placeholder="you@example.com"
                  autoComplete="email"
                  autoFocus
                  spellCheck={false}
                />
                <p className="text-xs text-muted-foreground">
                  We&rsquo;ll send a reset link to this email.
                </p>
              </div>

              {error && (
                <div className="px-3 py-2.5 rounded-lg bg-destructive/10 border border-destructive/20">
                  <p className="text-destructive text-sm">{error}</p>
                </div>
              )}

              <Button type="submit" disabled={loading} className="w-full">
                {loading ? <Loader2 size={16} className="animate-spin mr-2" /> : null}
                {loading ? 'Sending…' : 'Send reset link'}
              </Button>

              <Link to="/login" className="block text-center text-xs text-muted-foreground hover:text-foreground transition-colors">
                Back to sign in
              </Link>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
