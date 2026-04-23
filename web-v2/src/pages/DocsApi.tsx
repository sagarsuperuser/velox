import { useEffect, useRef } from 'react'
import { PublicLayout } from '@/components/PublicLayout'

export default function DocsApiPage() {
  const containerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    // Scalar's auto-loader ends with an IIFE that does
    // `document.getElementById("api-reference").parentNode.insertBefore(...)`
    // to create its mount div. If we appended the config script to
    // document.body, that mount div sits below #root and is hidden under
    // the React layout. Appending the config script INSIDE this ref'd
    // container forces Scalar to mount as a child here — visible in-page.
    const config = document.createElement('script')
    config.id = 'api-reference'
    config.setAttribute('data-url', '/openapi.yaml')
    container.appendChild(config)

    const loader = document.createElement('script')
    loader.id = 'scalar-api-reference-loader'
    loader.src = 'https://cdn.jsdelivr.net/npm/@scalar/api-reference'
    loader.async = true
    container.appendChild(loader)

    return () => {
      // Clear everything we (or Scalar) injected into the container, plus
      // any stray scalar elements in case the bundle added them elsewhere.
      while (container.firstChild) container.removeChild(container.firstChild)
      document
        .querySelectorAll('[data-scalar-app], [data-scalar-reference]')
        .forEach((el) => el.remove())
    }
  }, [])

  return (
    <PublicLayout>
      <div ref={containerRef} className="min-h-[calc(100vh-56px)]" />
    </PublicLayout>
  )
}
