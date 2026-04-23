import { type ReactNode } from 'react'
import { PublicLayout, PublicPageHeader } from '@/components/PublicLayout'

export function LegalLayout({
  title,
  updated,
  children,
}: {
  title: string
  updated: string
  children: ReactNode
}) {
  return (
    <PublicLayout>
      <PublicPageHeader eyebrow="Legal" title={title} description={`Last updated ${updated}.`} />
      <div className="max-w-3xl mx-auto px-6 py-10 space-y-6 text-[15px] leading-relaxed text-foreground">
        <div className="border border-amber-500/30 bg-amber-500/5 rounded-lg p-4 text-sm">
          <strong className="text-foreground">Draft — pre-counsel.</strong> This document is a
          placeholder provided during the design-partner phase. The final version will be prepared
          with legal counsel before general availability. For a negotiated agreement tailored to your
          organization, contact{' '}
          <a className="underline underline-offset-2 hover:text-foreground" href="mailto:legal@velox.dev">
            legal@velox.dev
          </a>
          .
        </div>
        {children}
      </div>
    </PublicLayout>
  )
}

export function H2({ children }: { children: ReactNode }) {
  return (
    <h2 className="text-lg font-semibold tracking-tight text-foreground mt-8 mb-2">{children}</h2>
  )
}

export function P({ children }: { children: ReactNode }) {
  return <p className="text-muted-foreground leading-relaxed">{children}</p>
}
