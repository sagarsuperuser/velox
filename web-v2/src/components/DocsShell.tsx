import { type ReactNode } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { cn } from '@/lib/utils'

const docsNav = [
  { heading: 'Introduction', items: [{ to: '/docs', label: 'Overview' }] },
  {
    heading: 'Guides',
    items: [
      { to: '/docs/quickstart', label: 'Quickstart' },
      { to: '/docs/recipes', label: 'Pricing recipes' },
      { to: '/docs/webhooks', label: 'Webhooks' },
      { to: '/docs/idempotency', label: 'Idempotency & retries' },
    ],
  },
  {
    heading: 'Reference',
    items: [{ to: '/docs/api', label: 'API reference' }],
  },
]

export function DocsShell({ children }: { children: ReactNode }) {
  const { pathname } = useLocation()
  return (
    <div className="max-w-6xl mx-auto px-6 py-10 grid grid-cols-1 md:grid-cols-[220px_1fr] gap-10">
      <aside className="md:sticky md:top-20 md:self-start">
        <nav aria-label="Docs sections">
          {docsNav.map((group) => (
            <div key={group.heading} className="mb-6">
              <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground mb-2">
                {group.heading}
              </div>
              <ul className="space-y-1">
                {group.items.map((item) => {
                  const active = pathname === item.to
                  return (
                    <li key={item.to}>
                      <Link
                        to={item.to}
                        aria-current={active ? 'page' : undefined}
                        className={cn(
                          'block px-2 py-1.5 rounded-md text-sm transition-colors',
                          active
                            ? 'bg-muted text-foreground font-medium'
                            : 'text-muted-foreground hover:text-foreground hover:bg-muted/60',
                        )}
                      >
                        {item.label}
                      </Link>
                    </li>
                  )
                })}
              </ul>
            </div>
          ))}
        </nav>
      </aside>
      <article className="prose-velox max-w-none">{children}</article>
    </div>
  )
}

export function Prose({ children }: { children: ReactNode }) {
  return (
    <div className="space-y-6 text-[15px] leading-relaxed text-foreground">{children}</div>
  )
}

export function DocsH1({ children }: { children: ReactNode }) {
  return (
    <h1 className="text-3xl font-semibold tracking-tight text-foreground mb-2">{children}</h1>
  )
}

export function DocsH2({ children }: { children: ReactNode }) {
  return (
    <h2 className="text-xl font-semibold tracking-tight text-foreground mt-10 mb-3 pt-2">
      {children}
    </h2>
  )
}

export function DocsH3({ children }: { children: ReactNode }) {
  return (
    <h3 className="text-base font-semibold text-foreground mt-6 mb-2">{children}</h3>
  )
}

export function DocsLead({ children }: { children: ReactNode }) {
  return <p className="text-muted-foreground text-base mb-4">{children}</p>
}

export function Code({ children, language }: { children: string; language?: string }) {
  return (
    <pre className="bg-muted/60 border border-border rounded-lg p-4 overflow-x-auto text-[13px] leading-relaxed font-mono my-4">
      {language && (
        <div className="text-[10px] uppercase tracking-wide text-muted-foreground mb-2 font-sans">
          {language}
        </div>
      )}
      <code>{children}</code>
    </pre>
  )
}

export function InlineCode({ children }: { children: ReactNode }) {
  return (
    <code className="font-mono text-[13px] px-1.5 py-0.5 rounded bg-muted/60 border border-border">
      {children}
    </code>
  )
}

export function Callout({ children, tone = 'info' }: { children: ReactNode; tone?: 'info' | 'warn' }) {
  return (
    <div
      className={cn(
        'border-l-2 pl-4 py-2 my-4 text-sm',
        tone === 'info' && 'border-primary/40 bg-primary/5 text-foreground',
        tone === 'warn' && 'border-amber-500/60 bg-amber-500/5 text-foreground',
      )}
    >
      {children}
    </div>
  )
}
