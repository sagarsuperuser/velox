import * as React from 'react'
import { format } from 'date-fns'
import { DayPicker } from 'react-day-picker'
import { Calendar, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'

interface DatePickerProps {
  value: string                    // ISO date string (yyyy-mm-dd) or ''
  onChange: (value: string) => void
  placeholder?: string
  className?: string
  minDate?: Date                   // Disable dates before this
}

export function DatePicker({ value, onChange, placeholder = 'Pick a date', className, minDate }: DatePickerProps) {
  const [open, setOpen] = React.useState(false)
  const [dropUp, setDropUp] = React.useState(false)
  const [alignRight, setAlignRight] = React.useState(false)
  const ref = React.useRef<HTMLDivElement>(null)

  const selectedDate = value ? new Date(value + 'T00:00:00') : undefined

  // Determine if calendar should open upward and/or right-align.
  // Popover is ~300px wide; right-align when the trigger is close enough to
  // the viewport's right edge that a left-anchored popover would clip.
  React.useEffect(() => {
    if (!open || !ref.current) return
    const rect = ref.current.getBoundingClientRect()
    const spaceBelow = window.innerHeight - rect.bottom
    const spaceRight = window.innerWidth - rect.left
    setDropUp(spaceBelow < 380)
    setAlignRight(spaceRight < 300)
  }, [open])

  // Close on outside click
  React.useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  // Close on Escape
  React.useEffect(() => {
    if (!open) return
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [open])

  const handleSelect = (day: Date | undefined) => {
    if (day) {
      onChange(format(day, 'yyyy-MM-dd'))
      setOpen(false)
    }
  }

  return (
    <div ref={ref} className={cn('relative', className)}>
      <Button
        type="button"
        variant="outline"
        onClick={() => setOpen(!open)}
        className={cn(
          'w-full justify-start text-left font-normal h-9',
          !value && 'text-muted-foreground'
        )}
      >
        <Calendar className="mr-2 h-4 w-4 shrink-0" />
        <span className="flex-1 truncate">
          {selectedDate ? format(selectedDate, 'MMM d, yyyy') : placeholder}
        </span>
        {value && (
          <span
            role="button"
            onClick={(e) => { e.stopPropagation(); onChange(''); }}
            className="ml-1 p-0.5 rounded-sm hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
          >
            <X className="h-3.5 w-3.5" />
          </span>
        )}
      </Button>

      {open && (
        <div className={cn(
          'absolute z-50 rounded-lg border border-border bg-popover p-3 shadow-lg animate-in fade-in-0 zoom-in-95',
          dropUp ? 'bottom-full mb-1' : 'top-full mt-1',
          alignRight ? 'right-0' : 'left-0'
        )}>
          <DayPicker
            mode="single"
            selected={selectedDate}
            onSelect={handleSelect}
            defaultMonth={selectedDate}
            showOutsideDays
            className="velox-cal"
            disabled={minDate ? { before: minDate } : undefined}
          />
          <style>{`
            .velox-cal { --accent: var(--primary); }
            .velox-cal .rdp-month_caption { display:flex; align-items:center; justify-content:center; padding:0 0 8px; }
            .velox-cal .rdp-caption_label { font-size:14px; font-weight:600; color:var(--foreground); }
            .velox-cal .rdp-button_previous, .velox-cal .rdp-button_next { position:absolute; top:12px; padding:6px; border-radius:6px; color:var(--muted-foreground); cursor:pointer; border:none; background:none; display:flex; }
            .velox-cal .rdp-button_previous:hover, .velox-cal .rdp-button_next:hover { background:var(--accent); color:var(--primary-foreground); }
            .velox-cal .rdp-button_previous { left:12px; }
            .velox-cal .rdp-button_next { right:12px; }
            .velox-cal .rdp-weekday { width:36px; text-align:center; font-size:11px; font-weight:600; color:var(--muted-foreground); padding:4px 0; text-transform:uppercase; letter-spacing:0.05em; }
            .velox-cal .rdp-day { width:36px; height:36px; display:flex; align-items:center; justify-content:center; }
            .velox-cal .rdp-day_button { width:100%; height:100%; display:flex; align-items:center; justify-content:center; border-radius:8px; cursor:pointer; transition:all 0.15s; border:none; background:none; color:var(--foreground); font-size:13px; }
            .velox-cal .rdp-day_button:hover { background:var(--accent); color:var(--primary-foreground); }
            .velox-cal .rdp-selected .rdp-day_button { background:var(--primary); color:var(--primary-foreground); font-weight:600; }
            .velox-cal .rdp-selected .rdp-day_button:hover { background:var(--primary); opacity:0.9; }
            .velox-cal .rdp-today .rdp-day_button { font-weight:700; position:relative; }
            .velox-cal .rdp-today .rdp-day_button::after { content:''; position:absolute; bottom:3px; left:50%; transform:translateX(-50%); width:4px; height:4px; border-radius:50%; background:var(--primary); }
            .velox-cal .rdp-selected.rdp-today .rdp-day_button::after { background:var(--primary-foreground); }
            .velox-cal .rdp-outside .rdp-day_button { color:var(--muted-foreground); opacity:0.4; }
            .velox-cal .rdp-disabled .rdp-day_button { opacity:0.25; cursor:not-allowed; }
            .velox-cal .rdp-nav { display:flex; gap:4px; }
            .velox-cal .rdp-months { display:flex; gap:16px; }
            .velox-cal .rdp-weekdays { display:flex; }
            .velox-cal .rdp-week { display:flex; }
          `}</style>
          <div className="border-t border-border mt-2 pt-2 flex justify-center">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="text-xs"
              onClick={() => { onChange(format(new Date(), 'yyyy-MM-dd')); setOpen(false); }}
            >
              Today
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
