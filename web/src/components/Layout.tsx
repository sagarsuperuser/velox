import { Link, useLocation } from 'react-router-dom'
import {
  LayoutDashboard, Users, FileText, CreditCard, Tag, Wallet, LogOut, Settings,
  Receipt, AlertTriangle, ScrollText,
} from 'lucide-react'
import { cn } from '@/lib/cn'
import { clearApiKey } from '@/lib/api'

const billingNav = [
  { to: '/', icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/customers', icon: Users, label: 'Customers' },
  { to: '/invoices', icon: FileText, label: 'Invoices' },
  { to: '/subscriptions', icon: CreditCard, label: 'Subscriptions' },
]

const configNav = [
  { to: '/pricing', icon: Tag, label: 'Pricing' },
  { to: '/credits', icon: Wallet, label: 'Credits' },
  { to: '/credit-notes', icon: Receipt, label: 'Credit Notes' },
  { to: '/dunning', icon: AlertTriangle, label: 'Dunning' },
]

const bottomNav = [
  { to: '/audit-log', icon: ScrollText, label: 'Audit Log' },
  { to: '/settings', icon: Settings, label: 'Settings' },
]

function NavLink({ to, icon: Icon, label, pathname }: { to: string; icon: typeof LayoutDashboard; label: string; pathname: string }) {
  return (
    <Link
      to={to}
      className={cn(
        'flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors',
        pathname === to
          ? 'bg-white/10 text-white'
          : 'text-white/60 hover:text-white hover:bg-white/5'
      )}
    >
      <Icon size={18} />
      {label}
    </Link>
  )
}

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
          <p className="text-xs uppercase text-white/30 tracking-wider px-3 pt-2 pb-1">Billing</p>
          {billingNav.map(item => (
            <NavLink key={item.to} {...item} pathname={location.pathname} />
          ))}

          <p className="text-xs uppercase text-white/30 tracking-wider px-3 pt-4 pb-1">Configuration</p>
          {configNav.map(item => (
            <NavLink key={item.to} {...item} pathname={location.pathname} />
          ))}

          <div className="border-t border-white/10 my-2" />

          {bottomNav.map(item => (
            <NavLink key={item.to} {...item} pathname={location.pathname} />
          ))}
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
