import { useState, useEffect, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  LayoutDashboard, Users, FileText, CreditCard, Tag, Wallet, Settings,
  Receipt, AlertTriangle, ScrollText, Globe, Key, BarChart3, Ticket,
  User, Hash, Zap, type LucideIcon,
} from 'lucide-react'
import { api, type Customer, type Invoice, type Plan, type Subscription, formatCents } from '@/lib/api'
import {
  Command,
  CommandDialog,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
  CommandSeparator,
} from '@/components/ui/command'

interface CommandPaletteProps {
  open: boolean
  onClose: () => void
}

interface NavItem {
  id: string
  title: string
  subtitle: string
  icon: LucideIcon
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

  const [customers, setCustomers] = useState<Customer[]>([])
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [plans, setPlans] = useState<Plan[]>([])
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [fetched, setFetched] = useState(false)

  // Fetch entity data when palette opens
  useEffect(() => {
    if (!open || fetched) return
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
    })
  }, [open, fetched])

  const go = useCallback((href: string) => {
    navigate(href)
    onClose()
  }, [navigate, onClose])

  return (
    <CommandDialog open={open} onOpenChange={(isOpen) => { if (!isOpen) onClose() }}>
      <Command className="rounded-lg border-none shadow-none">
      <CommandInput placeholder="Search or jump to..." />
      <CommandList>
        <CommandEmpty>No results found</CommandEmpty>

        <CommandGroup heading="Navigation">
          {NAV_ITEMS.map(item => {
            const Icon = item.icon
            return (
              <CommandItem
                key={item.id}
                value={item.title}
                onSelect={() => go(item.href)}
                className="data-selected:bg-primary data-selected:text-primary-foreground"
              >
                <Icon className="mr-2 h-4 w-4" />
                <div className="flex flex-col">
                  <span>{item.title}</span>
                  <span className="text-xs opacity-60">{item.subtitle}</span>
                </div>
              </CommandItem>
            )
          })}
        </CommandGroup>

        {customers.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Customers">
              {customers.slice(0, 10).map(c => (
                <CommandItem
                  key={`cust-${c.id}`}
                  value={`customer ${c.display_name} ${c.external_id} ${c.email || ''}`}
                  onSelect={() => go(`/customers/${c.id}`)}
                  className="data-selected:bg-primary data-selected:text-primary-foreground"
                >
                  <User className="mr-2 h-4 w-4" />
                  <div className="flex flex-col">
                    <span>{c.display_name}</span>
                    <span className="text-xs opacity-60">{c.external_id}</span>
                  </div>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {invoices.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Invoices">
              {invoices.slice(0, 10).map(inv => (
                <CommandItem
                  key={`inv-${inv.id}`}
                  value={`invoice ${inv.invoice_number} ${inv.status}`}
                  onSelect={() => go(`/invoices/${inv.id}`)}
                  className="data-selected:bg-primary data-selected:text-primary-foreground"
                >
                  <FileText className="mr-2 h-4 w-4" />
                  <div className="flex flex-col">
                    <span>{inv.invoice_number}</span>
                    <span className="text-xs opacity-60">{inv.status} -- {formatCents(inv.amount_due_cents, inv.currency)}</span>
                  </div>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {plans.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Plans">
              {plans.slice(0, 10).map(p => (
                <CommandItem
                  key={`plan-${p.id}`}
                  value={`plan ${p.name} ${p.code}`}
                  onSelect={() => go('/pricing')}
                  className="data-selected:bg-primary data-selected:text-primary-foreground"
                >
                  <Zap className="mr-2 h-4 w-4" />
                  <div className="flex flex-col">
                    <span>{p.name}</span>
                    <span className="text-xs opacity-60">{p.code}</span>
                  </div>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {subscriptions.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Subscriptions">
              {subscriptions.slice(0, 10).map(s => (
                <CommandItem
                  key={`sub-${s.id}`}
                  value={`subscription ${s.display_name} ${s.code} ${s.status}`}
                  onSelect={() => go(`/subscriptions/${s.id}`)}
                  className="data-selected:bg-primary data-selected:text-primary-foreground"
                >
                  <Hash className="mr-2 h-4 w-4" />
                  <div className="flex flex-col">
                    <span>{s.display_name}</span>
                    <span className="text-xs opacity-60">{s.code} -- {s.status}</span>
                  </div>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}
      </CommandList>
      </Command>
    </CommandDialog>
  )
}
