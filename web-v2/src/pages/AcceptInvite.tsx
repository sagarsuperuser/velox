import { useEffect, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Loader2, Eye, EyeOff, AlertCircle, ArrowLeft, Mail, Building2 } from 'lucide-react'

import { useAuth } from '@/contexts/AuthContext'
import { ApiError } from '@/lib/api'
import { membersApi, type InvitePreview } from '@/lib/members'

import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { VeloxLogo } from '@/components/VeloxLogo'

export default function AcceptInvitePage() {
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''
  const navigate = useNavigate()
  const { refresh } = useAuth()

  const [preview, setPreview] = useState<InvitePreview | null>(null)
  const [loadingPreview, setLoadingPreview] = useState(true)
  const [previewError, setPreviewError] = useState('')

  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState('')

  useEffect(() => {
    if (!token) {
      setLoadingPreview(false)
      return
    }
    let cancelled = false
    membersApi.previewInvite(token)
      .then(p => { if (!cancelled) setPreview(p) })
      .catch(err => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setPreviewError('This invitation is invalid, expired, or has already been used.')
        } else {
          setPreviewError(err instanceof Error ? err.message : 'Cannot load invitation')
        }
      })
      .finally(() => { if (!cancelled) setLoadingPreview(false) })
    return () => { cancelled = true }
  }, [token])

  // No token in URL — broken link, not a form-validation error.
  if (!token) {
    return <InvalidInvite message="This link is missing its invitation token." />
  }

  if (loadingPreview) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <Loader2 size={24} className="animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (previewError || !preview) {
    return <InvalidInvite message={previewError || 'Invitation not found.'} />
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitError('')

    if (!password) {
      setSubmitError(preview.needs_new_account ? 'Choose a password' : 'Enter your existing password')
      return
    }
    if (preview.needs_new_account) {
      if (password.length < 8) {
        setSubmitError('Password must be at least 8 characters')
        return
      }
      if (password !== confirm) {
        setSubmitError('Passwords do not match')
        return
      }
    }

    setSubmitting(true)
    try {
      await membersApi.acceptInvite(
        token,
        password,
        preview.needs_new_account ? displayName.trim() : '',
      )
      // acceptInvite sets the session cookie server-side; refresh pulls
      // the new identity into AuthContext so ProtectedRoute lets us in.
      await refresh()
      navigate('/', { replace: true })
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 401) {
          setSubmitError('Wrong password for this account')
        } else if (err.status === 404) {
          setSubmitError('This invitation is invalid, expired, or has already been used')
        } else {
          setSubmitError(err.message)
        }
      } else {
        setSubmitError('Cannot connect to Velox API')
      }
    } finally {
      setSubmitting(false)
    }
  }

  const workspace = preview.tenant_name || preview.tenant_id

  return (
    <div className="min-h-screen flex flex-col items-center justify-center px-4 bg-background">
      <div className="flex flex-col items-center mb-8">
        <VeloxLogo size="lg" />
        <p className="text-sm text-muted-foreground mt-2">
          {preview.needs_new_account ? 'Create your account' : 'Join the workspace'}
        </p>
      </div>

      <Card className="w-full max-w-[380px]">
        <CardContent className="p-6 space-y-4">
          {/* Invitation header */}
          <div className="rounded-lg border border-border bg-muted/40 px-4 py-3 space-y-2">
            <div className="flex items-center gap-2 text-sm">
              <Building2 size={14} className="text-muted-foreground" />
              <span className="text-muted-foreground">Workspace</span>
              <span className="font-medium text-foreground ml-auto truncate" title={workspace}>
                {workspace}
              </span>
            </div>
            <div className="flex items-center gap-2 text-sm">
              <Mail size={14} className="text-muted-foreground" />
              <span className="text-muted-foreground">Invited</span>
              <span className="font-medium text-foreground ml-auto truncate" title={preview.email}>
                {preview.email}
              </span>
            </div>
            {preview.invited_by_email && (
              <p className="text-xs text-muted-foreground">
                Invited by {preview.invited_by_email}
              </p>
            )}
          </div>

          <form onSubmit={handleSubmit} noValidate className="space-y-4">
            {preview.needs_new_account && (
              <div className="space-y-1.5">
                <Label htmlFor="name">Your name <span className="text-muted-foreground">(optional)</span></Label>
                <Input
                  id="name"
                  type="text"
                  value={displayName}
                  onChange={e => setDisplayName(e.target.value)}
                  placeholder="Jane Doe"
                  autoComplete="name"
                />
              </div>
            )}

            <div className="space-y-1.5">
              <Label htmlFor="password">
                {preview.needs_new_account ? 'Choose a password' : 'Your existing password'}
              </Label>
              <div className="relative">
                <Input
                  id="password"
                  type={showPassword ? 'text' : 'password'}
                  value={password}
                  onChange={e => setPassword(e.target.value)}
                  placeholder={preview.needs_new_account ? 'At least 8 characters' : 'Enter your password'}
                  autoComplete={preview.needs_new_account ? 'new-password' : 'current-password'}
                  autoFocus
                  className="pr-10"
                />
                <button
                  type="button"
                  tabIndex={-1}
                  onClick={() => setShowPassword(!showPassword)}
                  aria-label={showPassword ? 'Hide password' : 'Show password'}
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
                >
                  {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
                </button>
              </div>
              {!preview.needs_new_account && (
                <p className="text-xs text-muted-foreground">
                  We found an existing account for {preview.email}. Sign in with that password to join.
                </p>
              )}
            </div>

            {preview.needs_new_account && (
              <div className="space-y-1.5">
                <Label htmlFor="confirm">Confirm password</Label>
                <Input
                  id="confirm"
                  type={showPassword ? 'text' : 'password'}
                  value={confirm}
                  onChange={e => setConfirm(e.target.value)}
                  placeholder="Re-enter password"
                  autoComplete="new-password"
                />
              </div>
            )}

            {submitError && (
              <div className="px-3 py-2.5 rounded-lg bg-destructive/10 border border-destructive/20">
                <p className="text-destructive text-sm">{submitError}</p>
              </div>
            )}

            <Button type="submit" disabled={submitting} className="w-full">
              {submitting && <Loader2 size={16} className="animate-spin mr-2" />}
              {submitting
                ? 'Accepting...'
                : preview.needs_new_account
                  ? 'Create account & join'
                  : 'Sign in & join workspace'}
            </Button>

            {!preview.needs_new_account && (
              <Link
                to="/forgot-password"
                className="flex items-center justify-center text-sm text-muted-foreground hover:text-foreground transition-colors"
              >
                Forgot your password?
              </Link>
            )}
          </form>
        </CardContent>
      </Card>
    </div>
  )
}

function InvalidInvite({ message }: { message: string }) {
  return (
    <div className="min-h-screen flex flex-col items-center justify-center px-4 bg-background">
      <div className="flex flex-col items-center mb-8">
        <VeloxLogo size="lg" />
        <p className="text-sm text-muted-foreground mt-2">Invitation</p>
      </div>
      <Card className="w-full max-w-[380px]">
        <CardContent className="p-6 flex flex-col items-center text-center space-y-4">
          <AlertCircle size={40} className="text-destructive" />
          <h2 className="text-base font-semibold">Invitation unavailable</h2>
          <p className="text-sm text-muted-foreground">{message}</p>
          <p className="text-xs text-muted-foreground">
            Ask whoever invited you to send a fresh invitation.
          </p>
          <Link to="/login" className="w-full">
            <Button variant="outline" className="w-full">
              <ArrowLeft size={14} className="mr-2" />
              Back to sign in
            </Button>
          </Link>
        </CardContent>
      </Card>
    </div>
  )
}
