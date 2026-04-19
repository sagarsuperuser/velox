import { useState } from 'react'
import { Copy, Check } from 'lucide-react'
import { cn } from '@/lib/utils'

interface CopyButtonProps {
  text: string
  size?: number
  className?: string
  title?: string
}

export function CopyButton({ text, size = 12, className, title = 'Copy' }: CopyButtonProps) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      type="button"
      title={title}
      onClick={(e) => {
        e.stopPropagation()
        navigator.clipboard.writeText(text)
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
      className={cn('inline-flex items-center gap-1 text-muted-foreground hover:text-foreground transition-colors', className)}
    >
      {copied ? <Check size={size} /> : <Copy size={size} />}
    </button>
  )
}
