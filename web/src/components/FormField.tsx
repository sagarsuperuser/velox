import { forwardRef } from 'react'

interface FormFieldProps extends React.InputHTMLAttributes<HTMLInputElement> {
  label: string
  error?: string
  mono?: boolean
  hint?: string
}

export const FormField = forwardRef<HTMLInputElement, FormFieldProps>(
  ({ label, error, mono, hint, required, className, ...props }, ref) => {
    return (
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          {label}
          {required && <span className="text-red-500 ml-0.5">*</span>}
        </label>
        <input
          ref={ref}
          required={required}
          className={`w-full px-3 py-2 border rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 transition-colors ${
            error
              ? 'border-red-300 focus:ring-red-500 focus:border-red-300'
              : 'border-gray-200 focus:ring-velox-500 focus:border-velox-500'
          } ${mono ? 'font-mono' : ''} ${className || ''}`}
          {...props}
        />
        {error && (
          <p className="mt-1 text-xs text-red-600">{error}</p>
        )}
        {!error && hint && (
          <p className="mt-1 text-xs text-gray-400">{hint}</p>
        )}
      </div>
    )
  }
)

FormField.displayName = 'FormField'

interface FormSelectProps extends React.SelectHTMLAttributes<HTMLSelectElement> {
  label: string
  error?: string
  options: { value: string; label: string }[]
  placeholder?: string
}

export function FormSelect({ label, error, options, placeholder, required, className, ...props }: FormSelectProps) {
  return (
    <div>
      <label className="block text-sm font-medium text-gray-700 mb-1">
        {label}
        {required && <span className="text-red-500 ml-0.5">*</span>}
      </label>
      <select
        required={required}
        className={`w-full px-3 py-2 border rounded-lg shadow-sm text-sm focus:outline-none focus:ring-2 bg-white transition-colors ${
          error
            ? 'border-red-300 focus:ring-red-500'
            : 'border-gray-200 focus:ring-velox-500'
        } ${className || ''}`}
        {...props}
      >
        {placeholder && <option value="">{placeholder}</option>}
        {options.map(o => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
      {error && (
        <p className="mt-1 text-xs text-red-600">{error}</p>
      )}
    </div>
  )
}
