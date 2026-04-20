import { StrictMode, lazy, Suspense, Component } from 'react'
import type { ReactNode, ErrorInfo } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getApiKey } from '@/lib/api'
import '@fontsource-variable/geist'
import '@fontsource-variable/geist-mono'
import './index.css'

// Error Boundary
class ErrorBoundary extends Component<
  { children: ReactNode },
  { hasError: boolean; error?: Error }
> {
  state = { hasError: false, error: undefined as Error | undefined }

  static getDerivedStateFromError(error: Error) {
    return { hasError: true, error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('ErrorBoundary:', error, info)
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="min-h-screen flex items-center justify-center p-8">
          <div className="text-center max-w-md">
            <h1 className="text-lg font-semibold mb-2 text-foreground">Something went wrong</h1>
            <p className="text-sm text-muted-foreground mb-4">{this.state.error?.message}</p>
            <button
              onClick={() => {
                this.setState({ hasError: false })
                window.location.href = '/'
              }}
              className="px-4 py-2 bg-primary text-primary-foreground rounded-lg text-sm"
            >
              Return to Dashboard
            </button>
          </div>
        </div>
      )
    }
    return this.props.children
  }
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      retry: 1,
      refetchOnWindowFocus: true,
      refetchOnMount: true,
    },
  },
})

function ProtectedRoute({ children }: { children: ReactNode }) {
  if (!getApiKey()) {
    return <Navigate to="/login" replace />
  }
  return <>{children}</>
}

// Lazy load pages
const LoginPage = lazy(() => import('@/pages/Login'))
const DashboardPage = lazy(() => import('@/pages/Dashboard'))
const CustomersPage = lazy(() => import('@/pages/Customers'))
const PricingPage = lazy(() => import('@/pages/Pricing'))
const DunningPage = lazy(() => import('@/pages/Dunning'))
const WebhooksPage = lazy(() => import('@/pages/Webhooks'))
const ApiKeysPage = lazy(() => import('@/pages/ApiKeys'))
const SettingsPage = lazy(() => import('@/pages/Settings'))
const InvoicesPage = lazy(() => import('@/pages/Invoices'))
const SubscriptionsPage = lazy(() => import('@/pages/Subscriptions'))
const UsageEventsPage = lazy(() => import('@/pages/UsageEvents'))
const CouponsPage = lazy(() => import('@/pages/Coupons'))
const CreditsPage = lazy(() => import('@/pages/Credits'))
const CreditNotesPage = lazy(() => import('@/pages/CreditNotes'))
const AnalyticsPage = lazy(() => import('@/pages/Analytics'))
const AuditLogPage = lazy(() => import('@/pages/AuditLog'))
const UpdatePaymentPage = lazy(() => import('@/pages/UpdatePayment'))
const CustomerPortalPage = lazy(() => import('@/pages/CustomerPortal'))
const CustomerDetailPage = lazy(() => import('@/pages/CustomerDetail'))
const InvoiceDetailPage = lazy(() => import('@/pages/InvoiceDetail'))
const SubscriptionDetailPage = lazy(() => import('@/pages/SubscriptionDetail'))
const PlanDetailPage = lazy(() => import('@/pages/PlanDetail'))
const MeterDetailPage = lazy(() => import('@/pages/MeterDetail'))

const App = () => (
  <ErrorBoundary>
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <BrowserRouter>
          <Suspense
            fallback={
              <div className="min-h-screen flex items-center justify-center">
                <div className="animate-spin h-8 w-8 border-2 border-primary border-t-transparent rounded-full" />
              </div>
            }
          >
            <Routes>
              <Route path="/login" element={<LoginPage />} />
              <Route path="/" element={<ProtectedRoute><DashboardPage /></ProtectedRoute>} />
              <Route path="/customers" element={<ProtectedRoute><CustomersPage /></ProtectedRoute>} />
              <Route path="/pricing" element={<ProtectedRoute><PricingPage /></ProtectedRoute>} />
              <Route path="/dunning" element={<ProtectedRoute><DunningPage /></ProtectedRoute>} />
              <Route path="/webhooks" element={<ProtectedRoute><WebhooksPage /></ProtectedRoute>} />
              <Route path="/api-keys" element={<ProtectedRoute><ApiKeysPage /></ProtectedRoute>} />
              <Route path="/invoices" element={<ProtectedRoute><InvoicesPage /></ProtectedRoute>} />
              <Route path="/subscriptions" element={<ProtectedRoute><SubscriptionsPage /></ProtectedRoute>} />
              <Route path="/usage" element={<ProtectedRoute><UsageEventsPage /></ProtectedRoute>} />
              <Route path="/analytics" element={<ProtectedRoute><AnalyticsPage /></ProtectedRoute>} />
              <Route path="/coupons" element={<ProtectedRoute><CouponsPage /></ProtectedRoute>} />
              <Route path="/credits" element={<ProtectedRoute><CreditsPage /></ProtectedRoute>} />
              <Route path="/credit-notes" element={<ProtectedRoute><CreditNotesPage /></ProtectedRoute>} />
              <Route path="/audit-log" element={<ProtectedRoute><AuditLogPage /></ProtectedRoute>} />
              <Route path="/settings" element={<ProtectedRoute><SettingsPage /></ProtectedRoute>} />
              <Route path="/customers/:id" element={<ProtectedRoute><CustomerDetailPage /></ProtectedRoute>} />
              <Route path="/invoices/:id" element={<ProtectedRoute><InvoiceDetailPage /></ProtectedRoute>} />
              <Route path="/subscriptions/:id" element={<ProtectedRoute><SubscriptionDetailPage /></ProtectedRoute>} />
              <Route path="/plans/:id" element={<ProtectedRoute><PlanDetailPage /></ProtectedRoute>} />
              <Route path="/meters/:id" element={<ProtectedRoute><MeterDetailPage /></ProtectedRoute>} />
              <Route path="/update-payment" element={<UpdatePaymentPage />} />
              <Route path="/customer-portal" element={<CustomerPortalPage />} />
            </Routes>
          </Suspense>
        </BrowserRouter>
        <Toaster position="bottom-right" richColors closeButton />
      </TooltipProvider>
    </QueryClientProvider>
  </ErrorBoundary>
)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
