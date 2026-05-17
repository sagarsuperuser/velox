# ADR-032: Public cost-dashboard projection — token shape + sanitization contract

**Status:** Accepted
**Date:** 2026-05-14
**Related**: ADR-021 (hosted-invoice public token shape), ADR-031 (per-plan bill_timing)

## Context

The cost-dashboard was half-built: the operator-facing surface (`GET /v1/customers/{id}/usage` + `<CostDashboard>` React component) shipped earlier; the **public** surface for partners to embed in their own apps did not. The token column + Customer model field landed in migration 0064 but no public route, rotation endpoint, or sanitized projection existed.

This is wedge-relevant work — cost visibility *is* the AI-native pitch — and demo-blocking. MANUAL_TEST FLOW CU8 documented the expected shape; the code lagged.

## Decision

Ship a single sanitized JSON endpoint for the public projection. Defer the embeddable React widget until a real design partner asks for it (the JSON is consumer-ready; partners can render their own UI today).

### Token

- Format: `vlx_pcd_` + 64 hex (32 bytes entropy). Same shape as the hosted-invoice public token (ADR-021).
- Persisted plaintext in `customers.cost_dashboard_token` with a partial UNIQUE index.
- Rotation invalidates the previous token IMMEDIATELY — no grace window. Read-only surface; the rotation intent is "stop the previous URL right now."
- Audit log entry on rotation (`action=rotate`, `metadata.surface=cost_dashboard_token`). Plaintext token NEVER in the audit row.

### Lookup

- Token lookup uses RLS-bypass (`TxBypass`). The token IS the credential — no tenant context yet, and the returned customer's `tenant_id` is what scopes everything downstream.
- 401 envelope identical for invalid / never-existed / rotated tokens (anti-enumeration). Same shape Velox uses for revoked API keys.
- Prefix-mismatch fast-path: any token not starting with `vlx_pcd_` → 401 without DB lookup.

### Sanitization contract

The projection is built by composing the existing `CustomerUsageService.Get` (same math the operator dashboard uses → dashboard math == public math) then stripping PII at the assembler boundary.

| Field | Public? | Why |
|---|---|---|
| customer_id, tenant_id | yes | Caller has the token; these are not secrets. |
| billing_period { start, end, source } | yes | Cycle-bound context. `source` is `"subscription"` or `"no_subscription"`. |
| subscriptions[].{id, plan_name, currency, period} | yes | What's running and when the cycle closes. |
| usage[].{meter_key, meter_name, unit, currency, totals, rules[]} | yes | Cost attribution — the whole point. |
| totals[], projected_total_cents | yes | Headline number partners surface to their customer. |
| **email** | NO | PII |
| **display_name** | NO | PII |
| **external_id** | NO | Caller chose this id privately |
| **metadata** | NO | Caller-controlled — could carry anything |
| **billing_profile** | NO | Legal name, address, tax_id |
| **warnings** | NO | Operator-facing tech messages |
| **plan_id**, **rating_rule_version_id** | NO | Internal IDs, not useful to the partner |

The mapping happens in `internal/usage/cost_dashboard.go::CostDashboardAssembler.GetByToken`. Adding a new field to the operator-facing `CustomerUsageResult` does NOT automatically leak it to the public surface — the assembler walks an explicit allowlist of fields. Future PII fields stay private by default.

### Empty state

No active subscription → 200 with `billing_period.source = "no_subscription"` and empty arrays. Not a 404, not a 5xx — the partner widget renders a clean empty state rather than handling an error code.

### Rate limit

Mounted under the existing `hostedInvoiceRL` 60/min/IP bucket. Tighter than the general 100/min for the same reason hosted invoice tightened: payment-adjacent surfaces are higher-value targets, and the widget may poll.

## Consequences

**Positive:**
- Velox can now demo the AI-native cost-visibility pitch end-to-end. Partner embeds the JSON into their own app; rotation gives the operator a kill switch.
- Sanitization is explicit (allowlist, not denylist) — future operator-facing additions don't leak by accident.
- Same token + rotation shape as hosted invoice (ADR-021) — one mental model for operators.

**Negative:**
- No widget. Partners build their own renderer or wait for the React component to ship. Tradeoff: shipping the JSON now unblocks the demo; the widget is uncertain UX work that benefits from real DP feedback before locking in.
- Token plaintext in the DB. Same tradeoff hosted invoice made; 256-bit entropy makes brute-force infeasible and the read-only surface limits blast radius if the column leaks.

## Industry reference

- **Stripe**: `hosted_invoice_url` is the canonical pattern — long-lived token-in-URL for a single invoice. No equivalent for cost dashboard (Stripe doesn't ship one).
- **Lago / Orb / Metronome**: none ship a partner-embeddable cost dashboard out of the box. Velox is shipping ahead of the peer set here — the wedge bet is that AI infra customers specifically want this.
- The closest analogue is **Datadog / Snowflake usage portals**, which both use token-bound iframe URLs with sanitized projections — the shape Velox is matching.
