import { useSearchParams, Link } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { CheckCircle2 } from 'lucide-react'

export default function CheckoutSuccessPage() {
  const [searchParams] = useSearchParams()
  const sessionId = searchParams.get('session_id')
  const customerId = searchParams.get('customer_id')

  return (
    <div className="min-h-screen bg-background flex items-center justify-center p-4">
      <Card className="w-full max-w-md">
        <CardContent className="p-8 text-center space-y-4">
          <div className="w-16 h-16 rounded-full bg-emerald-50 dark:bg-emerald-500/10 flex items-center justify-center mx-auto">
            <CheckCircle2 size={64} className="text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
          </div>
          <h1 className="text-xl font-semibold text-foreground">Payment successful</h1>
          <p className="text-sm text-muted-foreground">
            Your payment was processed successfully. You can close this tab or return to the dashboard.
          </p>
          {sessionId && (
            <p className="text-xs font-mono text-muted-foreground">Reference: {sessionId}</p>
          )}
          <div className="flex flex-col sm:flex-row gap-2 pt-2">
            <Button asChild size="lg" className="flex-1">
              <Link to="/">Return to dashboard</Link>
            </Button>
            {customerId && (
              <Button asChild variant="outline" size="lg" className="flex-1">
                <Link to={`/customers/${customerId}`}>View customer</Link>
              </Button>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
