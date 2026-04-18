import { useState, useEffect, useCallback, type ReactNode } from 'react'
import { Link, useLocation } from 'react-router-dom'
import {
  LayoutDashboard, Users, FileText, CreditCard, Tag, Wallet, LogOut, Settings,
  Receipt, AlertTriangle, ScrollText, Globe, Key, Menu, X, BarChart3, Ticket,
  Sun, Moon, Search, TrendingUp, type LucideIcon,
} from 'lucide-react'
import { useDarkMode } from '@/hooks/useDarkMode'
import { useKeyboardShortcuts } from '@/hooks/useKeyboardShortcuts'
import { cn } from '@/lib/utils'
import { api, setActiveCurrency } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { Badge } from '@/components/ui/badge'
import { CommandPalette } from '@/components/CommandPalette'
import { VeloxLogo } from '@/components/VeloxLogo'
import { KeyboardHelp } from '@/components/KeyboardHelp'

const billingNav = [
  { to: '/', icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/customers', icon: Users, label: 'Customers' },
  { to: '/invoices', icon: FileText, label: 'Invoices' },
  { to: '/subscriptions', icon: CreditCard, label: 'Subscriptions' },
  { to: '/usage', icon: BarChart3, label: 'Usage' },
  { to: '/analytics', icon: TrendingUp, label: 'Analytics' },
]

const configNav = [
  { to: '/pricing', icon: Tag, label: 'Pricing' },
  { to: '/coupons', icon: Ticket, label: 'Coupons' },
  { to: '/credits', icon: Wallet, label: 'Credits' },
  { to: '/credit-notes', icon: Receipt, label: 'Credit Notes' },
  { to: '/dunning', icon: AlertTriangle, label: 'Dunning' },
]

const systemNav = [
  { to: '/audit-log', icon: ScrollText, label: 'Audit Log' },
  { to: '/webhooks', icon: Globe, label: 'Webhooks' },
  { to: '/api-keys', icon: Key, label: 'API Keys' },
  { to: '/settings', icon: Settings, label: 'Settings' },
]

function NavLink({
  to, icon: Icon, label, pathname, onClick, count,
}: {
  to: string; icon: LucideIcon; label: string; pathname: string; onClick?: () => void; count?: number
}) {
  const active = pathname === to
  return (
    <div>
    <Tooltip>
      <TooltipTrigger asChild>
        <Link
          to={to}
          onClick={onClick}
          aria-current={active ? 'page' : undefined}
          className={cn(
            'flex items-center gap-3 px-3 py-2 rounded-md text-sm transition-all duration-150 relative',
            active
              ? 'bg-sidebar-accent text-sidebar-accent-foreground font-medium'
              : 'text-muted-foreground hover:text-foreground hover:bg-sidebar-accent/50 hover:translate-x-0.5'
          )}
        >
          {active && (
            <span className="absolute left-0 top-1.5 bottom-1.5 w-[2px] rounded-r bg-sidebar-primary" />
          )}
          <div className="flex items-center justify-between w-full">
            <div className="flex items-center gap-3">
              <Icon size={18} />
              {label}
            </div>
            {count != null && count > 0 && (
              <span className="text-[10px] font-medium bg-destructive text-destructive-foreground rounded-full px-1.5 py-0.5 min-w-[18px] text-center">
                {count}
              </span>
            )}
          </div>
        </Link>
      </TooltipTrigger>
      <TooltipContent side="right" className="md:hidden">
        {label}
      </TooltipContent>
    </Tooltip>
    </div>
  )
}

export function Layout({ children }: { children: ReactNode }) {
  const location = useLocation()
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [commandOpen, setCommandOpen] = useState(false)
  const [navCounts, setNavCounts] = useState<Record<string, number>>({})
  const { dark, toggle: toggleDark } = useDarkMode()
  useKeyboardShortcuts()

  // Cmd+K / Ctrl+K keyboard shortcut
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        setCommandOpen(prev => !prev)
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  // Load tenant currency once on mount
  useEffect(() => {
    api.getSettings().then(s => {
      if (s.default_currency) setActiveCurrency(s.default_currency)
    }).catch(() => { /* ignore */ })
  }, [])

  // Fetch nav badge counts on mount
  useEffect(() => {
    api.getAnalyticsOverview().then(ov => {
      const counts: Record<string, number> = {}
      if (ov.open_invoices > 0) counts['/invoices'] = ov.open_invoices
      if (ov.dunning_active > 0) counts['/dunning'] = ov.dunning_active
      setNavCounts(counts)
    }).catch(() => {})
  }, [])

  const closeSidebar = () => setSidebarOpen(false)

  const sidebarContent = (
    <>
      {/* Header */}
      <div className="p-4 border-b border-border flex items-center justify-between">
        <VeloxLogo size="sm" />
        <button
          onClick={closeSidebar}
          aria-label="Close menu"
          className="md:hidden text-muted-foreground hover:text-foreground"
        >
          <X size={20} />
        </button>
      </div>

      {/* Search trigger */}
      <div className="px-3 pt-3">
        <button
          onClick={() => setCommandOpen(true)}
          className="w-full flex items-center gap-2 px-3 py-2 bg-muted rounded-md text-sm text-muted-foreground hover:bg-accent transition-colors"
        >
          <Search size={14} />
          <span className="flex-1 text-left">Search...</span>
          <kbd className="text-[11px] bg-background px-1.5 py-0.5 rounded border border-border font-medium text-muted-foreground">
            {navigator.platform?.includes('Mac') ? '\u2318' : 'Ctrl+'}K
          </kbd>
        </button>
      </div>

      {/* Navigation */}
      <nav aria-label="Main navigation" className="flex-1 p-3 space-y-1 overflow-y-auto">
        <p className="text-xs uppercase text-muted-foreground tracking-wider px-3 pt-2 pb-1">
          Billing
        </p>
        {billingNav.map(item => (
          <NavLink key={item.to} {...item} pathname={location.pathname} onClick={closeSidebar} count={navCounts[item.to]} />
        ))}

        <p className="text-xs uppercase text-muted-foreground tracking-wider px-3 pt-4 pb-1">
          Configuration
        </p>
        {configNav.map(item => (
          <NavLink key={item.to} {...item} pathname={location.pathname} onClick={closeSidebar} count={navCounts[item.to]} />
        ))}

        <Separator className="my-2" />

        <p className="text-xs uppercase text-muted-foreground tracking-wider px-3 pt-2 pb-1">
          System
        </p>
        {systemNav.map(item => (
          <NavLink key={item.to} {...item} pathname={location.pathname} onClick={closeSidebar} count={navCounts[item.to]} />
        ))}
      </nav>

      {/* Footer */}
      <div className="p-3 border-t border-border space-y-1">
        <div className="px-3 pb-2">
          <Badge variant="outline" className="text-[10px] tracking-wide border-primary/30 text-primary">
            v2.0
          </Badge>
        </div>
        <Button
          variant="ghost"
          className="w-full justify-start gap-3 text-muted-foreground hover:text-foreground"
          onClick={toggleDark}
        >
          {dark ? <Sun size={18} /> : <Moon size={18} />}
          {dark ? 'Light Mode' : 'Dark Mode'}
        </Button>
        <Button
          variant="ghost"
          className="w-full justify-start gap-3 text-muted-foreground hover:text-foreground"
          onClick={async () => {
            await fetch('/v1/auth/logout', { method: 'POST', credentials: 'same-origin' }).catch(() => {})
            window.location.href = '/login'
          }}
        >
          <LogOut size={18} />
          Sign Out
        </Button>
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
      <aside className="hidden md:flex w-60 bg-card border-r border-border flex-col shrink-0">
        {sidebarContent}
      </aside>

      {/* Sidebar - mobile */}
      <aside
        className={cn(
          'fixed inset-y-0 left-0 z-40 w-60 bg-card border-r border-border flex flex-col transition-transform duration-200 md:hidden',
          sidebarOpen ? 'translate-x-0' : '-translate-x-full'
        )}
      >
        {sidebarContent}
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto bg-background">
        {/* Mobile header */}
        <div className="md:hidden flex items-center gap-3 px-4 py-3 border-b border-border bg-card sticky top-0 z-20">
          <button
            onClick={() => setSidebarOpen(true)}
            aria-label="Open menu"
            className="text-muted-foreground hover:text-foreground"
          >
            <Menu size={22} />
          </button>
          <VeloxLogo size="sm" />
        </div>
        <div className="max-w-7xl mx-auto p-4 md:p-8">
          {children}
        </div>
      </main>

      {/* Command Palette */}
      <CommandPalette open={commandOpen} onClose={() => setCommandOpen(false)} />

      {/* Keyboard Shortcuts Help */}
      <KeyboardHelp />
    </div>
  )
}
