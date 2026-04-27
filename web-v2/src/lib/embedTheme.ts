// Theme + accent overrides for the public cost-dashboard embed.
//
// The embed is rendered inside an iframe hosted under the operator's
// domain. Dark-mode-by-default — most product surfaces look better in
// dark, and operators who prefer light pass `?theme=light` once. The
// iframe deliberately ignores localStorage and prefers-color-scheme:
// theme should be deterministic from the URL so the host page's choice
// always wins.
//
// `?accent=<6-digit-hex>` overrides the primary brand color (the cycle
// progress bar and any focus rings). Anything that doesn't match the
// strict hex regex is ignored — never trust query-param input to be
// CSS-safe.

const HEX6 = /^#[0-9a-fA-F]{6}$/

export type EmbedTheme = 'dark' | 'light'

export interface EmbedThemeOptions {
  theme: EmbedTheme
  accent: string | null
}

export function parseEmbedTheme(search: string): EmbedThemeOptions {
  const params = new URLSearchParams(search)
  const theme: EmbedTheme = params.get('theme') === 'light' ? 'light' : 'dark'
  const accentParam = params.get('accent')
  const accent = accentParam && HEX6.test(accentParam) ? accentParam : null
  return { theme, accent }
}

export function applyEmbedTheme(opts: EmbedThemeOptions, root: HTMLElement = document.documentElement): void {
  if (opts.theme === 'dark') {
    root.classList.add('dark')
  } else {
    root.classList.remove('dark')
  }
  if (opts.accent) {
    root.style.setProperty('--primary', opts.accent)
    root.style.setProperty('--ring', opts.accent)
  } else {
    root.style.removeProperty('--primary')
    root.style.removeProperty('--ring')
  }
}
