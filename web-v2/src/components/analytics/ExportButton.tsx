import { Download } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { downloadCsv } from '@/lib/csv'

interface ExportButtonProps<T extends Record<string, unknown>> {
  filename: string
  rows: T[]
  columns?: (keyof T)[]
  disabled?: boolean
}

// ExportButton wraps downloadCsv with a consistent icon + label treatment.
// Disabled automatically when rows is empty.
export function ExportButton<T extends Record<string, unknown>>({
  filename, rows, columns, disabled,
}: ExportButtonProps<T>) {
  const isEmpty = rows.length === 0
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={() => downloadCsv(filename, rows, columns as string[] | undefined)}
      disabled={disabled || isEmpty}
      aria-label={`Export ${filename} as CSV`}
    >
      <Download size={14} className="mr-1.5" />
      Export
    </Button>
  )
}
