import { useState, useRef, useEffect, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { DayPicker } from 'react-day-picker'
import { format, isValid } from 'date-fns'
import { Calendar, X } from 'lucide-react'

interface DatePickerProps {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  label?: string
  includeTime?: boolean
  clearable?: boolean
  hint?: string
}

export function DatePicker({
  value, onChange, placeholder = 'Select date', label,
  includeTime = false, clearable = true, hint,
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
    const dropdownHeight = 340
    const top = spaceBelow > dropdownHeight ? rect.bottom + 4 : rect.top - dropdownHeight - 4
    setPos({ top: Math.max(8, top), left: rect.left })
  }, [])

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

  const displayValue = selectedDate
    ? includeTime ? format(selectedDate, 'MMM d, yyyy h:mm a') : format(selectedDate, 'MMM d, yyyy')
    : ''

  return (
    <div>
      {label && <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">{label}</label>}
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen(!open)}
        className={`w-full flex items-center gap-2 px-3 py-2 border border-gray-200 dark:border-gray-700 rounded-lg text-sm text-left focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white dark:bg-gray-800 transition-colors hover:border-gray-300 dark:hover:border-gray-600 ${
          displayValue ? 'text-gray-900 dark:text-gray-100' : 'text-gray-400'
        }`}
      >
        <Calendar size={15} className="text-gray-400 shrink-0" />
        <span className="flex-1 truncate">{displayValue || placeholder}</span>
        {clearable && displayValue && (
          <span
            onClick={(e) => { e.stopPropagation(); onChange(''); setOpen(false) }}
            className="p-0.5 rounded hover:bg-gray-100 text-gray-400 hover:text-gray-600 transition-colors"
          >
            <X size={14} />
          </span>
        )}
      </button>
      {hint && <p className="text-xs text-gray-500 mt-1">{hint}</p>}

      {open && createPortal(
        <div
          ref={dropdownRef}
          className="fixed z-[9999] bg-white dark:bg-gray-900 rounded-xl shadow-lg border border-gray-200 dark:border-gray-700 p-4 animate-scale-in"
          style={{ top: pos.top, left: pos.left, minWidth: 280 }}
        >
          <style>{`
            .velox-cal .rdp-month_caption { display: flex; align-items: center; justify-content: center; padding: 0 0 8px; }
            .velox-cal .rdp-caption_label { font-size: 14px; font-weight: 600; color: #111827; }
            .velox-cal .rdp-button_previous, .velox-cal .rdp-button_next { position: absolute; top: 16px; padding: 4px; border-radius: 6px; color: #6b7280; cursor: pointer; }
            .velox-cal .rdp-button_previous:hover, .velox-cal .rdp-button_next:hover { background: #f3f4f6; }
            .velox-cal .rdp-button_previous { left: 16px; }
            .velox-cal .rdp-button_next { right: 16px; }
            .velox-cal .rdp-weekday { width: 36px; text-align: center; font-size: 11px; font-weight: 500; color: #9ca3af; padding: 4px 0; }
            .velox-cal .rdp-day { width: 36px; height: 36px; display: flex; align-items: center; justify-content: center; font-size: 13px; border-radius: 8px; }
            .velox-cal .rdp-day_button { width: 100%; height: 100%; display: flex; align-items: center; justify-content: center; border-radius: 8px; cursor: pointer; transition: background 0.15s; border: none; background: none; }
            .velox-cal .rdp-day_button:hover { background: #f0f4ff; }
            .velox-cal .rdp-selected .rdp-day_button { background: #6d28d9; color: white; font-weight: 500; }
            .velox-cal .rdp-selected .rdp-day_button:hover { background: #5b21b6; }
            .velox-cal .rdp-today .rdp-day_button { font-weight: 700; color: #6d28d9; }
            .velox-cal .rdp-selected.rdp-today .rdp-day_button { color: white; }
            .velox-cal .rdp-outside .rdp-day_button { color: #d1d5db; }
            .velox-cal .rdp-disabled .rdp-day_button { color: #e5e7eb; cursor: not-allowed; }
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
          />
          {includeTime && (
            <div className="border-t border-gray-100 mt-2 pt-2 flex items-center gap-2">
              <label className="text-xs text-gray-500">Time:</label>
              <input type="time" value={timeValue}
                onChange={(e) => handleTimeChange(e.target.value)}
                className="px-2 py-1 border border-gray-200 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-velox-500" />
              <button type="button" onClick={() => setOpen(false)}
                className="ml-auto px-3 py-1 bg-velox-600 text-white rounded-md text-xs font-medium hover:bg-velox-700 transition-colors">
                Done
              </button>
            </div>
          )}
        </div>,
        document.body
      )}
    </div>
  )
}
