import { createContext, useContext, useState, useCallback, useEffect, useRef } from 'react'
import { CheckCircle, XCircle, X } from 'lucide-react'

const DISMISS_MS = 5000

interface Toast {
  id: number
  type: 'success' | 'error'
  message: string
  createdAt: number
}

interface ToastContextType {
  success: (message: string) => void
  error: (message: string) => void
}

const ToastContext = createContext<ToastContextType>({
  success: () => {},
  error: () => {},
})

export function useToast() {
  return useContext(ToastContext)
}

let nextId = 0

function ToastItem({ toast, onRemove }: { toast: Toast; onRemove: (id: number) => void }) {
  const [progress, setProgress] = useState(100)
  const rafRef = useRef<number>(0)

  useEffect(() => {
    const start = toast.createdAt
    const tick = () => {
      const elapsed = Date.now() - start
      const remaining = Math.max(0, 1 - elapsed / DISMISS_MS)
      setProgress(remaining * 100)
      if (remaining > 0) {
        rafRef.current = requestAnimationFrame(tick)
      }
    }
    rafRef.current = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(rafRef.current)
  }, [toast.createdAt])

  const borderColor = toast.type === 'success'
    ? 'border-l-emerald-500'
    : 'border-l-red-500'

  const progressColor = toast.type === 'success'
    ? 'bg-emerald-500'
    : 'bg-red-500'

  return (
    <div
      role="alert"
      className={`flex items-center gap-3 pl-4 pr-3 py-3 rounded-lg text-sm animate-slide-in border-l-[3px] ${borderColor} bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100 border border-gray-100 dark:border-gray-700 shadow-toast relative overflow-hidden`}
    >
      {toast.type === 'success'
        ? <CheckCircle size={18} className="text-emerald-500 flex-shrink-0" />
        : <XCircle size={18} className="text-red-500 flex-shrink-0" />
      }
      <span className="flex-1">{toast.message}</span>
      <button onClick={() => onRemove(toast.id)} className="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 flex-shrink-0">
        <X size={16} />
      </button>
      {/* Progress bar */}
      <div className="absolute bottom-0 left-0 right-0 h-[2px] bg-gray-100 dark:bg-gray-700">
        <div
          className={`h-full ${progressColor} transition-none`}
          style={{ width: `${progress}%` }}
        />
      </div>
    </div>
  )
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const addToast = useCallback((type: 'success' | 'error', message: string) => {
    const id = nextId++
    setToasts(prev => [...prev, { id, type, message, createdAt: Date.now() }])
    setTimeout(() => {
      setToasts(prev => prev.filter(t => t.id !== id))
    }, DISMISS_MS)
  }, [])

  const remove = useCallback((id: number) => {
    setToasts(prev => prev.filter(t => t.id !== id))
  }, [])

  return (
    <ToastContext.Provider value={{
      success: (msg) => addToast('success', msg),
      error: (msg) => addToast('error', msg),
    }}>
      {children}
      <div className="fixed bottom-4 right-4 z-50 space-y-2 max-w-sm">
        {toasts.map(toast => (
          <ToastItem key={toast.id} toast={toast} onRemove={remove} />
        ))}
      </div>
    </ToastContext.Provider>
  )
}
