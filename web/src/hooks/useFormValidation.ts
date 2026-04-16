import { useState, useCallback, useRef } from 'react'

// --- Validation rules ---

export type ValidationRule = {
  validate: (value: string) => boolean
  message: string
}

export const rules = {
  required: (label = 'This field'): ValidationRule => ({
    validate: (v) => v.trim().length > 0,
    message: `${label} is required`,
  }),

  email: (): ValidationRule => ({
    validate: (v) => !v || /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(v),
    message: 'Invalid email address',
  }),

  phone: (): ValidationRule => ({
    validate: (v) => !v || /^[\+\d\s\-\(\)]{7,20}$/.test(v),
    message: 'Invalid phone number',
  }),

  url: (): ValidationRule => ({
    validate: (v) => {
      if (!v) return true
      try { const u = new URL(v); return u.protocol === 'https:' || u.protocol === 'http:' } catch { return false }
    },
    message: 'Must be a valid URL',
  }),

  slug: (): ValidationRule => ({
    validate: (v) => !v || /^[a-zA-Z0-9_\-]+$/.test(v),
    message: 'Only letters, numbers, hyphens, and underscores',
  }),

  minAmount: (min: number): ValidationRule => ({
    validate: (v) => !v || parseFloat(v) >= min,
    message: `Must be at least ${min}`,
  }),

  maxAmount: (max: number): ValidationRule => ({
    validate: (v) => !v || parseFloat(v) <= max,
    message: `Must be at most ${max}`,
  }),
}

// --- Hook ---

type FieldRules = Record<string, ValidationRule[]>

export function useFormValidation(fieldRules: FieldRules) {
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [touched, setTouched] = useState<Record<string, boolean>>({})
  const fieldRefs = useRef<Record<string, HTMLElement | null>>({})

  const validateField = useCallback((name: string, value: string): string | null => {
    const rules = fieldRules[name]
    if (!rules) return null
    for (const rule of rules) {
      if (!rule.validate(value)) return rule.message
    }
    return null
  }, [fieldRules])

  const onBlur = useCallback((name: string, value: string) => {
    setTouched(t => ({ ...t, [name]: true }))
    const error = validateField(name, value)
    setErrors(e => {
      if (error) return { ...e, [name]: error }
      const { [name]: _, ...rest } = e
      return rest
    })
  }, [validateField])

  const onChange = useCallback((name: string, value: string) => {
    // Only show errors after the field has been touched (first blur)
    if (!touched[name]) return
    const error = validateField(name, value)
    setErrors(e => {
      if (error) return { ...e, [name]: error }
      const { [name]: _, ...rest } = e
      return rest
    })
  }, [validateField, touched])

  const validateAll = useCallback((values: Record<string, unknown>): boolean => {
    const newErrors: Record<string, string> = {}
    const newTouched: Record<string, boolean> = {}

    for (const name of Object.keys(fieldRules)) {
      newTouched[name] = true
      const error = validateField(name, String(values[name] ?? ''))
      if (error) newErrors[name] = error
    }

    setTouched(newTouched)
    setErrors(newErrors)

    // Focus first error field
    const firstErrorField = Object.keys(fieldRules).find(name => newErrors[name])
    if (firstErrorField && fieldRefs.current[firstErrorField]) {
      fieldRefs.current[firstErrorField]?.focus()
    }

    return Object.keys(newErrors).length === 0
  }, [fieldRules, validateField])

  const fieldError = useCallback((name: string): string | undefined => {
    return touched[name] ? errors[name] : undefined
  }, [errors, touched])

  const registerRef = useCallback((name: string) => (el: HTMLElement | null) => {
    fieldRefs.current[name] = el
  }, [])

  const clearErrors = useCallback(() => {
    setErrors({})
    setTouched({})
  }, [])

  const setServerErrors = useCallback((fieldErrors: Record<string, string>) => {
    const newTouched: Record<string, boolean> = {}
    for (const name of Object.keys(fieldErrors)) {
      newTouched[name] = true
    }
    setErrors(prev => ({ ...prev, ...fieldErrors }))
    setTouched(prev => ({ ...prev, ...newTouched }))

    // Focus first error field
    const firstField = Object.keys(fieldErrors)[0]
    if (firstField && fieldRefs.current[firstField]) {
      fieldRefs.current[firstField]?.focus()
    }
  }, [])

  return { onBlur, onChange, validateAll, fieldError, registerRef, clearErrors, setServerErrors, errors }
}
