import { useState, useEffect, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { Command } from 'cmdk'
import {
  Search, LayoutDashboard, Users, FileText, CreditCard, Tag, Wallet, Settings,
  Receipt, AlertTriangle, ScrollText, Globe, Key, BarChart3, Ticket,
  User, Hash, Zap, ArrowRight,
} from 'lucide-react'
import { api, type Customer, type Invoice, type Plan, type Subscription, formatCents } from '@/lib/api'
import { cn } from '@/lib/cn'

interface CommandPaletteProps {
  open: boolean
  onClose: () => void
}

interface NavItem {
  id: string
  title: string
  subtitle: string
  icon: typeof LayoutDashboard
  href: string
}

const NAV_ITEMS: NavItem[] = [
  { id: 'nav-dashboard', title: 'Dashboard', subtitle: 'Overview & analytics', icon: LayoutDashboard, href: '/' },
  { id: 'nav-customers', title: 'Customers', subtitle: 'Manage customers', icon: Users, href: '/customers' },
  { id: 'nav-invoices', title: 'Invoices', subtitle: 'View invoices', icon: FileText, href: '/invoices' },
  { id: 'nav-subscriptions', title: 'Subscriptions', subtitle: 'Manage subscriptions', icon: CreditCard, href: '/subscriptions' },
  { id: 'nav-usage', title: 'Usage Events', subtitle: 'Usage metering', icon: BarChart3, href: '/usage' },
  { id: 'nav-pricing', title: 'Pricing', subtitle: 'Plans, meters, rules', icon: Tag, href: '/pricing' },
  { id: 'nav-coupons', title: 'Coupons', subtitle: 'Discount codes', icon: Ticket, href: '/coupons' },
  { id: 'nav-credits', title: 'Credits', subtitle: 'Customer credits', icon: Wallet, href: '/credits' },
  { id: 'nav-credit-notes', title: 'Credit Notes', subtitle: 'Refunds & adjustments', icon: Receipt, href: '/credit-notes' },
  { id: 'nav-dunning', title: 'Dunning', subtitle: 'Payment recovery', icon: AlertTriangle, href: '/dunning' },
  { id: 'nav-audit', title: 'Audit Log', subtitle: 'Activity history', icon: ScrollText, href: '/audit-log' },
  { id: 'nav-webhooks', title: 'Webhooks', subtitle: 'Endpoint management', icon: Globe, href: '/webhooks' },
  { id: 'nav-api-keys', title: 'API Keys', subtitle: 'Authentication', icon: Key, href: '/api-keys' },
  { id: 'nav-settings', title: 'Settings', subtitle: 'Tenant configuration', icon: Settings, href: '/settings' },
]

export function CommandPalette({ open, onClose }: CommandPaletteProps) {
  const navigate = useNavigate()

  const [searching, setSearching] = useState(false)
  const [customers, setCustomers] = useState<Customer[]>([])
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [plans, setPlans] = useState<Plan[]>([])
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [fetched, setFetched] = useState(false)

  // Fetch entity data when palette opens
  useEffect(() => {
    if (!open || fetched) return
    setSearching(true)
    Promise.allSettled([
      api.listCustomers('limit=50'),
      api.listInvoices('limit=50'),
      api.listPlans(),
      api.listSubscriptions('limit=50'),
    ]).then(([cRes, iRes, pRes, sRes]) => {
      if (cRes.status === 'fulfilled') setCustomers(cRes.value.data || [])
      if (iRes.status === 'fulfilled') setInvoices(iRes.value.data || [])
      if (pRes.status === 'fulfilled') setPlans(pRes.value.data || [])
      if (sRes.status === 'fulfilled') setSubscriptions(sRes.value.data || [])
      setFetched(true)
      setSearching(false)
    })
  }, [open, fetched])

  const go = useCallback((href: string) => {
    navigate(href)
    onClose()
  }, [navigate, onClose])

  if (!open) return null

  return (
    <div className="fixed inset-0 z-[60]">
      {/* Dark backdrop */}
      <div className="absolute inset-0 bg-gray-950/60 backdrop-blur-sm" onClick={onClose} />

      {/* Palette */}
      <div className="relative flex justify-center pt-[12vh] px-4">
        <div className="w-full max-w-[560px] bg-white dark:bg-gray-900 rounded-xl shadow-[0_25px_60px_-12px_rgba(0,0,0,0.35)] border border-gray-200 dark:border-gray-700 overflow-hidden animate-scale-in flex flex-col">
          <Command label="Command palette" shouldFilter={true} className="flex flex-col flex-1 min-h-0">
            {/* Search input */}
            <div className="flex items-center gap-3 px-5 border-b border-gray-200 dark:border-gray-700">
              <Search size={20} className="text-velox-500 shrink-0" />
              <Command.Input
                placeholder="Search or jump to..."
                className="flex-1 py-4 text-[15px] bg-transparent border-0 outline-none ring-0 focus:ring-0 focus:outline-none text-gray-900 dark:text-gray-100 placeholder:text-gray-400 dark:placeholder:text-gray-500"
              />
              <kbd className="hidden sm:inline-flex px-2 py-1 text-[11px] font-medium text-gray-400 dark:text-gray-500 bg-gray-100 dark:bg-gray-800 rounded-md border border-gray-200 dark:border-gray-700">
                ESC
              </kbd>
            </div>

            {/* Results */}
            <Command.List className="overflow-y-auto max-h-[380px]">
              {searching && !fetched && (
                <div className="px-5 py-10 text-center text-sm text-gray-400">Loading...</div>
              )}

              <Command.Empty className="px-5 py-10 text-center text-sm text-gray-400">
                No results found
              </Command.Empty>

              <Command.Group heading="Navigation" className="py-1 [&_[cmdk-group-heading]]:px-5 [&_[cmdk-group-heading]]:pt-3 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:text-gray-400 [&_[cmdk-group-heading]]:dark:text-gray-500 [&_[cmdk-group-heading]]:select-none">
                {NAV_ITEMS.map(item => (
                  <CommandItem key={item.id} icon={item.icon} title={item.title} subtitle={item.subtitle} onSelect={() => go(item.href)} />
                ))}
              </Command.Group>

              {customers.length > 0 && (
                <Command.Group heading="Customers" className="py-1 [&_[cmdk-group-heading]]:px-5 [&_[cmdk-group-heading]]:pt-3 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:text-gray-400 [&_[cmdk-group-heading]]:dark:text-gray-500 [&_[cmdk-group-heading]]:select-none">
                  {customers.slice(0, 10).map(c => (
                    <CommandItem key={`cust-${c.id}`} icon={User} title={c.display_name} subtitle={c.external_id} onSelect={() => go(`/customers/${c.id}`)} keywords={[c.display_name, c.external_id, c.email || '']} />
                  ))}
                </Command.Group>
              )}

              {invoices.length > 0 && (
                <Command.Group heading="Invoices" className="py-1 [&_[cmdk-group-heading]]:px-5 [&_[cmdk-group-heading]]:pt-3 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:text-gray-400 [&_[cmdk-group-heading]]:dark:text-gray-500 [&_[cmdk-group-heading]]:select-none">
                  {invoices.slice(0, 10).map(inv => (
                    <CommandItem key={`inv-${inv.id}`} icon={FileText} title={inv.invoice_number} subtitle={`${inv.status} · ${formatCents(inv.amount_due_cents, inv.currency)}`} onSelect={() => go(`/invoices/${inv.id}`)} keywords={[inv.invoice_number, inv.status]} />
                  ))}
                </Command.Group>
              )}

              {plans.length > 0 && (
                <Command.Group heading="Plans" className="py-1 [&_[cmdk-group-heading]]:px-5 [&_[cmdk-group-heading]]:pt-3 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:text-gray-400 [&_[cmdk-group-heading]]:dark:text-gray-500 [&_[cmdk-group-heading]]:select-none">
                  {plans.slice(0, 10).map(p => (
                    <CommandItem key={`plan-${p.id}`} icon={Zap} title={p.name} subtitle={p.code} onSelect={() => go('/pricing')} keywords={[p.name, p.code]} />
                  ))}
                </Command.Group>
              )}

              {subscriptions.length > 0 && (
                <Command.Group heading="Subscriptions" className="py-1 [&_[cmdk-group-heading]]:px-5 [&_[cmdk-group-heading]]:pt-3 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:text-gray-400 [&_[cmdk-group-heading]]:dark:text-gray-500 [&_[cmdk-group-heading]]:select-none">
                  {subscriptions.slice(0, 10).map(s => (
                    <CommandItem key={`sub-${s.id}`} icon={Hash} title={s.display_name} subtitle={`${s.code} · ${s.status}`} onSelect={() => go(`/subscriptions/${s.id}`)} keywords={[s.display_name, s.code]} />
                  ))}
                </Command.Group>
              )}
            </Command.List>

            {/* Footer */}
            <div className="flex items-center gap-5 px-5 py-2 border-t border-gray-100 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/50">
              <span className="flex items-center gap-1.5 text-[11px] text-gray-400">
                <kbd className="px-1 py-0.5 bg-white dark:bg-gray-700 rounded border border-gray-200 dark:border-gray-600 font-mono text-[10px] leading-none">&uarr;&darr;</kbd>
                navigate
              </span>
              <span className="flex items-center gap-1.5 text-[11px] text-gray-400">
                <kbd className="px-1.5 py-0.5 bg-white dark:bg-gray-700 rounded border border-gray-200 dark:border-gray-600 font-mono text-[10px] leading-none">&crarr;</kbd>
                open
              </span>
              <span className="flex items-center gap-1.5 text-[11px] text-gray-400">
                <kbd className="px-1 py-0.5 bg-white dark:bg-gray-700 rounded border border-gray-200 dark:border-gray-600 font-mono text-[10px] leading-none">esc</kbd>
                close
              </span>
            </div>
          </Command>
        </div>
      </div>
    </div>
  )
}

function CommandItem({ icon: Icon, title, subtitle, onSelect, keywords }: {
  icon: typeof LayoutDashboard
  title: string
  subtitle: string
  onSelect: () => void
  keywords?: string[]
}) {
  return (
    <Command.Item
      value={title}
      keywords={keywords}
      onSelect={onSelect}
      className={cn(
        'w-full flex items-center gap-3 px-5 py-2 text-left transition-all duration-100 cursor-pointer',
        'hover:bg-gray-50 dark:hover:bg-gray-800/60',
        'data-[selected=true]:bg-velox-600 dark:data-[selected=true]:bg-velox-600',
      )}
    >
      <div className={cn(
        'w-7 h-7 rounded-md flex items-center justify-center shrink-0 transition-colors',
        'bg-gray-100 dark:bg-gray-800 text-gray-500 dark:text-gray-400',
        'group-data-[selected=true]:bg-white/20 group-data-[selected=true]:text-white',
      )}>
        <Icon size={15} />
      </div>
      <div className="flex-1 min-w-0">
        <p className={cn(
          'text-sm truncate text-gray-900 dark:text-gray-100',
          'data-[selected=true]:text-white data-[selected=true]:font-medium',
        )}>
          {title}
        </p>
        <p className={cn(
          'text-xs truncate text-gray-400 dark:text-gray-500',
        )}>
          {subtitle}
        </p>
      </div>
      <ArrowRight size={14} className="text-white/50 shrink-0 opacity-0 [[data-selected=true]_&]:opacity-100 transition-opacity" />
    </Command.Item>
  )
}
