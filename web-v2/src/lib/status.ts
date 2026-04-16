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
