import { StrictMode, lazy, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { getApiKey } from '@/lib/api'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { ToastProvider } from '@/components/Toast'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { LoginPage } from '@/pages/Login'
import { UpdatePaymentPage } from '@/pages/UpdatePayment'
import './index.css'

// Lazy-loaded pages
const DashboardPage = lazy(() => import('@/pages/Dashboard').then(m => ({ default: m.DashboardPage })))
const CustomersPage = lazy(() => import('@/pages/Customers').then(m => ({ default: m.CustomersPage })))
const CustomerDetailPage = lazy(() => import('@/pages/CustomerDetail').then(m => ({ default: m.CustomerDetailPage })))
const InvoicesPage = lazy(() => import('@/pages/Invoices').then(m => ({ default: m.InvoicesPage })))
const InvoiceDetailPage = lazy(() => import('@/pages/InvoiceDetail').then(m => ({ default: m.InvoiceDetailPage })))
const SubscriptionsPage = lazy(() => import('@/pages/Subscriptions').then(m => ({ default: m.SubscriptionsPage })))
const SubscriptionDetailPage = lazy(() => import('@/pages/SubscriptionDetail').then(m => ({ default: m.SubscriptionDetailPage })))
const PricingPage = lazy(() => import('@/pages/Pricing').then(m => ({ default: m.PricingPage })))
const PlanDetailPage = lazy(() => import('@/pages/PlanDetail').then(m => ({ default: m.PlanDetailPage })))
const MeterDetailPage = lazy(() => import('@/pages/MeterDetail').then(m => ({ default: m.MeterDetailPage })))
const CouponsPage = lazy(() => import('@/pages/Coupons').then(m => ({ default: m.CouponsPage })))
const CreditsPage = lazy(() => import('@/pages/Credits').then(m => ({ default: m.CreditsPage })))
const SettingsPage = lazy(() => import('@/pages/Settings').then(m => ({ default: m.SettingsPage })))
const DunningPage = lazy(() => import('@/pages/Dunning').then(m => ({ default: m.DunningPage })))
const CreditNotesPage = lazy(() => import('@/pages/CreditNotes').then(m => ({ default: m.CreditNotesPage })))
const AuditLogPage = lazy(() => import('@/pages/AuditLog').then(m => ({ default: m.AuditLogPage })))
const WebhooksPage = lazy(() => import('@/pages/Webhooks').then(m => ({ default: m.WebhooksPage })))
const ApiKeysPage = lazy(() => import('@/pages/ApiKeys').then(m => ({ default: m.ApiKeysPage })))
const UsageEventsPage = lazy(() => import('@/pages/UsageEvents').then(m => ({ default: m.UsageEventsPage })))

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  if (!getApiKey()) {
    return <Navigate to="/login" replace />
  }
  return <>{children}</>
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <ToastProvider>
        <BrowserRouter>
          <Suspense fallback={<div className="flex h-screen items-center justify-center"><LoadingSkeleton variant="detail" /></div>}>
            <Routes>
              <Route path="/login" element={<LoginPage />} />
              <Route path="/update-payment" element={<UpdatePaymentPage />} />
              <Route path="/" element={<ProtectedRoute><DashboardPage /></ProtectedRoute>} />
              <Route path="/customers" element={<ProtectedRoute><CustomersPage /></ProtectedRoute>} />
              <Route path="/customers/:id" element={<ProtectedRoute><CustomerDetailPage /></ProtectedRoute>} />
              <Route path="/invoices" element={<ProtectedRoute><InvoicesPage /></ProtectedRoute>} />
              <Route path="/invoices/:id" element={<ProtectedRoute><InvoiceDetailPage /></ProtectedRoute>} />
              <Route path="/subscriptions" element={<ProtectedRoute><SubscriptionsPage /></ProtectedRoute>} />
              <Route path="/subscriptions/:id" element={<ProtectedRoute><SubscriptionDetailPage /></ProtectedRoute>} />
              <Route path="/usage" element={<ProtectedRoute><UsageEventsPage /></ProtectedRoute>} />
              <Route path="/pricing" element={<ProtectedRoute><PricingPage /></ProtectedRoute>} />
              <Route path="/plans/:id" element={<ProtectedRoute><PlanDetailPage /></ProtectedRoute>} />
              <Route path="/meters/:id" element={<ProtectedRoute><MeterDetailPage /></ProtectedRoute>} />
              <Route path="/coupons" element={<ProtectedRoute><CouponsPage /></ProtectedRoute>} />
              <Route path="/credits" element={<ProtectedRoute><CreditsPage /></ProtectedRoute>} />
              <Route path="/dunning" element={<ProtectedRoute><DunningPage /></ProtectedRoute>} />
              <Route path="/credit-notes" element={<ProtectedRoute><CreditNotesPage /></ProtectedRoute>} />
              <Route path="/audit-log" element={<ProtectedRoute><AuditLogPage /></ProtectedRoute>} />
              <Route path="/webhooks" element={<ProtectedRoute><WebhooksPage /></ProtectedRoute>} />
              <Route path="/api-keys" element={<ProtectedRoute><ApiKeysPage /></ProtectedRoute>} />
              <Route path="/settings" element={<ProtectedRoute><SettingsPage /></ProtectedRoute>} />
            </Routes>
          </Suspense>
        </BrowserRouter>
      </ToastProvider>
    </ErrorBoundary>
  </StrictMode>,
)
