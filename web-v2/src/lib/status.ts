export function statusBadgeVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' | 'success' | 'info' | 'warning' | 'danger' {
  switch (status) {
    // Green
    case 'active': case 'paid': case 'succeeded': case 'resolved': return 'success'
    // Blue
    case 'finalized': case 'processing': case 'issued': return 'info'
    // Red
    case 'voided': case 'canceled': case 'failed': case 'revoked': return 'danger'
    // Amber
    case 'paused': case 'pending': case 'escalated': return 'warning'
    // Gray
    case 'draft': case 'archived': return 'secondary'
    default: return 'outline'
  }
}

export function statusBorderColor(status: string): string {
  switch (status) {
    case 'active': case 'paid': case 'succeeded': case 'resolved': return 'border-l-emerald-500'
    case 'finalized': case 'processing': case 'issued': return 'border-l-blue-500'
    case 'voided': case 'canceled': case 'failed': case 'revoked': return 'border-l-red-500'
    case 'paused': case 'pending': case 'escalated': return 'border-l-amber-500'
    case 'draft': case 'archived': return 'border-l-gray-300 dark:border-l-gray-600'
    default: return 'border-l-transparent'
  }
}
