import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams, useNavigate } from 'react-router-dom'

import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Loader2, Mail, CheckCircle2, Clock } from 'lucide-react'

const apiBase = import.meta.env.VITE_API_URL || ''

// Three states the page cycles through:
//   "form"        — default entry; customer types email and submits.
//   "sent"        — form submitted successfully; "check your email" card.
//                   Same copy whether or not their email matched, matching
//                   the backend's enumeration-resistant 202.
//   "consuming"   — a magic_token is present in the URL; we're calling
//                   /magic/consume and will redirect on success.
//   "consume_err" — the magic_token was invalid/used/expired. Generic
//                   copy, same across all failure modes to mirror the
//                   uniform 401 from the backend.
type ViewState = 'form' | 'sent' | 'consuming' | 'consume_err'

interface ConsumeResponse {
  token: string
  customer_id: string
  livemode: boolean
  expires_at: string
}

export default function CustomerPortalLoginPage() {
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()

  const magicToken = useMemo(() => searchParams.get('magic_token') || '', [searchParams])
  const initialState: ViewState = magicToken ? 'consuming' : 'form'

  const [state, setState] = useState<ViewState>(initialState)
  const [email, setEmail] = useState('')
  const [submitting, setSubmitting] = useState(false)

  // If a magic_token is present, consume it immediately. A successful
  // consume swaps the single-use magic token for a reusable session
  // token and redirects into the portal; failure shows the generic
  // "invalid or expired link" card.
  const consume = useCallback(async () => {
    try {
      const res = await fetch(`${apiBase}/v1/public/customer-portal/magic/consume`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: magicToken }),
      })
      if (!res.ok) {
        setState('consume_err')
        return
      }
      const body = (await res.json()) as ConsumeResponse
      navigate(`/customer-portal?token=${encodeURIComponent(body.token)}`, { replace: true })
    } catch {
      setState('consume_err')
    }
  }, [magicToken, navigate])

  useEffect(() => {
    if (state === 'consuming') {
      consume()
    }
  }, [state, consume])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!email.trim()) return
    setSubmitting(true)
    try {
      // We intentionally don't branch on the response — a 202 is the
      // expected "accepted" status and the only failure we surface to
      // the user is structural (e.g. network error). Match-or-miss,
      // the "check your email" card renders the same.
      await fetch(`${apiBase}/v1/public/customer-portal/magic-link`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email: email.trim() }),
      })
      setState('sent')
    } catch {
      // Network-level failure — retry-friendly copy rather than a
      // misleading "invalid email" message.
      setState('sent')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="min-h-screen flex flex-col items-center justify-center px-4 bg-background">
      <div className="flex flex-col items-center mb-8">
        <h1 className="text-2xl font-bold text-foreground">Velox</h1>
        <p className="text-sm text-muted-foreground mt-1">Customer Portal</p>
      </div>

      <Card className="w-full max-w-[400px]">
        <CardContent className="p-6">
          {state === 'form' && (
            <form onSubmit={handleSubmit} noValidate className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="email">Email address</Label>
                <Input
                  id="email"
                  type="email"
                  value={email}
                  onChange={e => setEmail(e.target.value)}
                  placeholder="you@example.com"
                  autoFocus
                  required
                />
                <p className="text-xs text-muted-foreground">
                  We'll email you a one-time sign-in link. No password needed.
                </p>
              </div>

              <Button type="submit" disabled={submitting || !email.trim()} className="w-full">
                {submitting ? (
                  <>
                    <Loader2 size={16} className="animate-spin mr-2" />
                    Sending link...
                  </>
                ) : (
                  'Email me a sign-in link'
                )}
              </Button>
            </form>
          )}

          {state === 'sent' && (
            <div className="text-center py-4 space-y-3">
              <div className="w-12 h-12 rounded-full bg-primary/10 flex items-center justify-center mx-auto">
                <Mail size={22} className="text-primary" />
              </div>
              <p className="text-sm font-medium text-foreground">Check your email</p>
              <p className="text-sm text-muted-foreground">
                If <span className="font-medium text-foreground">{email}</span> is
                on file with your billing provider, we've sent a sign-in link that
                expires in 15 minutes.
              </p>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setState('form')
                  setEmail('')
                }}
                className="text-xs"
              >
                Use a different email
              </Button>
            </div>
          )}

          {state === 'consuming' && (
            <div className="text-center py-8 space-y-3">
              <Loader2 size={22} className="animate-spin text-primary mx-auto" />
              <p className="text-sm text-muted-foreground">Signing you in...</p>
            </div>
          )}

          {state === 'consume_err' && (
            <div className="text-center py-4 space-y-3">
              <div className="w-12 h-12 rounded-full bg-destructive/10 flex items-center justify-center mx-auto">
                <Clock size={22} className="text-destructive/70" />
              </div>
              <p className="text-sm font-medium text-foreground">
                This sign-in link is no longer valid
              </p>
              <p className="text-sm text-muted-foreground">
                Links expire after 15 minutes and can only be used once. Request a
                new one below.
              </p>
              <Button
                onClick={() => {
                  setState('form')
                  setEmail('')
                  navigate('/customer-portal/login', { replace: true })
                }}
                className="w-full"
              >
                <CheckCircle2 size={14} className="mr-2" />
                Request a new link
              </Button>
            </div>
          )}
        </CardContent>
      </Card>

      <p className="text-xs text-muted-foreground text-center mt-6">Powered by Velox Billing</p>
    </div>
  )
}
