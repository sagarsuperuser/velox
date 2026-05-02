import { useState } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Loader2, Mail } from 'lucide-react'
import { VeloxLogo } from '@/components/VeloxLogo'
import { requestMagicLink } from '@/lib/portalAuth'

// PortalLogin is the customer-facing magic-link request page. The
// server's /v1/public/customer-portal/magic-link endpoint always
// returns 202 regardless of whether the email matches a customer (no
// enumeration oracle), so this page shows the same confirmation copy
// in every case.
//
// Distinct from the operator /login page: customer auth doesn't use
// passwords at all — magic-link is the only credential. ADR-011
// reserved password auth for the operator dashboard.
export default function PortalLogin() {
  const [email, setEmail] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    if (!email.trim()) {
      setError('Email is required')
      return
    }
    setSubmitting(true)
    try {
      await requestMagicLink(email.trim())
      setSubmitted(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not request a login link')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="min-h-screen flex flex-col items-center justify-center px-4 bg-background">
      <div className="flex flex-col items-center mb-8">
        <VeloxLogo size="lg" />
        <p className="text-sm text-muted-foreground mt-2">Sign in to your billing portal</p>
      </div>

      <Card className="w-full max-w-[420px]">
        <CardContent className="p-6">
          {submitted ? (
            <div className="text-center space-y-3 py-4">
              <div className="mx-auto w-12 h-12 rounded-full bg-emerald-500/10 flex items-center justify-center">
                <Mail size={24} className="text-emerald-600 dark:text-emerald-400" />
              </div>
              <p className="text-sm font-medium text-foreground">Check your email</p>
              <p className="text-xs text-muted-foreground max-w-xs mx-auto">
                If an account exists for <span className="text-foreground">{email}</span>, we've sent a sign-in link.
                It expires in 15 minutes.
              </p>
              <button
                type="button"
                onClick={() => { setSubmitted(false); setEmail('') }}
                className="text-xs text-muted-foreground hover:text-foreground transition-colors mt-4"
              >
                Use a different email
              </button>
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
                  We'll email you a one-time sign-in link.
                </p>
              </div>

              {error && (
                <div className="px-3 py-2.5 rounded-lg bg-destructive/10 border border-destructive/20">
                  <p className="text-destructive text-sm">{error}</p>
                </div>
              )}

              <Button type="submit" disabled={submitting} className="w-full">
                {submitting ? <Loader2 size={16} className="animate-spin mr-2" /> : null}
                {submitting ? 'Sending link…' : 'Send sign-in link'}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>

      <p className="text-xs text-muted-foreground mt-6">
        Need help? <a href="mailto:support@velox.dev" className="text-foreground hover:underline">Contact support</a>
      </p>
    </div>
  )
}
