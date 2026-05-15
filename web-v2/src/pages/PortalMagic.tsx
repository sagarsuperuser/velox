import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams, Link } from 'react-router-dom'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Loader2, AlertCircle } from 'lucide-react'
import { VeloxLogo } from '@/components/VeloxLogo'
import { consumeMagicLink } from '@/lib/portalAuth'

// PortalMagic is the landing page reached from the magic-link email.
// Reads ?token=… from the URL, exchanges it via /v1/public/customer-
// portal/magic/consume, stores the resulting portal session in
// localStorage, then redirects to /portal.
//
// Failures (unknown token, already-used, expired) collapse into a
// single "invalid or expired" error per ADR — never expose which.
export default function PortalMagic() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const [error, setError] = useState('')

  // consumeMagicLink is single-use server-side: a second POST with the
  // same token returns 401 "invalid or expired". React StrictMode (dev)
  // double-invokes useEffect on mount, so without the ref-guard the
  // second invocation would consume → fail → setError, surfacing the
  // "link not valid" UI even on a successful sign-in. Mark the token
  // as in-flight on first run so the second invoke short-circuits.
  const consumedRef = useRef(false)
  useEffect(() => {
    if (consumedRef.current) return
    const token = params.get('token') || ''
    if (!token) {
      setError('Sign-in link is missing the token. Request a new one from the login page.')
      return
    }
    consumedRef.current = true
    consumeMagicLink(token)
      .then(() => {
        navigate('/portal', { replace: true })
      })
      .catch(err => {
        setError(err instanceof Error ? err.message : 'Invalid or expired sign-in link.')
      })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className="min-h-screen flex flex-col items-center justify-center px-4 bg-background">
      <div className="flex flex-col items-center mb-8">
        <VeloxLogo size="lg" />
      </div>
      <Card className="w-full max-w-[420px]">
        <CardContent className="p-6">
          {error ? (
            <div className="text-center space-y-3 py-2">
              <div className="mx-auto w-12 h-12 rounded-full bg-destructive/10 flex items-center justify-center">
                <AlertCircle size={24} className="text-destructive" />
              </div>
              <p className="text-sm font-medium text-foreground">Sign-in link not valid</p>
              <p className="text-xs text-muted-foreground max-w-xs mx-auto">{error}</p>
              <Link to="/portal/login">
                <Button variant="outline" className="w-full mt-2">Request a new link</Button>
              </Link>
            </div>
          ) : (
            <div className="flex flex-col items-center py-6 space-y-3">
              <Loader2 size={28} className="animate-spin text-muted-foreground" />
              <p className="text-sm text-muted-foreground">Signing you in…</p>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
