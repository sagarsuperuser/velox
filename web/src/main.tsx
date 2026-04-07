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
import { CreditsPage } from '@/pages/Credits'
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
          <Route path="/pricing" element={<ProtectedRoute><PricingPage /></ProtectedRoute>} />
          <Route path="/credits" element={<ProtectedRoute><CreditsPage /></ProtectedRoute>} />
        </Routes>
      </BrowserRouter>
    </ToastProvider>
  </StrictMode>,
)
