# ADR-048: Credit clawbacks reverse proportional tax via the credit-note primitive

- Status: Accepted
- Date: 2026-06-06
- Supersedes/relates: ADR-039 (credits = the discount primitive), ADR-042 (integer proration math), the cancel/prebill-relief work (#22)

## Context

When Velox claws back part of an **already-paid, taxed `in_advance`** charge — a mid-cycle **plan downgrade**, a **mid-cycle cancel** of a paid prebill, or a **plan swap** — it credits the customer for the unused time. Today all three paths grant a **bare net (tax-exclusive) amount** to the customer credit ledger and reverse **no** output tax:

- downgrade proration — `subscription/handler.go` (`proratedCents < 0` → `GrantProration`)
- cancel paid-prebill — `engine.go BillOnCancel` (paid path → `creditGranter.Grant`)
- plan swap — `engine.go BillOnPlanSwapImmediate` (→ `creditGranter.Grant`)

The customer **paid gross** (net + tax) for that unused slice. Crediting only the net is a **latent cash-short bug**: a net credit balance offsets a *future gross* invoice 1:1, so the customer ends up short by the tax slice on any taxed invoice — and the tenant's upstream output-tax liability is never reduced, so it over-reports/over-remits relative to what it actually keeps.

Industry (Stripe / Chargebee / Lago) handles this with a **credit note** that reverses proportional tax against the original invoice. Velox already has that primitive (`creditnote.Service`), and the **unpaid**-prebill cancel branch (`relieveUnpaidPrebillOnCancel`) already uses it correctly. The three paths above are the inconsistent ones.

A tempting shortcut — adding a tax column to the credit ledger and grossing up the grant — is **wrong**: the ledger has no tax breakdown and is consumed as a pure cash offset on `amount_due`, with no place to file an authority-side reversal. A gross-on-the-ledger grant would refund the customer's tax as cash with **no** offsetting reversal → the tenant over-remits. Tax reversals belong on the **credit-note** document, anchored to the original invoice's committed `tax_transaction_id`.

## Decision

Route all three clawbacks through the existing tax-reversing **credit-note** primitive (`creditnote.Service.CreateAndIssueAdjustment`), against the **resolved original paid `in_advance` invoice** (already looked up at every site via `FindBaseInvoiceForPeriod`):

1. **Gross up** the net unused amount to the invoice's gross via its own `Total/Subtotal` ratio (`money.RoundHalfToEven(net × Total ÷ Subtotal)`; `1×` for zero-tax) — identical to `relieveUnpaidPrebillOnCancel`.
2. `CreateAndIssueAdjustment(src.ID, grossUnused, reason, desc)`. On a **paid** source invoice, `Issue` credits the full gross to the customer's **balance** (same spendable `CreditGrant` ledger entry as today) **and** reverses the proportional output tax against the original `tax_transaction_id` (no-op for manual/none providers — gated on `tax_transaction_id != ""`). The tax slice is derived from the **original invoice ratio** (`inv.TaxAmountCents × subtotal ÷ inv.TotalAmountCents`) with a last-CN residual true-up, so tax-inclusive pricing and floor() residuals are handled by construction and repeated clawbacks can't over-reverse (bounded by the per-invoice over-credit cap).
3. **Replace** the bare net grant at each site (never add alongside it — that would double-credit). The **unpaid**-prebill branch (`relieveUnpaidPrebillOnCancel`) is left untouched (it reduces `amount_due`, grants no balance, and must not become a balance-crediting CN).
4. Invoice **display**: present the upgrade proration as the two-line credit-unused-old + charge-new shape (the structurally tax-correct view), with plain-English labels.

When the credit-note adjuster is unwired (narrow unit tests only — production always wires it), fall back to the legacy net ledger grant so existing tests pass.

## Why this is correct (verified)

Cash-neutral and tax-correct, and strictly better than today: the customer gets back the **gross** they paid for the unused slice (which offsets a future gross invoice 1:1), and the tenant's authority-side liability is reduced by exactly the reversed tax — two separate ledgers, no double-count (the reversal never touches the credit ledger or `amount_due`).

## Consequences

- A clawback now produces a **credit note** (a tax document) instead of a bare ledger grant. Operator-visible artifact changes; the customer's spendable balance outcome is preserved (and corrected upward by the tax they overpaid).
- Each clawback CN must be anchored to the resolved source invoice id so the over-credit cap + cumulative tax true-up see prior clawbacks.
- Rolled out in phases: (A) engine cancel + plan-swap, (B) downgrade proration (new `CreditNoteIssuer` dependency on the subscription handler + relocate the proration-source dedup onto the CN), (C) the two-line upgrade display. Tracked in #184.
