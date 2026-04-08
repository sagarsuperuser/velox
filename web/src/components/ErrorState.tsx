import { AlertTriangle } from 'lucide-react'

interface ErrorStateProps {
  title?: string
  message?: string
  onRetry?: () => void
}

export function ErrorState({ title = 'Something went wrong', message, onRetry }: ErrorStateProps) {
  return (
    <div className="flex flex-col items-center justify-center py-12 px-6">
      <AlertTriangle size={40} className="text-red-400 mb-4" />
      <h3 className="text-sm font-semibold text-gray-900">{title}</h3>
      {message && (
        <p className="text-sm text-gray-500 mt-1 text-center max-w-md">{message}</p>
      )}
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
