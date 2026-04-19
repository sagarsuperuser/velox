import { useCallback, useMemo, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'

// Backs a bag of string-typed fields (filter, sort, page, search) with URL
// query params so list views are shareable, bookmarkable, and restored on
// refresh. Missing params fall back to the provided defaults; values that
// equal the default are stripped from the URL, keeping canonical state clean.
// Unrelated params (e.g. ?ref=email) are preserved.
//
// Setter replaces the current history entry by default so rapid typing in a
// search box or fast filter-click sequences don't pollute the back stack —
// the back button then returns to the previous page, which matches how users
// actually think about "back" on filtered lists.
export function useUrlState<T extends Record<string, string>>(
  defaults: T,
): [T, (partial: Partial<T>) => void] {
  // Capture defaults on first render so an inline-object literal at the call
  // site doesn't destabilise downstream memoisation.
  const defaultsRef = useRef(defaults)
  const [searchParams, setSearchParams] = useSearchParams()

  const state = useMemo(() => {
    const result = { ...defaultsRef.current }
    for (const key of Object.keys(defaultsRef.current)) {
      const val = searchParams.get(key)
      if (val !== null) {
        ;(result as Record<string, string>)[key] = val
      }
    }
    return result
  }, [searchParams])

  const setState = useCallback(
    (partial: Partial<T>) => {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current)
          for (const [key, value] of Object.entries(partial)) {
            if (value === undefined) continue
            const isDefault = value === (defaultsRef.current as Record<string, string>)[key]
            if (isDefault || value === '') {
              next.delete(key)
            } else {
              next.set(key, value)
            }
          }
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )

  return [state, setState]
}
