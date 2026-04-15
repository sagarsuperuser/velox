import { AlertTriangle } from 'lucide-react'

interface ErrorStateProps {
  title?: string
  message?: string
  onRetry?: () => void
}

export function ErrorState({ title = 'Failed to load data', message, onRetry }: ErrorStateProps) {
  return (
    <div role="status" className="flex flex-col items-center justify-center py-12 px-6">
      <div className="w-12 h-12 rounded-full bg-red-50 flex items-center justify-center mb-4">
        <AlertTriangle size={22} className="text-red-400" />
      </div>
      <h3 className="text-sm font-semibold text-gray-900 dark:text-gray-100">{title}</h3>
      {message && (
        <p className="text-sm text-gray-600 dark:text-gray-400 mt-1 text-center max-w-md">{message}</p>
      )}
      <p className="text-xs text-gray-500 dark:text-gray-500 mt-2">Check your connection and try again</p>
      {onRetry && (
        <button
          onClick={onRetry}
          className="mt-4 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors"
        >
          Try again
        </button>
      )}
    </div>
  )
}
