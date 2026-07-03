# Design RFC — Prepaid commits + drawdown

**Status:** PROPOSAL (not built). Grounded in verified-Orb research + a
full repo census (2026-07-03). **Cross-platform validation incomplete** —
the Metronome/Lago/Stripe/AI-infra-practice verification agents were
knocked out by a usage-credit ceiling mid-research, so the disagreement
axes below (netting, expiry defaults, ordering) rest on Orb + first
principles, not a peer sweep. Re-run that validation before locking.

## Why (wedge fit)

Commits + drawdown is a **core wedge pillar**, not table stakes
([[project_positioning_wedge]] build-list item 6). The first DP profile —
AI infra, Series A–B, hybrid pricing — sells **commit + usage**, not
per-seat plans; the fixed slice is a *prepaid commit (drawdown)* or flat
platform fee, and "commit/drawdown is its OWN primitive (credit-ledger,
not proration)". Orb/Metronome/Lago all **lead** with usage metering +
prepaid credits/commits; proration is their secondary layer. Velox
already has the metering and the ledger block model — the commit
primitive is the missing headline.

## The good news: Velox already has ~70% of this

The census found the credit ledger is already an **Orb-style credit-block
model** (`consumed_cents` per block, FIFO drain, per-customer advisory
locks, structural idempotency via partial unique indexes, RLS+livemode,
ADR-071 atomic expiry). Mapping the verified Orb lifecycle onto Velox:

| Orb lifecycle stage | Velox today | Gap |
|---|---|---|
| **Purchase → gross invoice** (qty × cost basis; a real invoice, never a silent top-up) | Manual one-off invoices exist (`subscription_id` nullable, `billing_reason='manual'`, `invoice/handler.go`) | No commit *line* semantics; no link from the paid invoice to the funded grant |
| **Fund** (single API charges + increments balance; optional `require_successful_payment` → `pending_payment` block) | `MarkPaid` choke points identified (`settlement.go:99`, `reconciler.go:299`, `invoice/service.go:958`) | No fund-on-payment hook; no pending/active block state |
| **Drawdown** (auto-consume as usage ingested; deterministic order) | `ApplyToInvoiceAtomic` + `drainPositiveBlocks` FIFO, ordered `expires_at NULLS LAST, created_at, id`; runs apply-before-charge in the billing tx | Ordering lacks Orb's zero-cost-basis-first (promo drained before paid — revenue-preserving) |
| **Exhaust → overage** (usage not blocked; billed on end-of-period invoice; credits apply only to in-arrears charges) | Exactly how Velox works — credits are an apply-before-charge header reduction (`credits_applied_cents`), overage is just the un-covered usage on the cycle invoice | None — already correct |
| **Expiry** (end-of-cadence / fixed / never) | ADR-071 `ExpireGrantAtomic` via `expires_at` | None — already correct |
| **Alert** (balance-VALUE thresholds; webhooks, not %) | — the billing-alerts subsystem was **deleted** in the wedge trim (`0083`) | **Green field** — see below |
| **Auto-recharge** (balance-threshold top-up, fund-before-burn) | Auto-charge machinery exists (dunning/stored-PM) | No balance-triggered top-up sweep |

## Proposed design

### The discriminator (load-bearing)

The census confirms **nothing today distinguishes a purchased commit from
a promotional grant** — `entry_type` is a closed CHECK
(`grant/usage/expiry/adjustment`) and no field links a grant to its
funding invoice. This is needed for two independent reasons: (1) revenue
recognition — a *paid* commit and a *free* promo credit must drain in the
right order and report differently; (2) fund-on-payment idempotency — one
grant per paid invoice.

**Recommendation: reuse `entry_type='grant'` + a new nullable
`grant_kind` column** (`'commit' | 'promotional' | NULL`), plus
`source_invoice_id` with a partial-unique index (mirroring the
`source_credit_note_id` / `source_invoice_reversal_id` pattern in
0093/0106 — this is the established idempotency shape). Rejected
alternative: a new `entry_type='commit'` value — it forces a CHECK
expansion migration ([[feedback_enum_check_constraint_audit]]) and every
`entry_type`-switching reader has to learn the new value, for no gain over
a `grant_kind` tag on the existing type. The balance/drain math is
identical either way; only reporting and ordering read the tag.

### Phase 1 — the minimal-but-complete commit (build target)

1. **Fund a commit invoice.** A commit line on a manual (or
   subscription-attached) invoice; on `MarkPaid`, a hook grants a credit
   block stamped `grant_kind='commit'`, `source_invoice_id=<that
   invoice>`, `expires_at` per the commit's cadence. Idempotent via the
   partial-unique index (fund-once-per-paid-invoice), attached at the
   **existing** `MarkPaid` single-writer tx — not a new writer
   ([[feedback_first_good_practice_default]]: atomic at the choke point,
   not a reconciler).
   - **Payment gate (opt-in, matches Orb `require_successful_payment`):**
     block is `pending_payment` (not drawable) until the funding invoice
     pays; default is drawable-on-grant. "Fund-before-burn" is the safe
     default for auto-recharge (phase 2), opt-in for manual commits.
2. **Drawdown** — already works; add **zero-cost-basis-first** to the
   drain order so promo credits burn before paid commits (revenue
   preservation, verified as Orb's rule). One `ORDER BY` clause change +
   the `grant_kind` read.
3. **Balance-threshold alerts (green field).** Verified Orb model:
   fire on balance **value** crossings, not 80/100% of a commit —
   `credit.balance_low` (crossed below an operator-set threshold),
   `credit.balance_depleted` (>0 → 0), `credit.balance_recovered`
   (0 → >0). New outbound webhook events (the registry has none for
   credit today) + dashboard surface. A dedicated sweep under a new
   `LockKey*` on the leader-elected scheduler loop, evaluated in the same
   tx as the balance change where possible (per-cause events, not
   aggregate — [[feedback_webhook_events_per_cause]]). **Clean up the
   stale `VELOX_BILLING_ALERTS_INTERVAL`** phantom left in the compose
   files (shipped in P7; nothing reads it).
4. **Overage / expiry** — no work; already correct.

### Phase 2 — deferred until a DP names the pressure

- **Auto-recharge / auto-top-up** — balance-threshold-triggered commit
  purchase + auto-charge on the stored PM, fund-before-burn (pending
  top-up pauses further top-ups). Reuses the auto-charge machinery; it's
  a new sweep + a config surface, sized like its own batch.
- **Recipe "commit + overage" template** — recipes can't carry credits
  today (`domain.Recipe` has no grant/commit field); adding it lets
  `anthropic_style` et al. ship a prepaid tier. Small, but only worth it
  once phase 1 is real.
- **Richer drain ordering** (license allocations, item-filtered blocks) —
  Orb's full 5-level order; Velox's soonest-expiring→FIFO + zero-cost
  basis is the load-bearing subset. Add levels when a DP needs
  per-item-scoped commits.

## Decisions to validate before building (open — need credits/DP)

1. **Netting vs gross line items.** Orb (verified) is **gross**: the
   purchase is a real invoice; drawdown is a header reduction on usage
   invoices — which is exactly Velox's shape. *Does any peer net?*
   Unverified (Metronome/Lago agents died). First-principles + Orb say
   gross; proceed on gross, confirm on re-validation.
2. **Expiry default.** Orb offers end-of-cadence / never / fixed.
   Unverified across peers what the *default* should be for AI infra.
   Propose: **term-aligned** (commit expires at the contract term end),
   configurable — matches "enterprise commit contracts with true-up".
3. **Drain order of promo vs paid** — verified (zero-cost-basis first).
   Safe to build.
4. **True-up at term end** — enterprise commits with minimums need a
   period-close true-up (bill the shortfall if usage < commit). Not in
   phase 1; flag as the first thing a real enterprise DP will ask for.

## Load-bearing constraints (from census — do not violate)

- Ledger block math: balance = `SUM(amount_cents)`; block-spendable =
  `amount_cents − consumed_cents`; the two stay in lockstep. Any new
  negative entry must attribute to positive blocks via the FIFO drain.
- Per-customer write serialization: `pg_advisory_xact_lock(tenant:customer)`
  on append; `FOR UPDATE` on apply/adjust; expiry holds both. A
  fund-on-payment path must take the same locks or it races drain/expiry.
- RLS + `livemode` + clock stamping on every ledger mutation
  (`set_livemode` trigger; `clock.Resolver` binding).
- Idempotency is **structural** (partial unique index), never
  check-then-act. Fund-once needs its own index.
- Commit *purchase* is a real billable line (`add_on`/`base_fee`) on its
  funding invoice; the *drawdown* stays a header reduction. Never a
  `credit` line type (the allow-list is closed and that's correct).

## Research provenance

- **Verified (3-0 adversarial):** Orb purchase-is-gross-invoice, drawdown
  deterministic order, exhaust→overage-not-blocked, credits-only-on-
  in-arrears, configurable expiry, first-class auto-top-up. Sources:
  `docs.withorb.com/product-catalog/prepurchase`,
  `/api-reference/credit/create-top-up`, `/tutorials/first-custom-credits`.
- **Unverified (agents hit the credit ceiling):** all Metronome, Lago,
  Stripe Billing, and OpenAI/Anthropic/Modal/Together/Fireworks/Baseten
  practice claims. Treat the cross-platform sections above as hypotheses.
- Repo census: credit ledger, threshold scan, invoice/MarkPaid,
  cost-dashboard, webhook registry, recipes — file:line map on request.
