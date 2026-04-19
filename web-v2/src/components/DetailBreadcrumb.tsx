import { Link } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'

interface DetailBreadcrumbProps {
  to: string
  parentLabel: string
  currentLabel: string
}

export function DetailBreadcrumb({ to, parentLabel, currentLabel }: DetailBreadcrumbProps) {
  return (
    <div className="flex items-center gap-2 text-sm text-muted-foreground mb-4">
      <Link to={to} className="hover:text-foreground transition-colors flex items-center gap-1">
        <ArrowLeft size={14} />
        {parentLabel}
      </Link>
      <span>/</span>
      <span className="text-foreground">{currentLabel}</span>
    </div>
  )
}
