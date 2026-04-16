import { useState, useEffect, useRef, useMemo, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
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

interface ResultItem {
  id: string
  section: string
  title: string
  subtitle: string
  icon: typeof LayoutDashboard
  href: string
}

const NAV_ITEMS: ResultItem[] = [
  { id: 'nav-dashboard', section: 'Navigation', title: 'Dashboard', subtitle: 'Overview & analytics', icon: LayoutDashboard, href: '/' },
  { id: 'nav-customers', section: 'Navigation', title: 'Customers', subtitle: 'Manage customers', icon: Users, href: '/customers' },
  { id: 'nav-invoices', section: 'Navigation', title: 'Invoices', subtitle: 'View invoices', icon: FileText, href: '/invoices' },
  { id: 'nav-subscriptions', section: 'Navigation', title: 'Subscriptions', subtitle: 'Manage subscriptions', icon: CreditCard, href: '/subscriptions' },
  { id: 'nav-usage', section: 'Navigation', title: 'Usage Events', subtitle: 'Usage metering', icon: BarChart3, href: '/usage' },
  { id: 'nav-pricing', section: 'Navigation', title: 'Pricing', subtitle: 'Plans, meters, rules', icon: Tag, href: '/pricing' },
  { id: 'nav-coupons', section: 'Navigation', title: 'Coupons', subtitle: 'Discount codes', icon: Ticket, href: '/coupons' },
  { id: 'nav-credits', section: 'Navigation', title: 'Credits', subtitle: 'Customer credits', icon: Wallet, href: '/credits' },
  { id: 'nav-credit-notes', section: 'Navigation', title: 'Credit Notes', subtitle: 'Refunds & adjustments', icon: Receipt, href: '/credit-notes' },
  { id: 'nav-dunning', section: 'Navigation', title: 'Dunning', subtitle: 'Payment recovery', icon: AlertTriangle, href: '/dunning' },
  { id: 'nav-audit', section: 'Navigation', title: 'Audit Log', subtitle: 'Activity history', icon: ScrollText, href: '/audit-log' },
  { id: 'nav-webhooks', section: 'Navigation', title: 'Webhooks', subtitle: 'Endpoint management', icon: Globe, href: '/webhooks' },
  { id: 'nav-api-keys', section: 'Navigation', title: 'API Keys', subtitle: 'Authentication', icon: Key, href: '/api-keys' },
  { id: 'nav-settings', section: 'Navigation', title: 'Settings', subtitle: 'Tenant configuration', icon: Settings, href: '/settings' },
]

export function CommandPalette({ open, onClose }: CommandPaletteProps) {
  const navigate = useNavigate()
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  const [query, setQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
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

  useEffect(() => { if (!open) { setQuery(''); setSelectedIndex(0) } }, [open])
  useEffect(() => { if (open) requestAnimationFrame(() => inputRef.current?.focus()) }, [open])

  const results = useMemo(() => {
    const q = query.toLowerCase().trim()
    const items: ResultItem[] = []

    const filteredNav = q
      ? NAV_ITEMS.filter(item => item.title.toLowerCase().includes(q) || item.subtitle.toLowerCase().includes(q))
      : NAV_ITEMS
    items.push(...filteredNav)

    if (q.length >= 2) {
      customers.filter(c =>
        c.display_name.toLowerCase().includes(q) || c.external_id.toLowerCase().includes(q) || (c.email && c.email.toLowerCase().includes(q))
      ).slice(0, 5).forEach(c => items.push({
        id: `cust-${c.id}`, section: 'Customers', title: c.display_name, subtitle: c.external_id, icon: User, href: `/customers/${c.id}`,
      }))

      invoices.filter(inv =>
        inv.invoice_number.toLowerCase().includes(q) || inv.status.toLowerCase().includes(q)
      ).slice(0, 5).forEach(inv => items.push({
        id: `inv-${inv.id}`, section: 'Invoices', title: inv.invoice_number, subtitle: `${inv.status} · ${formatCents(inv.amount_due_cents, inv.currency)}`, icon: FileText, href: `/invoices/${inv.id}`,
      }))

      plans.filter(p =>
        p.name.toLowerCase().includes(q) || p.code.toLowerCase().includes(q)
      ).slice(0, 5).forEach(p => items.push({
        id: `plan-${p.id}`, section: 'Plans', title: p.name, subtitle: p.code, icon: Zap, href: `/pricing`,
      }))

      subscriptions.filter(s =>
        s.code.toLowerCase().includes(q) || s.display_name.toLowerCase().includes(q)
      ).slice(0, 5).forEach(s => items.push({
        id: `sub-${s.id}`, section: 'Subscriptions', title: s.display_name, subtitle: `${s.code} · ${s.status}`, icon: Hash, href: `/subscriptions/${s.id}`,
      }))
    }

    return items
  }, [query, customers, invoices, plans, subscriptions])

  useEffect(() => { setSelectedIndex(0) }, [results])

  useEffect(() => {
    if (listRef.current) {
      const el = listRef.current.querySelector(`[data-cmd-index="${selectedIndex}"]`)
      el?.scrollIntoView({ block: 'nearest' })
    }
  }, [selectedIndex])

  const selectItem = useCallback((item: ResultItem) => { navigate(item.href); onClose() }, [navigate, onClose])

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setSelectedIndex(prev => (prev < results.length - 1 ? prev + 1 : 0)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setSelectedIndex(prev => (prev > 0 ? prev - 1 : results.length - 1)) }
    else if (e.key === 'Enter') { e.preventDefault(); if (results[selectedIndex]) selectItem(results[selectedIndex]) }
    else if (e.key === 'Escape') { e.preventDefault(); onClose() }
  }, [results, selectedIndex, selectItem, onClose])

  if (!open) return null

  // Group results by section
  const sections: { label: string; items: (ResultItem & { flatIndex: number })[] }[] = []
  let flatIndex = 0
  for (const item of results) {
    let section = sections.find(s => s.label === item.section)
    if (!section) { section = { label: item.section, items: [] }; sections.push(section) }
    section.items.push({ ...item, flatIndex })
    flatIndex++
  }

  return (
    <div className="fixed inset-0 z-[60]">
      {/* Dark backdrop — strong enough to focus attention */}
      <div className="absolute inset-0 bg-gray-950/60 backdrop-blur-sm" onClick={onClose} />

      {/* Palette */}
      <div className="relative flex justify-center pt-[12vh] px-4">
        <div
          className="w-full max-w-[560px] bg-white dark:bg-gray-900 rounded-xl shadow-[0_25px_60px_-12px_rgba(0,0,0,0.35)] border border-gray-200 dark:border-gray-700 overflow-hidden animate-scale-in flex flex-col"
          role="dialog"
          aria-modal="true"
          aria-label="Command palette"
        >
          {/* Search input — prominent */}
          <div className="flex items-center gap-3 px-5 border-b border-gray-200 dark:border-gray-700">
            <Search size={20} className="text-velox-500 shrink-0" />
            <input
              ref={inputRef}
              type="text"
              value={query}
              onChange={e => setQuery(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Search or jump to..."
              className="flex-1 py-4 text-[15px] bg-transparent border-0 outline-none text-gray-900 dark:text-gray-100 placeholder:text-gray-400 dark:placeholder:text-gray-500"
            />
            <kbd className="hidden sm:inline-flex px-2 py-1 text-[11px] font-medium text-gray-400 dark:text-gray-500 bg-gray-100 dark:bg-gray-800 rounded-md border border-gray-200 dark:border-gray-700">
              ESC
            </kbd>
          </div>

          {/* Results */}
          <div ref={listRef} className="overflow-y-auto max-h-[380px]">
            {searching && !fetched && (
              <div className="px-5 py-10 text-center text-sm text-gray-400">Loading...</div>
            )}

            {!searching && results.length === 0 && (
              <div className="px-5 py-10 text-center">
                <p className="text-sm text-gray-400">No results for "<span className="text-gray-600 dark:text-gray-300">{query}</span>"</p>
              </div>
            )}

            {sections.map(section => (
              <div key={section.label} className="py-1">
                <p className="px-5 pt-3 pb-1 text-[11px] uppercase tracking-wider font-semibold text-gray-400 dark:text-gray-500 select-none">
                  {section.label}
                </p>
                {section.items.map(item => {
                  const Icon = item.icon
                  const isSelected = item.flatIndex === selectedIndex
                  return (
                    <button
                      key={item.id}
                      data-cmd-index={item.flatIndex}
                      onClick={() => selectItem(item)}
                      onMouseEnter={() => setSelectedIndex(item.flatIndex)}
                      className={cn(
                        'w-full flex items-center gap-3 px-5 py-2 text-left transition-all duration-100',
                        isSelected
                          ? 'bg-velox-600 dark:bg-velox-600'
                          : 'hover:bg-gray-50 dark:hover:bg-gray-800/60'
                      )}
                    >
                      <div className={cn(
                        'w-7 h-7 rounded-md flex items-center justify-center shrink-0 transition-colors',
                        isSelected
                          ? 'bg-white/20 text-white'
                          : 'bg-gray-100 dark:bg-gray-800 text-gray-500 dark:text-gray-400'
                      )}>
                        <Icon size={15} />
                      </div>
                      <div className="flex-1 min-w-0">
                        <p className={cn(
                          'text-sm truncate',
                          isSelected
                            ? 'text-white font-medium'
                            : 'text-gray-900 dark:text-gray-100'
                        )}>
                          {item.title}
                        </p>
                        <p className={cn(
                          'text-xs truncate',
                          isSelected ? 'text-white/60' : 'text-gray-400 dark:text-gray-500'
                        )}>
                          {item.subtitle}
                        </p>
                      </div>
                      {isSelected && (
                        <ArrowRight size={14} className="text-white/50 shrink-0" />
                      )}
                    </button>
                  )
                })}
              </div>
            ))}
          </div>

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
        </div>
      </div>
    </div>
  )
}
