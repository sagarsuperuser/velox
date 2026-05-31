import * as React from 'react'
import { format, parse, isValid } from 'date-fns'
import { DayPicker } from 'react-day-picker'
import { Calendar, X, ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

interface DatePickerProps {
  value: string                    // ISO date string (yyyy-mm-dd) or ''
  onChange: (value: string) => void
  placeholder?: string
  className?: string
  minDate?: Date                   // Disable dates before this
}

// Display format the user sees ("May 4, 2026"). Source of truth on
// commit is yyyy-MM-dd via onChange.
const DISPLAY_FORMAT = 'MMM d, yyyy'

// Accepted typed input formats, tried in order. Looser formats sit
// last so a precise match wins when ambiguous (e.g. "01-02-2026"
// matches MM-dd-yyyy before falling through to d/M/yyyy guesses).
const PARSE_FORMATS = [
  'yyyy-MM-dd',
  'MMM d, yyyy', 'MMM d yyyy',
  'MMMM d, yyyy', 'MMMM d yyyy',
  'd MMM yyyy', 'd MMMM yyyy',
  'M/d/yyyy', 'MM/dd/yyyy',
  'M-d-yyyy', 'MM-dd-yyyy',
] as const

// ParseResult discriminates the two ways typed input can fail. The
// caller renders different copy for each so the user knows whether
// to fix the format or pick a later date.
type ParseResult =
  | { ok: true; value: string }              // parsed + within range
  | { ok: false; reason: 'format' }          // didn't match any accepted format
  | { ok: false; reason: 'min'; min: Date }  // parsed fine but earlier than minDate

// parseTypedDate accepts the strings users actually type and returns
// a discriminated result. Whitespace runs are collapsed to single
// spaces before matching so copy-paste with weird spacing
// ("Jul   1,  2026") still parses.
function parseTypedDate(input: string, minDate?: Date): ParseResult | { ok: true; value: '' } {
  const trimmed = input.trim().replace(/\s+/g, ' ')
  if (!trimmed) return { ok: true, value: '' }
  for (const f of PARSE_FORMATS) {
    const d = parse(trimmed, f, new Date())
    if (isValid(d)) {
      if (minDate) {
        const minStart = new Date(minDate.getFullYear(), minDate.getMonth(), minDate.getDate())
        const dStart = new Date(d.getFullYear(), d.getMonth(), d.getDate())
        if (dStart < minStart) return { ok: false, reason: 'min', min: minDate }
      }
      return { ok: true, value: format(d, 'yyyy-MM-dd') }
    }
  }
  return { ok: false, reason: 'format' }
}

export function DatePicker({ value, onChange, placeholder = 'Pick a date', className, minDate }: DatePickerProps) {
  const [open, setOpen] = React.useState(false)
  const [dropUp, setDropUp] = React.useState(false)
  const [alignRight, setAlignRight] = React.useState(false)
  const [inputText, setInputText] = React.useState('')
  const [errorReason, setErrorReason] = React.useState<null | 'format' | 'min'>(null)
  const hasError = errorReason !== null
  const ref = React.useRef<HTMLDivElement>(null)
  const inputRef = React.useRef<HTMLInputElement>(null)
  const errorId = React.useId()

  const selectedDate = value ? new Date(value + 'T00:00:00') : undefined

  // Sync the input text when the external value changes (e.g. caller
  // resets the field, or the calendar popover commits a date).
  // Three things must be true to skip the sync:
  //  1. The user is mid-typing — never overwrite their draft.
  //  2. The field is currently in an error state — the parent's value
  //     just got cleared by our own commit() failure path; mirroring
  //     that empty back into inputText would erase the user's bad
  //     input that they need to see in red to fix.
  //  3. (implicit via setState bailout) text already matches.
  React.useEffect(() => {
    if (inputRef.current && document.activeElement === inputRef.current) return
    if (hasError) return
    setInputText(value ? format(new Date(value + 'T00:00:00'), DISPLAY_FORMAT) : '')
  }, [value, hasError])

  // Clear stale 'min' errors when the minDate constraint shifts (e.g.
  // upstream a sibling field changes "From" so "To" might now be
  // valid). Format errors are about the user's input, not the
  // constraint, so they don't clear here.
  //
  // errorReason is read via functional setState so it stays out of
  // the deps array — including it would cause a self-clearing loop
  // the moment commitInput() set the value to 'min', because this
  // effect would re-run on the errorReason change and immediately
  // null it. Bug discovered when typing a back date made the field
  // appear to vanish instead of showing the constraint message.
  const minDateKey = minDate ? minDate.toISOString() : null
  React.useEffect(() => {
    setErrorReason(prev => (prev === 'min' ? null : prev))
  }, [minDateKey])

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

  const commitInput = () => {
    const result = parseTypedDate(inputText, minDate)
    if (!result.ok) {
      // Invalid: surface a red border + inline error AND clear the
      // parent's value so any submit gating tied to a non-empty date
      // flips to disabled. The user's bad input stays visible (the
      // sync effect skips while hasError is true) so they can fix
      // it without losing what they typed.
      setErrorReason(result.reason)
      if (value !== '') onChange('')
      return
    }
    setErrorReason(null)
    if (result.value === '') {
      if (value) onChange('')
    } else if (result.value !== value) {
      onChange(result.value)
      // Reformat the input so "5/4/2026" snaps to "May 4, 2026"
      // immediately after blur, without waiting for a re-render
      // round-trip via the value sync effect.
      setInputText(format(new Date(result.value + 'T00:00:00'), DISPLAY_FORMAT))
    }
  }

  const handleSelect = (day: Date | undefined) => {
    if (day) {
      // Clear any pending error BEFORE onChange. The sync effect
      // skips while hasError is true, so without this the input
      // would keep showing the bad typed text after the user picked
      // a fresh date from the calendar — value updates, field stays
      // wrong.
      setErrorReason(null)
      onChange(format(day, 'yyyy-MM-dd'))
      setOpen(false)
    }
  }

  const handleClear = () => {
    onChange('')
    setInputText('')
    setErrorReason(null)
    inputRef.current?.focus()
  }

  return (
    <div ref={ref} className={cn('relative', className)}>
      <div
        className={cn(
          'flex h-9 items-center gap-1.5 rounded-md border bg-transparent dark:bg-input/30 px-2.5 transition-colors cursor-text',
          'has-[input:focus-visible]:border-ring has-[input:focus-visible]:ring-3 has-[input:focus-visible]:ring-ring/50',
          hasError ? 'border-destructive' : 'border-input',
        )}
        onClick={(e) => {
          // Click anywhere on the wrapper focuses the input — except
          // when the click landed on one of the trailing buttons,
          // which manage their own focus.
          if (e.target === e.currentTarget) inputRef.current?.focus()
        }}
      >
        <Calendar className="h-4 w-4 text-muted-foreground shrink-0" />
        <input
          ref={inputRef}
          type="text"
          value={inputText}
          onChange={(e) => { setInputText(e.target.value); if (hasError) setErrorReason(null) }}
          onBlur={commitInput}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              commitInput()
              setOpen(false)
            } else if (e.key === 'ArrowDown' && !open) {
              e.preventDefault()
              setOpen(true)
            }
          }}
          placeholder={placeholder}
          aria-invalid={hasError || undefined}
          aria-describedby={hasError ? errorId : undefined}
          autoComplete="off"
          spellCheck={false}
          className="flex-1 min-w-0 bg-transparent outline-none text-sm placeholder:text-muted-foreground"
        />
        {value && (
          <button
            type="button"
            onClick={handleClear}
            aria-label="Clear date"
            className="p-0.5 rounded-sm hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-label={open ? 'Close calendar' : 'Open calendar'}
          aria-expanded={open}
          className="p-0.5 rounded-sm hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
        >
          <ChevronDown className={cn('h-3.5 w-3.5 transition-transform', open && 'rotate-180')} />
        </button>
      </div>
      {errorReason === 'format' && (
        <p id={errorId} role="alert" className="text-xs text-destructive mt-1">Couldn’t read that date. Try <span className="font-mono">2026-05-04</span> or <span className="font-mono">May 4, 2026</span>.</p>
      )}
      {errorReason === 'min' && minDate && (
        <p id={errorId} role="alert" className="text-xs text-destructive mt-1">
          Date must be on or after <span className="font-mono">{format(minDate, DISPLAY_FORMAT)}</span>.
        </p>
      )}

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
            // captionLayout="dropdown" gives month + year dropdowns
            // next to the prev/next arrows (Stripe / Linear / Vercel
            // pattern). Without this, jumping to a year far from
            // today requires clicking the arrow ~12 times per year,
            // which is what the user hit on the pause-collection
            // resume-date field.
            captionLayout="dropdown"
            // startMonth / endMonth bound the year dropdown. Anchor
            // the lower bound to minDate when provided so disabled
            // years don't pollute the menu; otherwise default to a
            // 5-year past + 10-year future window from today. The
            // upper bound is generous because annual-billing trial
            // extensions and credit-grant expiries reach years out.
            startMonth={minDate ? new Date(minDate.getFullYear(), 0) : new Date(new Date().getFullYear() - 5, 0)}
            endMonth={new Date(new Date().getFullYear() + 10, 11)}
          />
          <style>{`
            .velox-cal { --accent: var(--primary); }
            .velox-cal .rdp-month_caption { display:flex; align-items:center; justify-content:center; padding:0 0 8px; }
            .velox-cal .rdp-caption_label { font-size:14px; font-weight:600; color:var(--foreground); }
            /* Dropdown caption (captionLayout="dropdown"). Native <select>
               theming differs across browsers; we strip the OS chrome and
               style a compact pill that matches the rest of the popover.
               .rdp-dropdowns is the flex row holding both selects; each
               select is wrapped in a .rdp-dropdown_root span by the lib. */
            .velox-cal .rdp-dropdowns { display:flex; gap:6px; align-items:center; }
            .velox-cal .rdp-dropdown_root { position:relative; }
            .velox-cal .rdp-dropdown { appearance:none; -webkit-appearance:none; background:transparent; border:1px solid var(--border); border-radius:6px; padding:4px 22px 4px 8px; font-size:13px; font-weight:600; color:var(--foreground); cursor:pointer; outline:none; }
            .velox-cal .rdp-dropdown:hover { border-color:var(--ring); }
            .velox-cal .rdp-dropdown:focus-visible { border-color:var(--ring); box-shadow:0 0 0 3px color-mix(in oklab, var(--ring) 50%, transparent); }
            .velox-cal .rdp-dropdown_root::after { content:''; position:absolute; right:8px; top:50%; transform:translateY(-25%); width:0; height:0; border-left:4px solid transparent; border-right:4px solid transparent; border-top:4px solid var(--muted-foreground); pointer-events:none; }
            .velox-cal .rdp-caption_label[aria-hidden='true'] { display:none; }
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
          {/* Quick-select Today shortcut. Honour minDate by greying
              out the button when today < minDate — the day-grid above
              already disables today's cell, but this footer button
              has its own click handler and would otherwise bypass the
              constraint. Compare at start-of-day on both sides so a
              minDate passed with a non-zero time still gates today
              consistently. */}
          {(() => {
            const todayStart = new Date()
            todayStart.setHours(0, 0, 0, 0)
            const minStart = minDate
              ? new Date(minDate.getFullYear(), minDate.getMonth(), minDate.getDate())
              : null
            const todayBlocked = !!minStart && todayStart < minStart
            const todayBtn = (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="text-xs"
                disabled={todayBlocked}
                onClick={() => {
                  // Same error-clear contract as handleSelect: a
                  // calendar-driven commit must reset any typed-input
                  // error so the field reflects the new value.
                  setErrorReason(null)
                  onChange(format(new Date(), 'yyyy-MM-dd'))
                  setOpen(false)
                }}
              >
                Today
              </Button>
            )
            return (
              <div className="border-t border-border mt-2 pt-2 flex justify-center">
                {todayBlocked ? (
                  // Tooltip-component path for the disabled state. The
                  // native `title` attribute is unreliable on disabled
                  // buttons because Button has disabled:pointer-events-
                  // none, which interferes with browser hit-testing of
                  // the wrapping span across browsers/scroll positions.
                  // The shadcn Tooltip primitive registers focus/pointer
                  // listeners on the trigger itself and renders the
                  // popup via Portal, so it fires reliably. Wrapping
                  // span gives the trigger a focusable target since
                  // disabled buttons don't receive focus events.
                  <Tooltip>
                    <TooltipTrigger render={<span className="inline-block cursor-not-allowed" />}>
                      {todayBtn}
                    </TooltipTrigger>
                    <TooltipContent>
                      Today is below the minimum allowed date ({format(minStart!, 'MMM d, yyyy')}).
                    </TooltipContent>
                  </Tooltip>
                ) : (
                  todayBtn
                )}
              </div>
            )
          })()}
        </div>
      )}
    </div>
  )
}
