import { type ReactNode } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { Sun, Moon } from 'lucide-react'
import { useDarkMode } from '@/hooks/useDarkMode'
import { cn } from '@/lib/utils'
import { VeloxLogo } from '@/components/VeloxLogo'

const topNav = [
  { to: '/docs', label: 'Docs' },
  { to: '/security', label: 'Security' },
  { to: '/status', label: 'Status' },
  { to: '/changelog', label: 'Changelog' },
]

const footerCol1 = [
  { to: '/docs', label: 'Documentation' },
  { to: '/docs/quickstart', label: 'Quickstart' },
  { to: '/docs/webhooks', label: 'Webhooks' },
  { to: '/docs/idempotency', label: 'Idempotency' },
]

const footerCol2 = [
  { to: '/security', label: 'Security' },
  { to: '/status', label: 'Status' },
  { to: '/changelog', label: 'Changelog' },
]

const footerCol3 = [
  { to: '/terms', label: 'Terms' },
  { to: '/privacy', label: 'Privacy' },
  { to: '/dpa', label: 'DPA' },
]

export function PublicLayout({ children }: { children: ReactNode }) {
  const { pathname } = useLocation()
  const { dark, toggle } = useDarkMode()

  return (
    <div className="min-h-screen flex flex-col bg-background text-foreground">
      <header className="border-b border-border sticky top-0 z-30 bg-background/80 backdrop-blur">
        <div className="max-w-6xl mx-auto flex items-center justify-between px-6 h-14">
          <Link to="/docs" className="flex items-center gap-2.5 shrink-0">
            <VeloxLogo size="sm" />
          </Link>
          <nav className="hidden md:flex items-center gap-1" aria-label="Primary">
            {topNav.map((item) => {
              const active = pathname === item.to || pathname.startsWith(item.to + '/')
              return (
                <Link
                  key={item.to}
                  to={item.to}
                  aria-current={active ? 'page' : undefined}
                  className={cn(
                    'px-3 py-1.5 rounded-md text-sm transition-colors',
                    active
                      ? 'text-foreground font-medium bg-muted'
                      : 'text-muted-foreground hover:text-foreground hover:bg-muted/60',
                  )}
                >
                  {item.label}
                </Link>
              )
            })}
          </nav>
          <div className="flex items-center gap-2">
            <button
              onClick={toggle}
              aria-label={dark ? 'Switch to light mode' : 'Switch to dark mode'}
              className="p-2 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/60 transition-colors"
            >
              {dark ? <Sun size={16} /> : <Moon size={16} />}
            </button>
            <Link
              to="/login"
              className="text-sm px-3 py-1.5 rounded-md border border-border hover:bg-muted/60 transition-colors"
            >
              Sign in
            </Link>
          </div>
        </div>
      </header>

      <main className="flex-1">{children}</main>

      <footer className="border-t border-border mt-16">
        <div className="max-w-6xl mx-auto px-6 py-12 grid grid-cols-2 md:grid-cols-4 gap-8 text-sm">
          <div>
            <VeloxLogo size="sm" />
            <p className="mt-4 text-muted-foreground text-xs leading-relaxed max-w-[200px]">
              Open-source usage-based billing for developer-first companies.
            </p>
          </div>
          <FooterCol title="Developers" links={footerCol1} />
          <FooterCol title="Platform" links={footerCol2} />
          <FooterCol title="Legal" links={footerCol3} />
        </div>
        <div className="border-t border-border">
          <div className="max-w-6xl mx-auto px-6 py-4 flex items-center justify-between text-xs text-muted-foreground">
            <span>© {new Date().getFullYear()} Velox</span>
            <a
              href="mailto:support@velox.dev"
              className="hover:text-foreground transition-colors"
            >
              support@velox.dev
            </a>
          </div>
        </div>
      </footer>
    </div>
  )
}

function FooterCol({ title, links }: { title: string; links: { to: string; label: string }[] }) {
  return (
    <div>
      <h3 className="font-medium text-foreground mb-3">{title}</h3>
      <ul className="space-y-2">
        {links.map((link) => (
          <li key={link.to}>
            <Link
              to={link.to}
              className="text-muted-foreground hover:text-foreground transition-colors"
            >
              {link.label}
            </Link>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function PublicPageHeader({
  eyebrow,
  title,
  description,
}: {
  eyebrow?: string
  title: string
  description?: string
}) {
  return (
    <div className="border-b border-border">
      <div className="max-w-6xl mx-auto px-6 py-10">
        {eyebrow && (
          <div className="text-xs uppercase tracking-wide text-muted-foreground mb-2">
            {eyebrow}
          </div>
        )}
        <h1 className="text-3xl md:text-4xl font-semibold tracking-tight text-foreground">{title}</h1>
        {description && (
          <p className="mt-3 text-muted-foreground max-w-2xl text-base leading-relaxed">
            {description}
          </p>
        )}
      </div>
    </div>
  )
}
