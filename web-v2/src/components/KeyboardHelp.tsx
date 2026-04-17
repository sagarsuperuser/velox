import { useState, useEffect } from 'react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'

interface Shortcut {
  keys: string[]
  label: string
}

const navigationShortcuts: Shortcut[] = [
  { keys: ['g', 'd'], label: 'Go to Dashboard' },
  { keys: ['g', 'c'], label: 'Go to Customers' },
  { keys: ['g', 'i'], label: 'Go to Invoices' },
  { keys: ['g', 's'], label: 'Go to Subscriptions' },
  { keys: ['g', 'u'], label: 'Go to Usage' },
  { keys: ['g', 'p'], label: 'Go to Pricing' },
  { keys: ['g', 'a'], label: 'Go to Analytics' },
  { keys: ['g', 'k'], label: 'Go to API Keys' },
]

const actionShortcuts: Shortcut[] = [
  { keys: ['\u2318 K'], label: 'Search / Command Palette' },
  { keys: ['?'], label: 'Show this help' },
  { keys: ['Esc'], label: 'Close modal / dialog' },
]

function Kbd({ children }: { children: string }) {
  return (
    <kbd className="inline-flex items-center justify-center min-w-[24px] h-6 px-1.5 bg-muted border border-border rounded text-xs font-mono font-medium text-muted-foreground">
      {children}
    </kbd>
  )
}

function ShortcutRow({ shortcut }: { shortcut: Shortcut }) {
  return (
    <div className="flex items-center justify-between py-1.5">
      <span className="text-sm text-foreground">{shortcut.label}</span>
      <div className="flex items-center gap-1">
        {shortcut.keys.map((key, i) => (
          <span key={i} className="flex items-center gap-1">
            {i > 0 && <span className="text-xs text-muted-foreground">then</span>}
            <Kbd>{key}</Kbd>
          </span>
        ))}
      </div>
    </div>
  )
}

export function KeyboardHelp() {
  const [open, setOpen] = useState(false)

  useEffect(() => {
    const handler = () => setOpen(prev => !prev)
    document.addEventListener('velox:toggle-help', handler)
    return () => document.removeEventListener('velox:toggle-help', handler)
  }, [])

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle>Keyboard Shortcuts</DialogTitle>
          <DialogDescription>
            Navigate quickly with these keyboard shortcuts.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-5 mt-2">
          <div>
            <h3 className="text-xs uppercase tracking-wider text-muted-foreground font-medium mb-2">
              Navigation
            </h3>
            <div className="space-y-0.5">
              {navigationShortcuts.map((s, i) => (
                <ShortcutRow key={i} shortcut={s} />
              ))}
            </div>
          </div>

          <div className="border-t border-border pt-4">
            <h3 className="text-xs uppercase tracking-wider text-muted-foreground font-medium mb-2">
              Actions
            </h3>
            <div className="space-y-0.5">
              {actionShortcuts.map((s, i) => (
                <ShortcutRow key={i} shortcut={s} />
              ))}
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
