import { createContext, useContext, useState, useCallback } from 'react'
import { cn } from '@/lib/cn'
import { CheckCircle, XCircle, X } from 'lucide-react'

interface Toast {
  id: number
  type: 'success' | 'error'
  message: string
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

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const addToast = useCallback((type: 'success' | 'error', message: string) => {
    const id = nextId++
    setToasts(prev => [...prev, { id, type, message }])
    setTimeout(() => {
      setToasts(prev => prev.filter(t => t.id !== id))
    }, 4000)
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
      <div className="fixed bottom-4 right-4 z-50 space-y-2">
        {toasts.map(toast => (
          <div
            key={toast.id}
            className={cn(
              'flex items-center gap-3 px-4 py-3 rounded-lg shadow-lg text-sm min-w-72 animate-slide-in',
              toast.type === 'success' ? 'bg-emerald-600 text-white' : 'bg-red-600 text-white'
            )}
          >
            {toast.type === 'success' ? <CheckCircle size={18} /> : <XCircle size={18} />}
            <span className="flex-1">{toast.message}</span>
            <button onClick={() => remove(toast.id)} className="text-white/70 hover:text-white">
              <X size={16} />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}
