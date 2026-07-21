import type { QueryClient } from '@tanstack/react-query'

// Every query family that renders a customer's credit balance or an
// invoice's owed/paid state. A mutation that MOVES money — credit
// grant/adjust, credit-note create/issue/void, dunning resolution,
// proration credit from a plan/quantity change — must invalidate ALL of
// them, not just its own page's list: the balance renders under
// ['customer-balance', id] on CustomerDetail but ['credits', …] on the
// Credits page, and neither polls, so a missed family stays stale for
// 30s+ and reads as "the credit didn't land" — an invitation to issue
// it twice (2026-07-21 staleness audit; every gap found was this class).
// Omitting ids invalidates the whole family — correct and cheap, since
// only mounted observers actually refetch.
export function invalidateMoneySurfaces(
  qc: QueryClient,
  opts: { customerId?: string; invoiceId?: string } = {},
) {
  const { customerId, invoiceId } = opts
  qc.invalidateQueries({ queryKey: customerId ? ['customer-balance', customerId] : ['customer-balance'] })
  qc.invalidateQueries({ queryKey: customerId ? ['credit-grants', customerId] : ['credit-grants'] })
  qc.invalidateQueries({ queryKey: customerId ? ['customer-overview', customerId] : ['customer-overview'] })
  qc.invalidateQueries({ queryKey: ['credits'] })
  qc.invalidateQueries({ queryKey: ['credit-notes'] })
  qc.invalidateQueries({ queryKey: invoiceId ? ['invoice', invoiceId] : ['invoice'] })
  qc.invalidateQueries({ queryKey: invoiceId ? ['invoice-credit-notes', invoiceId] : ['invoice-credit-notes'] })
  qc.invalidateQueries({ queryKey: ['invoices'] })
  qc.invalidateQueries({ queryKey: ['dashboard-recent-invoices'] })
  qc.invalidateQueries({ queryKey: ['dashboard-overview'] })
}
