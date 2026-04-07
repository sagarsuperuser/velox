import { Link, useLocation } from 'react-router-dom'
import {
  LayoutDashboard, Users, FileText, CreditCard, Tag, Wallet, LogOut, Settings,
} from 'lucide-react'
import { cn } from '@/lib/cn'
import { clearApiKey } from '@/lib/api'

const nav = [
  { to: '/', icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/customers', icon: Users, label: 'Customers' },
  { to: '/invoices', icon: FileText, label: 'Invoices' },
  { to: '/subscriptions', icon: CreditCard, label: 'Subscriptions' },
  { to: '/pricing', icon: Tag, label: 'Pricing' },
  { to: '/credits', icon: Wallet, label: 'Credits' },
]

export function Layout({ children }: { children: React.ReactNode }) {
  const location = useLocation()

  return (
    <div className="flex h-screen">
      {/* Sidebar */}
      <aside className="w-60 bg-velox-900 text-white flex flex-col">
        <div className="p-5 border-b border-white/10">
          <h1 className="text-xl font-bold tracking-tight">Velox</h1>
          <p className="text-xs text-white/50 mt-0.5">Billing Dashboard</p>
        </div>

        <nav className="flex-1 p-3 space-y-1">
          {nav.map(({ to, icon: Icon, label }) => (
            <Link
              key={to}
              to={to}
              className={cn(
                'flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors',
                location.pathname === to
                  ? 'bg-white/10 text-white'
                  : 'text-white/60 hover:text-white hover:bg-white/5'
              )}
            >
              <Icon size={18} />
              {label}
            </Link>
          ))}

          <div className="border-t border-white/10 my-2" />

          <Link
            to="/settings"
            className={cn(
              'flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors',
              location.pathname === '/settings'
                ? 'bg-white/10 text-white'
                : 'text-white/60 hover:text-white hover:bg-white/5'
            )}
          >
            <Settings size={18} />
            Settings
          </Link>
        </nav>

        <div className="p-3 border-t border-white/10">
          <button
            onClick={() => { clearApiKey(); window.location.href = '/login' }}
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-sm text-white/60 hover:text-white hover:bg-white/5 w-full transition-colors"
          >
            <LogOut size={18} />
            Sign Out
          </button>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto">
        <div className="max-w-7xl mx-auto p-8">
          {children}
        </div>
      </main>
    </div>
  )
}
