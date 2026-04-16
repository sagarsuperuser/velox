import { useState, useEffect, useRef, useMemo, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  Search, LayoutDashboard, Users, FileText, CreditCard, Tag, Wallet, Settings,
  Receipt, AlertTriangle, ScrollText, Globe, Key, BarChart3, Ticket,
  User, Hash, Zap,
} from 'lucide-react'
import { useDebounce } from '@/hooks/useDebounce'
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

  // Entity caches — fetched once per palette open
  const [customers, setCustomers] = useState<Customer[]>([])
  const [invoices, setInvoices] = useState<Invoice[]>([])
  const [plans, setPlans] = useState<Plan[]>([])
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([])
  const [fetched, setFetched] = useState(false)

  const debouncedQuery = useDebounce(query, 200)

  // Fetch entity data when palette opens
  useEffect(() => {
    if (!open) return
    if (fetched) return

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

  // Reset state when closing
  useEffect(() => {
    if (!open) {
      setQuery('')
      setSelectedIndex(0)
    }
  }, [open])

  // Focus input when opening
  useEffect(() => {
    if (open) {
      // Small delay to ensure DOM is ready
      requestAnimationFrame(() => {
        inputRef.current?.focus()
      })
    }
  }, [open])

  // Build results list
  const results = useMemo(() => {
    const q = debouncedQuery.toLowerCase().trim()
    const items: ResultItem[] = []

    // Filter navigation items
    const filteredNav = q
      ? NAV_ITEMS.filter(item =>
          item.title.toLowerCase().includes(q) ||
          item.subtitle.toLowerCase().includes(q)
        )
      : NAV_ITEMS

    items.push(...filteredNav)

    // Only search entities when there is a query of 2+ chars
    if (q.length >= 2) {
      // Customers
      const matchedCustomers = customers.filter(c =>
        c.display_name.toLowerCase().includes(q) ||
        c.external_id.toLowerCase().includes(q) ||
        (c.email && c.email.toLowerCase().includes(q))
      ).slice(0, 5)

      items.push(...matchedCustomers.map(c => ({
        id: `cust-${c.id}`,
        section: 'Customers',
        title: c.display_name,
        subtitle: c.external_id,
        icon: User,
        href: `/customers/${c.id}`,
      })))

      // Invoices
      const matchedInvoices = invoices.filter(inv =>
        inv.invoice_number.toLowerCase().includes(q) ||
        inv.status.toLowerCase().includes(q)
      ).slice(0, 5)

      items.push(...matchedInvoices.map(inv => ({
        id: `inv-${inv.id}`,
        section: 'Invoices',
        title: inv.invoice_number,
        subtitle: `${inv.status} ${formatCents(inv.amount_due_cents, inv.currency)}`,
        icon: FileText,
        href: `/invoices/${inv.id}`,
      })))

      // Plans
      const matchedPlans = plans.filter(p =>
        p.name.toLowerCase().includes(q) ||
        p.code.toLowerCase().includes(q)
      ).slice(0, 5)

      items.push(...matchedPlans.map(p => ({
        id: `plan-${p.id}`,
        section: 'Plans',
        title: p.name,
        subtitle: p.code,
        icon: Zap,
        href: `/pricing`,
      })))

      // Subscriptions
      const matchedSubs = subscriptions.filter(s =>
        s.code.toLowerCase().includes(q) ||
        s.display_name.toLowerCase().includes(q)
      ).slice(0, 5)

      items.push(...matchedSubs.map(s => ({
        id: `sub-${s.id}`,
        section: 'Subscriptions',
        title: s.display_name,
        subtitle: `${s.code} -- ${s.status}`,
        icon: Hash,
        href: `/subscriptions/${s.id}`,
      })))
    }

    return items
  }, [debouncedQuery, customers, invoices, plans, subscriptions])

  // Reset selected index when results change
  useEffect(() => {
    setSelectedIndex(0)
  }, [results])

  // Scroll selected item into view
  useEffect(() => {
    if (listRef.current) {
      const el = listRef.current.querySelector(`[data-cmd-index="${selectedIndex}"]`)
      el?.scrollIntoView({ block: 'nearest' })
    }
  }, [selectedIndex])

  const selectItem = useCallback((item: ResultItem) => {
    navigate(item.href)
    onClose()
  }, [navigate, onClose])

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setSelectedIndex(prev => (prev < results.length - 1 ? prev + 1 : 0))
        break
      case 'ArrowUp':
        e.preventDefault()
        setSelectedIndex(prev => (prev > 0 ? prev - 1 : results.length - 1))
        break
      case 'Enter':
        e.preventDefault()
        if (results[selectedIndex]) {
          selectItem(results[selectedIndex])
        }
        break
      case 'Escape':
        e.preventDefault()
        onClose()
        break
    }
  }, [results, selectedIndex, selectItem, onClose])

  if (!open) return null

  // Group results by section for display
  const sections: { label: string; items: (ResultItem & { flatIndex: number })[] }[] = []
  let flatIndex = 0
  for (const item of results) {
    let section = sections.find(s => s.label === item.section)
    if (!section) {
      section = { label: item.section, items: [] }
      sections.push(section)
    }
    section.items.push({ ...item, flatIndex })
    flatIndex++
  }

  return (
    <div className="fixed inset-0 z-50">
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/25 backdrop-blur-[2px] animate-fade-in"
        onClick={onClose}
      />

      {/* Palette container */}
      <div className="relative flex justify-center pt-[15vh] px-4">
        <div
          className="w-full max-w-[640px] bg-white dark:bg-gray-900 rounded-2xl shadow-modal border border-gray-200 dark:border-gray-800 overflow-hidden animate-scale-in flex flex-col"
          role="dialog"
          aria-modal="true"
          aria-label="Command palette"
        >
          {/* Search input */}
          <div className="flex items-center gap-3 px-4 border-b border-gray-100 dark:border-gray-800">
            <Search size={18} className="text-gray-400 shrink-0" />
            <input
              ref={inputRef}
              type="text"
              value={query}
              onChange={e => setQuery(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Search or jump to..."
              className="flex-1 py-3.5 text-base bg-transparent border-0 outline-none text-gray-900 dark:text-gray-100 placeholder:text-gray-400"
            />
            <kbd className="hidden sm:inline-flex items-center px-1.5 py-0.5 text-[11px] font-medium text-gray-400 bg-gray-100 dark:bg-gray-800 rounded border border-gray-200 dark:border-gray-700">
              ESC
            </kbd>
          </div>

          {/* Results */}
          <div ref={listRef} className="overflow-y-auto max-h-[400px] py-2">
            {searching && !fetched && (
              <div className="px-4 py-8 text-center text-sm text-gray-400">
                Loading...
              </div>
            )}

            {!searching && results.length === 0 && (
              <div className="px-4 py-8 text-center text-sm text-gray-400">
                No results found for "{query}"
              </div>
            )}

            {sections.map(section => (
              <div key={section.label}>
                <p className="px-4 pt-3 pb-1.5 text-[11px] uppercase tracking-wider font-medium text-gray-400 dark:text-gray-500 select-none">
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
                        'w-full flex items-center gap-3 px-4 py-2.5 text-left transition-colors',
                        isSelected
                          ? 'bg-velox-50 dark:bg-velox-900/20'
                          : 'hover:bg-gray-50 dark:hover:bg-gray-800/50'
                      )}
                    >
                      <div className={cn(
                        'w-8 h-8 rounded-lg flex items-center justify-center shrink-0',
                        isSelected
                          ? 'bg-velox-100 dark:bg-velox-900/40 text-velox-600 dark:text-velox-300'
                          : 'bg-gray-100 dark:bg-gray-800 text-gray-400 dark:text-gray-500'
                      )}>
                        <Icon size={16} />
                      </div>
                      <div className="flex-1 min-w-0">
                        <p className={cn(
                          'text-sm truncate',
                          isSelected
                            ? 'text-velox-700 dark:text-velox-300 font-medium'
                            : 'text-gray-900 dark:text-gray-100'
                        )}>
                          {item.title}
                        </p>
                        <p className="text-xs text-gray-400 dark:text-gray-500 truncate">
                          {item.subtitle}
                        </p>
                      </div>
                      {isSelected && (
                        <span className="text-xs text-gray-400 dark:text-gray-500 shrink-0">
                          Enter
                        </span>
                      )}
                    </button>
                  )
                })}
              </div>
            ))}
          </div>

          {/* Footer hints */}
          <div className="flex items-center gap-4 px-4 py-2.5 border-t border-gray-100 dark:border-gray-800 text-[11px] text-gray-400 dark:text-gray-500">
            <span className="flex items-center gap-1">
              <kbd className="px-1 py-0.5 bg-gray-100 dark:bg-gray-800 rounded border border-gray-200 dark:border-gray-700 font-mono text-[10px]">&uarr;</kbd>
              <kbd className="px-1 py-0.5 bg-gray-100 dark:bg-gray-800 rounded border border-gray-200 dark:border-gray-700 font-mono text-[10px]">&darr;</kbd>
              navigate
            </span>
            <span className="flex items-center gap-1">
              <kbd className="px-1.5 py-0.5 bg-gray-100 dark:bg-gray-800 rounded border border-gray-200 dark:border-gray-700 font-mono text-[10px]">&crarr;</kbd>
              select
            </span>
            <span className="flex items-center gap-1">
              <kbd className="px-1 py-0.5 bg-gray-100 dark:bg-gray-800 rounded border border-gray-200 dark:border-gray-700 font-mono text-[10px]">esc</kbd>
              close
            </span>
          </div>
        </div>
      </div>
    </div>
  )
}
