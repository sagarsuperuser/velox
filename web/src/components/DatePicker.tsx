import { useState, useRef, useEffect, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { DayPicker } from 'react-day-picker'
import { format, isValid, startOfDay, subDays, startOfMonth, subMonths } from 'date-fns'
import { Calendar, X, ChevronLeft, ChevronRight } from 'lucide-react'

interface DatePickerProps {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  label?: string
  includeTime?: boolean
  clearable?: boolean
  hint?: string
  minDate?: Date
  maxDate?: Date
  presets?: boolean // Show quick-select presets (Last 7d, 30d, etc.)
}

const PRESETS = [
  { label: 'Today', fn: () => format(new Date(), 'yyyy-MM-dd') },
  { label: 'Yesterday', fn: () => format(subDays(new Date(), 1), 'yyyy-MM-dd') },
  { label: '7 days ago', fn: () => format(subDays(new Date(), 7), 'yyyy-MM-dd') },
  { label: '30 days ago', fn: () => format(subDays(new Date(), 30), 'yyyy-MM-dd') },
  { label: '90 days ago', fn: () => format(subDays(new Date(), 90), 'yyyy-MM-dd') },
  { label: 'Start of month', fn: () => format(startOfMonth(new Date()), 'yyyy-MM-dd') },
  { label: 'Start of last month', fn: () => format(startOfMonth(subMonths(new Date(), 1)), 'yyyy-MM-dd') },
]

export function DatePicker({
  value, onChange, placeholder = 'Select date', label,
  includeTime = false, clearable = true, hint, minDate, maxDate,
  presets = false,
}: DatePickerProps) {
  const [open, setOpen] = useState(false)
  const [timeValue, setTimeValue] = useState('12:00')
  const [pos, setPos] = useState({ top: 0, left: 0 })
  const triggerRef = useRef<HTMLButtonElement>(null)
  const dropdownRef = useRef<HTMLDivElement>(null)

  const selectedDate = value ? (() => { const d = new Date(value); return isValid(d) ? d : undefined })() : undefined

  useEffect(() => {
    if (value && includeTime) {
      const d = new Date(value)
      if (isValid(d)) setTimeValue(format(d, 'HH:mm'))
    }
  }, [])

  // Position dropdown relative to trigger
  const updatePosition = useCallback(() => {
    if (!triggerRef.current) return
    const rect = triggerRef.current.getBoundingClientRect()
    const spaceBelow = window.innerHeight - rect.bottom
    const dropdownHeight = presets ? 380 : 360
    const top = spaceBelow > dropdownHeight ? rect.bottom + 4 : rect.top - dropdownHeight - 4
    const left = Math.min(rect.left, window.innerWidth - (presets ? 420 : 300))
    setPos({ top: Math.max(8, top), left: Math.max(8, left) })
  }, [presets])

  useEffect(() => {
    if (open) {
      updatePosition()
      window.addEventListener('scroll', updatePosition, true)
      window.addEventListener('resize', updatePosition)
    }
    return () => {
      window.removeEventListener('scroll', updatePosition, true)
      window.removeEventListener('resize', updatePosition)
    }
  }, [open, updatePosition])

  // Close on outside click
  useEffect(() => {
    if (!open) return
    const handleClick = (e: MouseEvent) => {
      if (triggerRef.current?.contains(e.target as Node)) return
      if (dropdownRef.current?.contains(e.target as Node)) return
      setOpen(false)
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [open])

  // Close on Escape
  useEffect(() => {
    if (!open) return
    const handleKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('keydown', handleKey)
    return () => document.removeEventListener('keydown', handleKey)
  }, [open])

  const handleDaySelect = (day: Date | undefined) => {
    if (!day) return
    if (includeTime) {
      const [h, m] = timeValue.split(':').map(Number)
      day.setHours(h, m, 0, 0)
      onChange(day.toISOString())
    } else {
      onChange(format(day, 'yyyy-MM-dd'))
    }
    if (!includeTime) setOpen(false)
  }

  const handleTimeChange = (t: string) => {
    setTimeValue(t)
    if (selectedDate && includeTime) {
      const d = new Date(selectedDate)
      const [h, m] = t.split(':').map(Number)
      d.setHours(h, m, 0, 0)
      onChange(d.toISOString())
    }
  }

  const handlePreset = (fn: () => string) => {
    onChange(fn())
    if (!includeTime) setOpen(false)
  }

  const goToToday = () => {
    const today = startOfDay(new Date())
    handleDaySelect(today)
  }

  const displayValue = selectedDate
    ? includeTime ? format(selectedDate, 'MMM d, yyyy h:mm a') : format(selectedDate, 'MMM d, yyyy')
    : ''

  const isDark = typeof document !== 'undefined' && document.documentElement.classList.contains('dark')

  return (
    <div>
      {label && (
        <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
          {label}
        </label>
      )}
      <button
        ref={triggerRef}
        type="button"
        aria-label={label || placeholder}
        aria-expanded={open}
        aria-haspopup="dialog"
        onClick={() => setOpen(!open)}
        className={`w-full flex items-center gap-2 px-3 py-2 border border-gray-200 dark:border-gray-700 rounded-lg text-sm text-left focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white dark:bg-gray-800 transition-colors hover:border-gray-300 dark:hover:border-gray-600 ${
          displayValue ? 'text-gray-900 dark:text-gray-100' : 'text-gray-400'
        }`}
      >
        <Calendar size={15} className="text-gray-400 shrink-0" />
        <span className="flex-1 truncate">{displayValue || placeholder}</span>
        {clearable && displayValue && (
          <span
            role="button"
            aria-label="Clear date"
            onClick={(e) => { e.stopPropagation(); onChange(''); setOpen(false) }}
            className="p-0.5 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 transition-colors"
          >
            <X size={14} />
          </span>
        )}
      </button>
      {hint && <p className="text-xs text-gray-500 mt-1">{hint}</p>}

      {open && createPortal(
        <div
          ref={dropdownRef}
          role="dialog"
          aria-label="Date picker"
          className="fixed z-[9999] bg-white dark:bg-gray-900 rounded-xl shadow-lg border border-gray-200 dark:border-gray-700 animate-scale-in flex overflow-hidden"
          style={{ top: pos.top, left: pos.left }}
        >
          {/* Presets sidebar */}
          {presets && (
            <div className="w-[130px] border-r border-gray-100 dark:border-gray-800 py-2 shrink-0">
              <p className="px-3 py-1 text-[10px] uppercase tracking-wider text-gray-400 font-semibold">Quick select</p>
              {PRESETS.map(p => (
                <button
                  key={p.label}
                  type="button"
                  onClick={() => handlePreset(p.fn)}
                  className="w-full text-left px-3 py-1.5 text-xs text-gray-600 dark:text-gray-400 hover:bg-gray-50 dark:hover:bg-gray-800 hover:text-gray-900 dark:hover:text-gray-100 transition-colors"
                >
                  {p.label}
                </button>
              ))}
            </div>
          )}

          {/* Calendar */}
          <div className="p-3">
            <style>{`
              .velox-cal { --accent: ${isDark ? '#818cf8' : '#635BFF'}; --accent-hover: ${isDark ? '#6366f1' : '#5851EB'}; }
              .velox-cal .rdp-month_caption { display: flex; align-items: center; justify-content: center; padding: 0 0 8px; }
              .velox-cal .rdp-caption_label { font-size: 14px; font-weight: 600; color: ${isDark ? '#f3f4f6' : '#111827'}; }
              .velox-cal .rdp-button_previous, .velox-cal .rdp-button_next { position: absolute; top: 12px; padding: 4px; border-radius: 6px; color: ${isDark ? '#9ca3af' : '#6b7280'}; cursor: pointer; border: none; background: none; display: flex; align-items: center; justify-content: center; }
              .velox-cal .rdp-button_previous:hover, .velox-cal .rdp-button_next:hover { background: ${isDark ? '#374151' : '#f3f4f6'}; color: ${isDark ? '#e5e7eb' : '#374151'}; }
              .velox-cal .rdp-button_previous { left: 12px; }
              .velox-cal .rdp-button_next { right: 12px; }
              .velox-cal .rdp-weekday { width: 36px; text-align: center; font-size: 11px; font-weight: 600; color: ${isDark ? '#6b7280' : '#9ca3af'}; padding: 4px 0; text-transform: uppercase; letter-spacing: 0.05em; }
              .velox-cal .rdp-day { width: 36px; height: 36px; display: flex; align-items: center; justify-content: center; font-size: 13px; border-radius: 8px; }
              .velox-cal .rdp-day_button { width: 100%; height: 100%; display: flex; align-items: center; justify-content: center; border-radius: 8px; cursor: pointer; transition: all 0.15s; border: none; background: none; color: ${isDark ? '#e5e7eb' : '#374151'}; font-size: 13px; }
              .velox-cal .rdp-day_button:hover { background: ${isDark ? '#374151' : '#f0f0ff'}; }
              .velox-cal .rdp-selected .rdp-day_button { background: var(--accent); color: white; font-weight: 600; }
              .velox-cal .rdp-selected .rdp-day_button:hover { background: var(--accent-hover); }
              .velox-cal .rdp-today .rdp-day_button { font-weight: 700; color: var(--accent); position: relative; }
              .velox-cal .rdp-today .rdp-day_button::after { content: ''; position: absolute; bottom: 4px; left: 50%; transform: translateX(-50%); width: 4px; height: 4px; border-radius: 50%; background: var(--accent); }
              .velox-cal .rdp-selected.rdp-today .rdp-day_button { color: white; }
              .velox-cal .rdp-selected.rdp-today .rdp-day_button::after { background: white; }
              .velox-cal .rdp-outside .rdp-day_button { color: ${isDark ? '#4b5563' : '#d1d5db'}; }
              .velox-cal .rdp-disabled .rdp-day_button { color: ${isDark ? '#374151' : '#e5e7eb'}; cursor: not-allowed; }
              .velox-cal .rdp-disabled .rdp-day_button:hover { background: none; }
              .velox-cal .rdp-nav { display: flex; gap: 4px; }
              .velox-cal .rdp-months { display: flex; gap: 16px; }
              .velox-cal .rdp-weekdays { display: flex; }
              .velox-cal .rdp-week { display: flex; }
            `}</style>
            <DayPicker
              mode="single"
              selected={selectedDate}
              onSelect={handleDaySelect}
              defaultMonth={selectedDate}
              showOutsideDays
              className="velox-cal"
              disabled={[
                ...(minDate ? [{ before: minDate }] : []),
                ...(maxDate ? [{ after: maxDate }] : []),
              ]}
              components={{
                Chevron: ({ orientation }) =>
                  orientation === 'left'
                    ? <ChevronLeft size={16} />
                    : <ChevronRight size={16} />,
              }}
            />

            {/* Footer: Today button + time input */}
            <div className={`border-t border-gray-100 dark:border-gray-800 mt-1 pt-2 flex items-center ${includeTime ? 'justify-between' : 'justify-center'}`}>
              <button
                type="button"
                onClick={goToToday}
                className="text-xs font-medium text-velox-600 dark:text-velox-400 hover:text-velox-700 dark:hover:text-velox-300 transition-colors px-2 py-1 rounded hover:bg-velox-50 dark:hover:bg-velox-900/20"
              >
                Today
              </button>

              {includeTime && (
                <div className="flex items-center gap-2">
                  <input
                    type="time"
                    value={timeValue}
                    onChange={(e) => handleTimeChange(e.target.value)}
                    aria-label="Select time"
                    className="px-2 py-1 border border-gray-200 dark:border-gray-700 rounded-lg text-xs bg-white dark:bg-gray-800 dark:text-gray-100 focus:outline-none focus:ring-2 focus:ring-velox-500"
                  />
                  <button
                    type="button"
                    onClick={() => setOpen(false)}
                    className="px-3 py-1 bg-velox-600 text-white rounded-lg text-xs font-medium hover:bg-velox-700 transition-colors"
                  >
                    Done
                  </button>
                </div>
              )}
            </div>
          </div>
        </div>,
        document.body
      )}
    </div>
  )
}
