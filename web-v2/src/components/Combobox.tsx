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
}

// Searchable dropdown built on cmdk. Matches the DatePicker positioning
// pattern used elsewhere in the app (ref + outside-click + escape, flip-up
// when space is tight). No Radix Popover dep; fewer layers, consistent look.
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
}: ComboboxProps) {
  const [open, setOpen] = React.useState(false)
  const [dropUp, setDropUp] = React.useState(false)
  const ref = React.useRef<HTMLDivElement>(null)

  const selected = options.find(o => o.value === value)

  React.useEffect(() => {
    if (!open || !ref.current) return
    const rect = ref.current.getBoundingClientRect()
    const spaceBelow = window.innerHeight - rect.bottom
    setDropUp(spaceBelow < 340)
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
          className={cn(
            'absolute z-50 w-full rounded-md border border-border bg-popover shadow-md',
            dropUp ? 'bottom-full mb-1' : 'top-full mt-1',
          )}
        >
          <Command
            filter={(val, search, keywords) => {
              const haystack = [val, ...(keywords ?? [])].join(' ').toLowerCase()
              return haystack.includes(search.toLowerCase()) ? 1 : 0
            }}
          >
            <CommandInput placeholder="Search..." autoFocus />
            <CommandList className="max-h-72">
              <CommandEmpty>{emptyMessage}</CommandEmpty>
              <CommandGroup>
                {options.map(opt => (
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
