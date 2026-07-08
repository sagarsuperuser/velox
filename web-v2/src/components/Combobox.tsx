import * as React from 'react'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'
import { ChevronsUpDown, Check, X } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface ComboboxOption {
  value: string
  label: string
  // Optional search helpers (e.g. include ISO code + local name + english name).
  keywords?: string[]
  // Optional leading visual (flag emoji, icon, currency symbol).
  prefix?: React.ReactNode
}

interface ComboboxProps {
  value: string
  onChange: (value: string) => void
  options: ComboboxOption[]
  placeholder?: string
  emptyMessage?: string
  className?: string
  triggerClassName?: string
  disabled?: boolean
  clearable?: boolean
  // Override the display in the trigger; defaults to selected option's label.
  renderSelected?: (opt: ComboboxOption | undefined) => React.ReactNode
  // Server-side search mode: the parent owns filtering. cmdk's internal
  // matcher is disabled (every option shown as-is) and each keystroke is
  // reported so the parent can refetch. Without this, any picker backed
  // by a paginated list can only ever match within the first page —
  // rows past the page cap are unfindable no matter what the user types.
  onSearchChange?: (search: string) => void
  serverFiltered?: boolean
}

// Searchable dropdown built on cmdk with a hand-rolled anchored popover
// (ref + outside-click + escape, flip-up when space is tight). No Radix/Base UI
// Popover dep here; fewer layers, consistent look. (DatePicker used to share
// this pattern but has since moved to Base UI's Popover — see date-picker.tsx.)
export function Combobox({
  value,
  onChange,
  options,
  placeholder = 'Select...',
  emptyMessage = 'No results',
  className,
  triggerClassName,
  disabled,
  clearable,
  renderSelected,
  onSearchChange,
  serverFiltered,
}: ComboboxProps) {
  const [open, setOpen] = React.useState(false)
  const [dropUp, setDropUp] = React.useState(false)
  const [alignRight, setAlignRight] = React.useState(false)
  // Available height for the popover, computed against actual viewport
  // room (above OR below the trigger, whichever side we pick). Without
  // this, opening the dropdown with DevTools docked at the bottom
  // shrinks window.innerHeight, the dropUp branch fires, but the
  // popover still wants ~340px upward — overflowing past any sibling
  // field above the trigger and rendering over it.
  const [maxHeight, setMaxHeight] = React.useState<number>(340)
  const ref = React.useRef<HTMLDivElement>(null)

  // Dedupe options by value defensively. CommandItem keys on
  // opt.value; a caller that hands us duplicates (e.g. tzdb listing
  // the same canonical zone twice) blows up React with a duplicate-
  // key warning and renders the same row twice. Map preserves the
  // first occurrence of each value, which is the conventional pick
  // for sorted lists.
  const dedupedOptions = React.useMemo(() => {
    const seen = new Map<string, ComboboxOption>()
    for (const o of options) {
      if (!seen.has(o.value)) seen.set(o.value, o)
    }
    return Array.from(seen.values())
  }, [options])

  const selected = dedupedOptions.find(o => o.value === value)

  React.useEffect(() => {
    if (!open || !ref.current) return
    const rect = ref.current.getBoundingClientRect()
    const margin = 16 // breathing room from viewport edge / DevTools split
    const spaceBelow = window.innerHeight - rect.bottom - margin
    const spaceAbove = rect.top - margin
    // Prefer downward unless above has materially more room AND below
    // is too tight for a usable list. Symmetric: same threshold for
    // both sides so flip is deterministic.
    const useDropUp = spaceAbove > spaceBelow && spaceBelow < 240
    setDropUp(useDropUp)
    // Cap the popover at whichever side we picked. Floor at 160px so a
    // truly squished viewport still shows the search input + a couple
    // of rows scrolling.
    const available = useDropUp ? spaceAbove : spaceBelow
    setMaxHeight(Math.max(160, Math.min(340, available)))
    // Right-align when the trigger sits close enough to the viewport's
    // right edge that the popover (which can grow up to 32rem wide for
    // long labels like "America/Los_Angeles (UTC-08:00)") would clip.
    const popoverWidth = 384 // px, matches md (~24rem) — close enough
    setAlignRight(window.innerWidth - rect.left < popoverWidth)
  }, [open])

  React.useEffect(() => {
    if (!open) return
    const outside = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', outside)
    document.addEventListener('keydown', esc)
    return () => {
      document.removeEventListener('mousedown', outside)
      document.removeEventListener('keydown', esc)
    }
  }, [open])

  const handleSelect = (val: string) => {
    onChange(val)
    setOpen(false)
  }

  const handleClear = (e: React.MouseEvent) => {
    e.stopPropagation()
    onChange('')
  }

  return (
    <div ref={ref} className={cn('relative w-full', className)}>
      <button
        type="button"
        disabled={disabled}
        onClick={() => !disabled && setOpen(o => !o)}
        className={cn(
          'w-full flex items-center justify-between gap-2 rounded-md border border-input bg-background px-3 py-2 text-sm text-foreground',
          'ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
          'disabled:cursor-not-allowed disabled:opacity-50',
          !selected && 'text-muted-foreground',
          triggerClassName,
        )}
        aria-expanded={open}
        aria-haspopup="listbox"
      >
        <span className="flex items-center gap-2 min-w-0 truncate">
          {selected?.prefix}
          <span className="truncate">
            {renderSelected ? renderSelected(selected) : (selected?.label ?? placeholder)}
          </span>
        </span>
        <span className="flex items-center gap-1 shrink-0">
          {clearable && selected && !disabled && (
            <span
              role="button"
              tabIndex={-1}
              aria-label="Clear selection"
              onClick={handleClear}
              className="h-5 w-5 rounded text-muted-foreground hover:text-foreground hover:bg-accent flex items-center justify-center"
            >
              <X size={12} />
            </span>
          )}
          <ChevronsUpDown size={14} className="text-muted-foreground" />
        </span>
      </button>

      {open && (
        <div
          style={{ maxHeight }}
          className={cn(
            // min-w-full lets the popover be at least as wide as the
            // trigger but grow up to max-w when content needs more
            // space (long timezone names, etc). Prior `w-full` clipped
            // labels in narrow columns. max-w caps the natural growth
            // so the popover never overflows the viewport on small
            // screens; alignRight handles the right-edge case.
            'absolute z-50 min-w-full max-w-[min(32rem,calc(100vw-2rem))] flex flex-col overflow-hidden rounded-md border border-border bg-popover shadow-md',
            dropUp ? 'bottom-full mb-1' : 'top-full mt-1',
            alignRight ? 'right-0' : 'left-0',
          )}
        >
          <Command
            shouldFilter={!serverFiltered}
            filter={(val, search, keywords) => {
              const haystack = [val, ...(keywords ?? [])].join(' ').toLowerCase()
              return haystack.includes(search.toLowerCase()) ? 1 : 0
            }}
            className="flex flex-1 flex-col min-h-0"
          >
            <CommandInput placeholder="Search..." autoFocus onValueChange={onSearchChange} />
            {/* CommandList scrolls within the remaining vertical space
                after the search input. flex-1 + min-h-0 is the
                standard pattern to let a flex child actually shrink
                below its content height. */}
            <CommandList className="flex-1 min-h-0 overflow-y-auto">
              <CommandEmpty>{emptyMessage}</CommandEmpty>
              <CommandGroup>
                {dedupedOptions.map(opt => (
                  <CommandItem
                    key={opt.value}
                    value={opt.label}
                    keywords={[opt.value, ...(opt.keywords ?? [])]}
                    onSelect={() => handleSelect(opt.value)}
                  >
                    <span className="flex items-center gap-2 min-w-0 flex-1 truncate">
                      {opt.prefix}
                      <span className="truncate">{opt.label}</span>
                    </span>
                    <Check
                      size={14}
                      className={cn('ml-2', value === opt.value ? 'opacity-100' : 'opacity-0')}
                    />
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </div>
      )}
    </div>
  )
}
