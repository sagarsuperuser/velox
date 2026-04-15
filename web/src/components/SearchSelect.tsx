import { useState, useRef, useEffect, useMemo, useCallback } from 'react'
import { Search, ChevronDown, X, Check } from 'lucide-react'

export interface SearchSelectOption {
  value: string
  label: string
  sublabel?: string
}

interface SearchSelectProps {
  value: string
  onChange: (value: string) => void
  options: SearchSelectOption[]
  placeholder?: string
  label?: string
  required?: boolean
  error?: string
  disabled?: boolean
}

export function SearchSelect({
  value,
  onChange,
  options,
  placeholder = 'Select...',
  label,
  required,
  error,
  disabled,
}: SearchSelectProps) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const ref = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  const listboxId = `searchselect-listbox-${label?.toLowerCase().replace(/\s+/g, '-') || 'default'}`
  const selected = options.find(o => o.value === value)

  const filtered = useMemo(() => {
    if (!query) return options
    const q = query.toLowerCase()
    return options.filter(o =>
      o.label.toLowerCase().includes(q) ||
      (o.sublabel && o.sublabel.toLowerCase().includes(q)) ||
      o.value.toLowerCase().includes(q)
    )
  }, [options, query])

  // Reset highlight when filtered list changes
  useEffect(() => {
    setHighlightedIndex(-1)
  }, [filtered])

  // Close on outside click
  useEffect(() => {
    const handleClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false)
        setQuery('')
      }
    }
    if (open) document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [open])

  // Close on Escape
  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { setOpen(false); setQuery('') }
    }
    if (open) document.addEventListener('keydown', handleKey)
    return () => document.removeEventListener('keydown', handleKey)
  }, [open])

  // Focus input when opened
  useEffect(() => {
    if (open && inputRef.current) inputRef.current.focus()
  }, [open])

  // Scroll highlighted option into view
  useEffect(() => {
    if (highlightedIndex >= 0 && listRef.current) {
      const el = listRef.current.querySelector(`[data-index="${highlightedIndex}"]`)
      el?.scrollIntoView({ block: 'nearest' })
    }
  }, [highlightedIndex])

  const handleSelect = useCallback((val: string) => {
    onChange(val)
    setOpen(false)
    setQuery('')
  }, [onChange])

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (!open) return

    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlightedIndex(prev =>
          prev < filtered.length - 1 ? prev + 1 : 0
        )
        break
      case 'ArrowUp':
        e.preventDefault()
        setHighlightedIndex(prev =>
          prev > 0 ? prev - 1 : filtered.length - 1
        )
        break
      case 'Enter':
        e.preventDefault()
        if (highlightedIndex >= 0 && highlightedIndex < filtered.length) {
          handleSelect(filtered[highlightedIndex].value)
        }
        break
    }
  }, [open, filtered, highlightedIndex, handleSelect])

  const activeDescendant = highlightedIndex >= 0
    ? `${listboxId}-option-${highlightedIndex}`
    : undefined

  return (
    <div ref={ref} className="relative">
      {label && (
        <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
          {label}{required && <span className="text-red-500 ml-0.5">*</span>}
        </label>
      )}

      {/* Trigger button */}
      <button
        type="button"
        role="combobox"
        aria-expanded={open}
        aria-haspopup="listbox"
        aria-controls={open ? listboxId : undefined}
        aria-label={label || placeholder}
        aria-activedescendant={open ? activeDescendant : undefined}
        disabled={disabled}
        onClick={() => setOpen(!open)}
        onKeyDown={handleKeyDown}
        className={`w-full flex items-center gap-2 px-3 py-2 border rounded-lg text-sm text-left transition-colors bg-white dark:bg-gray-900 dark:border-gray-700 ${
          error ? 'border-red-300 focus:ring-red-500' : 'border-gray-200 focus:ring-velox-500 dark:border-gray-700'
        } focus:outline-none focus:ring-2 focus:border-transparent hover:border-gray-300 dark:hover:border-gray-600 ${
          disabled ? 'opacity-50 cursor-not-allowed' : ''
        }`}
      >
        <span className={`flex-1 truncate ${selected ? 'text-gray-900 dark:text-gray-100' : 'text-gray-400'}`}>
          {selected ? selected.label : placeholder}
        </span>
        {value && !disabled ? (
          <span
            onClick={(e) => { e.stopPropagation(); onChange(''); setQuery('') }}
            className="p-0.5 rounded hover:bg-gray-100 dark:hover:bg-gray-800 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 transition-colors"
          >
            <X size={14} />
          </span>
        ) : (
          <ChevronDown size={14} className="text-gray-400 shrink-0" />
        )}
      </button>

      {error && <p className="text-xs text-red-600 mt-1">{error}</p>}

      {/* Dropdown */}
      {open && (
        <div className="absolute z-50 mt-1 w-full bg-white dark:bg-gray-900 rounded-xl shadow-lg border border-gray-200 dark:border-gray-700 overflow-hidden animate-scale-in">
          {/* Search input */}
          <div className="p-2 border-b border-gray-100 dark:border-gray-800">
            <div className="relative">
              <Search size={14} className="absolute left-2.5 top-2.5 text-gray-400" />
              <input
                ref={inputRef}
                type="text"
                value={query}
                onChange={e => setQuery(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder="Type to search..."
                aria-label="Search options"
                className="w-full pl-8 pr-3 py-2 text-sm border border-gray-200 dark:border-gray-700 rounded-lg focus:outline-none focus:ring-2 focus:ring-velox-500 focus:border-transparent bg-white dark:bg-gray-800 dark:text-gray-100"
              />
            </div>
          </div>

          {/* Options list */}
          <div ref={listRef} role="listbox" id={listboxId} className="max-h-60 overflow-y-auto">
            {filtered.length === 0 ? (
              <p className="px-4 py-6 text-sm text-gray-400 text-center">No results found</p>
            ) : (
              filtered.map((option, index) => (
                <button
                  key={option.value}
                  type="button"
                  role="option"
                  id={`${listboxId}-option-${index}`}
                  data-index={index}
                  aria-selected={option.value === value}
                  onClick={() => handleSelect(option.value)}
                  onMouseEnter={() => setHighlightedIndex(index)}
                  className={`w-full flex items-center gap-3 px-3 py-2.5 text-left transition-colors ${
                    index === highlightedIndex
                      ? 'bg-velox-50'
                      : option.value === value
                        ? 'bg-velox-50/50'
                        : 'hover:bg-gray-50 dark:hover:bg-gray-800'
                  }`}
                >
                  <div className="flex-1 min-w-0">
                    <p className={`text-sm truncate ${option.value === value ? 'font-medium text-velox-700 dark:text-velox-300' : 'text-gray-900 dark:text-gray-100'}`}>
                      {option.label}
                    </p>
                    {option.sublabel && (
                      <p className="text-xs text-gray-500 truncate mt-0.5">{option.sublabel}</p>
                    )}
                  </div>
                  {option.value === value && (
                    <Check size={14} className="text-velox-600 shrink-0" />
                  )}
                </button>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  )
}
