import { useEffect, useState } from 'react'

// useDebouncedValue returns `value` after it has been stable for
// `delayMs`. Backs server-side list search: the input stays
// keystroke-responsive (URL state updates immediately) while the
// network query keys off the debounced value, so the API sees one
// request per pause instead of one per keypress.
export function useDebouncedValue<T>(value: T, delayMs = 300): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(t)
  }, [value, delayMs])
  return debounced
}
