import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'

export function useKeyboardShortcuts() {
  const navigate = useNavigate()

  useEffect(() => {
    let lastKey = ''
    let lastKeyTime = 0

    const handler = (e: KeyboardEvent) => {
      // Don't trigger in input/textarea/select
      const target = e.target as HTMLElement
      if (['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)) return
      if (target.isContentEditable) return

      const now = Date.now()
      const key = e.key.toLowerCase()

      // Two-key combos (g + letter within 500ms)
      if (lastKey === 'g' && now - lastKeyTime < 500) {
        e.preventDefault()
        switch (key) {
          case 'd': navigate('/'); break
          case 'c': navigate('/customers'); break
          case 'i': navigate('/invoices'); break
          case 's': navigate('/subscriptions'); break
          case 'u': navigate('/usage'); break
          case 'p': navigate('/pricing'); break
          case 'a': navigate('/analytics'); break
          case 'k': navigate('/api-keys'); break
        }
        lastKey = ''
        return
      }

      // Single key shortcuts
      if (key === '?' && !e.metaKey && !e.ctrlKey) {
        e.preventDefault()
        // Toggle help overlay
        document.dispatchEvent(new CustomEvent('velox:toggle-help'))
        return
      }

      lastKey = key
      lastKeyTime = now
    }

    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [navigate])
}
