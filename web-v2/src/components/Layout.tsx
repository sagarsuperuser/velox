import { useState, useEffect, type ReactNode } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import {
  LayoutDashboard, Users, FileText, CreditCard, Tag, Wallet, LogOut, Settings,
  Receipt, AlertTriangle, ScrollText, Globe, Key, Menu, X, BarChart3, Ticket,
  Sun, Moon, Search, TrendingUp, UsersRound, ChevronsUpDown, type LucideIcon,
} from 'lucide-react'
import { toast } from 'sonner'
import { useDarkMode } from '@/hooks/useDarkMode'
import { cn } from '@/lib/utils'
import { api, setActiveCurrency } from '@/lib/api'
import { useAuth } from '@/contexts/AuthContext'
import { Separator } from '@/components/ui/separator'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { CommandPalette } from '@/components/CommandPalette'
import { VeloxLogo } from '@/components/VeloxLogo'
import { OnboardingLauncher } from '@/components/OnboardingLauncher'
import { useOnboardingSteps } from '@/hooks/useOnboardingSteps'

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
  { to: '/members', icon: UsersRound, label: 'Members' },
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
      <TooltipTrigger
        render={
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
          />
        }
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
      </TooltipTrigger>
      <TooltipContent side="right" className="md:hidden">
        {label}
      </TooltipContent>
    </Tooltip>
    </div>
  )
}

// ModeToggle — a Stripe-style Test/Live pill. The active side gets a subtle
// surface to mark state; "Live" gets a green dot so the state is legible at
// a glance across test/live sessions.
function ModeToggle({ livemode, onToggle, busy }: { livemode: boolean; onToggle: () => void; busy: boolean }) {
  return (
    <button
      type="button"
      onClick={onToggle}
      disabled={busy}
      aria-label={`Switch to ${livemode ? 'test' : 'live'} mode`}
      className={cn(
        'flex items-center rounded-full border border-border bg-muted p-0.5 text-xs font-medium transition-opacity',
        busy && 'opacity-60 cursor-not-allowed'
      )}
    >
      <span
        className={cn(
          'px-3 py-1 rounded-full transition-colors',
          !livemode ? 'bg-background shadow-sm text-foreground' : 'text-muted-foreground'
        )}
      >
        Test
      </span>
      <span
        className={cn(
          'px-3 py-1 rounded-full transition-colors flex items-center gap-1.5',
          livemode ? 'bg-background shadow-sm text-foreground' : 'text-muted-foreground'
        )}
      >
        {livemode && <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />}
        Live
      </span>
    </button>
  )
}

export function Layout({ children }: { children: ReactNode }) {
  const location = useLocation()
  const navigate = useNavigate()
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [commandOpen, setCommandOpen] = useState(false)
  const [navCounts, setNavCounts] = useState<Record<string, number>>({})
  const [toggling, setToggling] = useState(false)
  const { dark, toggle: toggleDark } = useDarkMode()
  const { user, logout, toggleLivemode } = useAuth()
  // Drives the live-mode Stripe-missing hard-blocker. The launcher itself
  // calls the same hook — React Query dedupes by key, so no duplicate fetches.
  const { hasLiveStripe } = useOnboardingSteps()

  const handleLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  const handleToggleLivemode = async () => {
    if (toggling) return
    setToggling(true)
    try {
      await toggleLivemode()
      toast.success(user?.livemode ? 'Switched to test mode' : 'Switched to live mode')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Could not switch mode')
    } finally {
      setToggling(false)
    }
  }

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

      {/* Footer — enterprise account menu. Trigger row shows identity +
          chevron; dropdown (opens upward) surfaces theme toggle and sign-out
          in a full-bleed menu, matching Linear/Vercel/Notion. Version tag
          lives inside the menu so the sidebar itself stays lean. */}
      <div className="p-2 border-t border-border">
        {user && (
          <DropdownMenu>
            <DropdownMenuTrigger
              aria-label="Account menu"
              className="w-full flex items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-accent data-[popup-open]:bg-accent outline-none focus-visible:ring-2 focus-visible:ring-ring transition-colors"
            >
              <div
                aria-hidden="true"
                className="h-7 w-7 shrink-0 rounded-full bg-gradient-to-br from-primary/25 to-primary/5 ring-1 ring-primary/20 text-primary flex items-center justify-center text-xs font-semibold"
              >
                {user.email.charAt(0).toUpperCase()}
              </div>
              <p className="text-xs text-foreground truncate flex-1 min-w-0" title={user.email}>
                {user.email}
              </p>
              <ChevronsUpDown size={14} className="shrink-0 text-muted-foreground" aria-hidden="true" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" side="top" sideOffset={8} className="w-60">
              <div className="flex items-center gap-2.5 px-2 py-2">
                <div
                  aria-hidden="true"
                  className="h-9 w-9 shrink-0 rounded-full bg-gradient-to-br from-primary/25 to-primary/5 ring-1 ring-primary/20 text-primary flex items-center justify-center text-sm font-semibold"
                >
                  {user.email.charAt(0).toUpperCase()}
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-medium text-foreground truncate" title={user.email}>
                    {user.email}
                  </p>
                  <p className="text-[11px] text-muted-foreground">Signed in</p>
                </div>
              </div>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={toggleDark}>
                {dark ? <Sun /> : <Moon />}
                <span>{dark ? 'Light mode' : 'Dark mode'}</span>
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onClick={handleLogout}>
                <LogOut />
                <span>Sign out</span>
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <p className="px-2 py-1 text-[10px] text-muted-foreground/60 text-center tracking-wide">
                Velox v2.0
              </p>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
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
        {/* Sticky top region — safety strips stack above the top bar so users
            never lose sight of mode/credential warnings while scrolling. */}
        <div className="sticky top-0 z-20">
          {/* Live-mode hard blocker: non-dismissible. Only fires once the
              Stripe-creds query has resolved to a definitive "no live keys"
              (hasLiveStripe === false) — undefined means still loading, so
              we don't flash a red banner to users who are fully set up. */}
          {user && user.livemode && hasLiveStripe === false && (
            <div
              role="alert"
              className="flex items-center justify-center gap-2 bg-destructive px-4 py-1.5 text-xs font-medium text-destructive-foreground"
            >
              <AlertTriangle size={14} aria-hidden="true" />
              <span>
                <strong className="font-semibold">LIVE</strong> mode but no Stripe live credentials — real charges will fail.
              </span>
              <Link
                to="/settings?tab=payments"
                className="ml-1 underline decoration-destructive-foreground/50 underline-offset-2 hover:decoration-destructive-foreground"
              >
                Connect Stripe
              </Link>
            </div>
          )}
          {/* Top bar — always visible. Mobile adds a hamburger, desktop leaves
              the left empty; the right carries the Test/Live toggle. */}
          <div className="flex items-center gap-3 px-4 py-3 border-b border-border bg-card">
            <button
              onClick={() => setSidebarOpen(true)}
              aria-label="Open menu"
              className="md:hidden text-muted-foreground hover:text-foreground"
            >
              <Menu size={22} />
            </button>
            <div className="md:hidden">
              <VeloxLogo size="sm" />
            </div>
            <div className="flex-1" />
            {user && <ModeToggle livemode={user.livemode} onToggle={handleToggleLivemode} busy={toggling} />}
          </div>
        </div>
        <div className="max-w-7xl mx-auto p-4 md:p-8">
          {children}
        </div>
      </main>

      {/* Command Palette */}
      <CommandPalette open={commandOpen} onClose={() => setCommandOpen(false)} />

      {/* Global onboarding launcher — floating bottom-right, persists across
          pages, self-hides when the checklist is complete or dismissed. */}
      <OnboardingLauncher />
    </div>
  )
}
