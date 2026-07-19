import { useCallback, useMemo, useState } from 'react'
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
  // Freeze defaults at first render (useState initializer — never set
  // again) so an inline-object literal at the call site doesn't
  // destabilise downstream memoisation. A ref held the same value until
  // the 2026-07-19 hooks cleanup: reading ref.current during render is
  // barred (React may render speculatively), and state read during
  // render is the sanctioned equivalent with identical capture-once
  // semantics.
  const [frozenDefaults] = useState(defaults)
  const [searchParams, setSearchParams] = useSearchParams()

  const state = useMemo(() => {
    const result = { ...frozenDefaults }
    for (const key of Object.keys(frozenDefaults)) {
      const val = searchParams.get(key)
      if (val !== null) {
        ;(result as Record<string, string>)[key] = val
      }
    }
    return result
  }, [searchParams, frozenDefaults])

  const setState = useCallback(
    (partial: Partial<T>) => {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current)
          for (const [key, value] of Object.entries(partial)) {
            if (value === undefined) continue
            const isDefault = value === (frozenDefaults as Record<string, string>)[key]
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
    [setSearchParams, frozenDefaults],
  )

  return [state, setState]
}
