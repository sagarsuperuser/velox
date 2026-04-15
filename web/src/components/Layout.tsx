import { useState, useEffect } from 'react'
import { Link, useLocation } from 'react-router-dom'
import {
  LayoutDashboard, Users, FileText, CreditCard, Tag, Wallet, LogOut, Settings,
  Receipt, AlertTriangle, ScrollText, Globe, Key, Menu, X, BarChart3, Ticket,
  Sun, Moon,
} from 'lucide-react'
import { useDarkMode } from '@/hooks/useDarkMode'
import { cn } from '@/lib/cn'
import { clearApiKey, api, setActiveCurrency } from '@/lib/api'

const billingNav = [
  { to: '/', icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/customers', icon: Users, label: 'Customers' },
  { to: '/invoices', icon: FileText, label: 'Invoices' },
  { to: '/subscriptions', icon: CreditCard, label: 'Subscriptions' },
  { to: '/usage', icon: BarChart3, label: 'Usage' },
]

const configNav = [
  { to: '/pricing', icon: Tag, label: 'Pricing' },
  { to: '/coupons', icon: Ticket, label: 'Coupons' },
  { to: '/credits', icon: Wallet, label: 'Credits' },
  { to: '/credit-notes', icon: Receipt, label: 'Credit Notes' },
  { to: '/dunning', icon: AlertTriangle, label: 'Dunning' },
]

const bottomNav = [
  { to: '/audit-log', icon: ScrollText, label: 'Audit Log' },
  { to: '/webhooks', icon: Globe, label: 'Webhooks' },
  { to: '/api-keys', icon: Key, label: 'API Keys' },
  { to: '/settings', icon: Settings, label: 'Settings' },
]

function NavLink({ to, icon: Icon, label, pathname, onClick }: { to: string; icon: typeof LayoutDashboard; label: string; pathname: string; onClick?: () => void }) {
  return (
    <Link
      to={to}
      onClick={onClick}
      aria-current={pathname === to ? 'page' : undefined}
      className={cn(
        'flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors',
        pathname === to
          ? 'bg-velox-50 text-velox-700 font-medium dark:bg-velox-900/20 dark:text-velox-300'
          : 'text-gray-500 hover:text-gray-900 hover:bg-gray-50 dark:text-gray-400 dark:hover:text-gray-100 dark:hover:bg-gray-800'
      )}
    >
      <Icon size={18} />
      {label}
    </Link>
  )
}

export function Layout({ children }: { children: React.ReactNode }) {
  const location = useLocation()
  const [sidebarOpen, setSidebarOpen] = useState(false)

  // Load tenant currency once on mount
  useEffect(() => {
    api.getSettings().then(s => {
      if (s.default_currency) setActiveCurrency(s.default_currency)
    }).catch(() => {})
  }, [])

  const { dark, toggle: toggleDark } = useDarkMode()
  const closeSidebar = () => setSidebarOpen(false)

  const sidebarContent = (
    <>
      <div className="p-5 border-b border-gray-100 dark:border-gray-800 flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold tracking-tight text-gray-900 dark:text-gray-100">Velox</h1>
          <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">Billing Dashboard</p>
        </div>
        <button onClick={closeSidebar} aria-label="Close menu" className="md:hidden text-gray-400 hover:text-gray-600 dark:hover:text-gray-300">
          <X size={20} />
        </button>
      </div>

      <nav aria-label="Main navigation" className="flex-1 p-3 space-y-1">
        <p className="text-xs uppercase text-gray-500 dark:text-gray-500 tracking-wider px-3 pt-2 pb-1">Billing</p>
        {billingNav.map(item => (
          <NavLink key={item.to} {...item} pathname={location.pathname} onClick={closeSidebar} />
        ))}

        <p className="text-xs uppercase text-gray-500 dark:text-gray-500 tracking-wider px-3 pt-4 pb-1">Configuration</p>
        {configNav.map(item => (
          <NavLink key={item.to} {...item} pathname={location.pathname} onClick={closeSidebar} />
        ))}

        <div className="border-t border-gray-100 dark:border-gray-800 my-2" />

        {bottomNav.map(item => (
          <NavLink key={item.to} {...item} pathname={location.pathname} onClick={closeSidebar} />
        ))}
      </nav>

      <div className="p-3 border-t border-gray-100 dark:border-gray-800 space-y-1">
        <button
          onClick={toggleDark}
          className="flex items-center gap-3 px-3 py-2 rounded-lg text-sm text-gray-600 hover:text-gray-900 hover:bg-gray-50 dark:text-gray-400 dark:hover:text-gray-100 dark:hover:bg-gray-800 w-full transition-colors"
        >
          {dark ? <Sun size={18} /> : <Moon size={18} />}
          {dark ? 'Light Mode' : 'Dark Mode'}
        </button>
        <button
          onClick={() => { clearApiKey(); window.location.href = '/login' }}
          className="flex items-center gap-3 px-3 py-2 rounded-lg text-sm text-gray-600 hover:text-gray-900 hover:bg-gray-50 dark:text-gray-400 dark:hover:text-gray-100 dark:hover:bg-gray-800 w-full transition-colors"
        >
          <LogOut size={18} />
          Sign Out
        </button>
      </div>
    </>
  )

  return (
    <div className="flex h-screen">
      {/* Mobile overlay */}
      {sidebarOpen && (
        <div
          className="fixed inset-0 bg-black/20 backdrop-blur-sm z-30 md:hidden"
          onClick={closeSidebar}
        />
      )}

      {/* Sidebar - desktop */}
      <aside className="hidden md:flex w-60 bg-white dark:bg-gray-900 border-r border-gray-200 dark:border-gray-800 flex-col flex-shrink-0">
        {sidebarContent}
      </aside>

      {/* Sidebar - mobile */}
      <aside
        className={cn(
          'fixed inset-y-0 left-0 z-40 w-60 bg-white dark:bg-gray-900 border-r border-gray-200 dark:border-gray-800 flex flex-col transition-transform duration-200 md:hidden',
          sidebarOpen ? 'translate-x-0' : '-translate-x-full'
        )}
      >
        {sidebarContent}
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto dark:bg-gray-950">
        {/* Mobile header */}
        <div className="md:hidden flex items-center gap-3 px-4 py-3 border-b border-gray-200 dark:border-gray-800 bg-white dark:bg-gray-900 sticky top-0 z-20">
          <button onClick={() => setSidebarOpen(true)} aria-label="Open menu" className="text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-100">
            <Menu size={22} />
          </button>
          <h1 className="text-lg font-bold tracking-tight text-velox-900 dark:text-gray-100">Velox</h1>
        </div>
        <div className="max-w-7xl mx-auto p-4 md:p-8">
          {children}
        </div>
      </main>
    </div>
  )
}
