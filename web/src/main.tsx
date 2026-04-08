import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { getApiKey } from '@/lib/api'
import { ToastProvider } from '@/components/Toast'
import { LoginPage } from '@/pages/Login'
import { DashboardPage } from '@/pages/Dashboard'
import { CustomersPage } from '@/pages/Customers'
import { CustomerDetailPage } from '@/pages/CustomerDetail'
import { InvoicesPage } from '@/pages/Invoices'
import { InvoiceDetailPage } from '@/pages/InvoiceDetail'
import { SubscriptionsPage } from '@/pages/Subscriptions'
import { PricingPage } from '@/pages/Pricing'
import { PlanDetailPage } from '@/pages/PlanDetail'
import { MeterDetailPage } from '@/pages/MeterDetail'
import { CreditsPage } from '@/pages/Credits'
import { SettingsPage } from '@/pages/Settings'
import { SubscriptionDetailPage } from '@/pages/SubscriptionDetail'
import { DunningPage } from '@/pages/Dunning'
import { CreditNotesPage } from '@/pages/CreditNotes'
import { AuditLogPage } from '@/pages/AuditLog'
import { WebhooksPage } from '@/pages/Webhooks'
import { ApiKeysPage } from '@/pages/ApiKeys'
import { UsageEventsPage } from '@/pages/UsageEvents'
import './index.css'

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  if (!getApiKey()) {
    return <Navigate to="/login" replace />
  }
  return <>{children}</>
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ToastProvider>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
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
          <Route path="/credits" element={<ProtectedRoute><CreditsPage /></ProtectedRoute>} />
          <Route path="/dunning" element={<ProtectedRoute><DunningPage /></ProtectedRoute>} />
          <Route path="/credit-notes" element={<ProtectedRoute><CreditNotesPage /></ProtectedRoute>} />
          <Route path="/audit-log" element={<ProtectedRoute><AuditLogPage /></ProtectedRoute>} />
          <Route path="/webhooks" element={<ProtectedRoute><WebhooksPage /></ProtectedRoute>} />
          <Route path="/api-keys" element={<ProtectedRoute><ApiKeysPage /></ProtectedRoute>} />
          <Route path="/settings" element={<ProtectedRoute><SettingsPage /></ProtectedRoute>} />
        </Routes>
      </BrowserRouter>
    </ToastProvider>
  </StrictMode>,
)
