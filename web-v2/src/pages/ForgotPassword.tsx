import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Zap, Loader2, ArrowLeft } from 'lucide-react'

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
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-gray-50 via-white to-[#F7F7FF] dark:from-gray-950 dark:via-gray-900 dark:to-[#1A1523] relative overflow-hidden">
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
          <p className="text-muted-foreground mt-1">Reset your password</p>
        </div>

        <Card className="shadow-lg">
          <CardHeader className="pb-4">
            <CardTitle className="text-base">Forgot password</CardTitle>
            <CardDescription>
              {submitted
                ? 'Check your email for a reset link'
                : 'Enter your email and we\'ll send a reset link'}
            </CardDescription>
          </CardHeader>
          <CardContent>
            {submitted ? (
              <div className="space-y-4">
                <div className="px-3 py-2 rounded-md bg-green-50 border border-green-200 dark:bg-green-900/20 dark:border-green-800">
                  <p className="text-green-700 dark:text-green-300 text-xs font-medium">
                    If an account exists with that email, a password reset link has been sent.
                  </p>
                </div>
                <p className="text-xs text-muted-foreground text-center">
                  In development, the reset token is logged to the server console.
                </p>
                <Link to="/login">
                  <Button variant="outline" className="w-full">
                    <ArrowLeft size={14} className="mr-2" />
                    Back to sign in
                  </Button>
                </Link>
              </div>
            ) : (
              <form onSubmit={handleSubmit} noValidate className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="email">Email</Label>
                  <Input
                    id="email"
                    type="email"
                    value={email}
                    onChange={e => setEmail(e.target.value)}
                    placeholder="admin@velox.dev"
                    autoFocus
                    autoComplete="email"
                  />
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
                      Sending...
                    </>
                  ) : (
                    'Send Reset Link'
                  )}
                </Button>

                <Link to="/login" className="block text-center text-xs text-muted-foreground hover:text-foreground transition-colors underline underline-offset-2">
                  Back to sign in
                </Link>
              </form>
            )}
          </CardContent>
        </Card>

        <p className="text-center text-xs text-muted-foreground/50 mt-6">
          Powered by Velox
        </p>
      </div>
    </div>
  )
}
