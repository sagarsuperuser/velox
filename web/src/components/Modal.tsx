import { useEffect, useRef } from 'react'
import { X } from 'lucide-react'

interface ModalProps {
  open: boolean
  onClose: () => void
  title: string
  children: React.ReactNode
  wide?: boolean
  dirty?: boolean // When true, warn before closing via Escape or backdrop click
  onSubmit?: () => void // When provided, wraps content in <form> and submits on Enter
}

export function Modal({ open, onClose, title, children, wide, dirty, onSubmit }: ModalProps) {
  const ref = useRef<HTMLDivElement>(null)

  const safeClose = () => {
    if (dirty) {
      if (!window.confirm('You have unsaved changes. Discard them?')) return
    }
    onClose()
  }

  useEffect(() => {
    const handleEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') safeClose()
    }
    if (open) document.addEventListener('keydown', handleEsc)
    return () => document.removeEventListener('keydown', handleEsc)
  }, [open, onClose, dirty])

  // Focus trap: focus first input/select/textarea on open, fall back to any focusable
  useEffect(() => {
    if (open && ref.current) {
      const inputEl = ref.current.querySelector<HTMLElement>(
        'input:not([type="hidden"]), select, textarea'
      )
      if (inputEl) {
        inputEl.focus()
      } else {
        const focusable = ref.current.querySelector<HTMLElement>(
          'button, [href], [tabindex]:not([tabindex="-1"])'
        )
        focusable?.focus()
      }
    }
  }, [open])

  if (!open) return null

  const handleFormSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    onSubmit?.()
  }

  const contentBody = (
    <div className="px-4 py-4 sm:px-6 sm:py-5 overflow-y-auto">
      {children}
    </div>
  )

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/25 backdrop-blur-[2px] animate-fade-in" onClick={safeClose} />
      <div ref={ref} role="dialog" aria-modal="true" aria-labelledby="modal-title" className={`relative bg-white dark:bg-gray-900 rounded-2xl shadow-modal w-full ${wide ? 'max-w-[min(32rem,90vw)]' : 'max-w-[min(28rem,90vw)]'} mx-4 animate-scale-in max-h-[90vh] flex flex-col`}>
        <div className="flex items-center justify-between px-4 py-3 sm:px-6 sm:py-4 border-b border-gray-100 dark:border-gray-800 shrink-0">
          <h2 id="modal-title" className="text-base font-semibold text-gray-900 dark:text-gray-100">{title}</h2>
          <button
            onClick={safeClose}
            aria-label="Close"
            className="w-8 h-8 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 dark:hover:text-gray-300 dark:hover:bg-gray-800 transition-colors"
          >
            <X size={18} />
          </button>
        </div>
        {onSubmit ? (
          <form onSubmit={handleFormSubmit}>
            {contentBody}
          </form>
        ) : (
          contentBody
        )}
      </div>
    </div>
  )
}
