# Design-Partner Onboarding Playbook

How we onboard a design partner and what they can expect from us. Audience: partner CTO/founder + the Velox maintainer running the engagement.

For the *internal readiness* checklist (what we ship before invites go out) see [`design-partner-readiness-plan.md`](design-partner-readiness-plan.md). This document is the *partner-facing* lifecycle.

---

## Who is a design partner?

A design partner is one of our **first three production tenants**. They run real money through Velox in exchange for direct access to the maintainer, a shaped roadmap, and 12 months free.

In return we ask:

- A weekly 30-minute check-in for the first 8 weeks
- Permission to publish a co-branded case study after 90 days of production use (drafts subject to their approval)
- Honest signal — feature gaps, surprises, papercuts, things that scared them
- A named technical contact on their side (one engineer, not a rotating queue)

This is not a free trial. It's a working relationship with deliverables on both sides.

---

## Partner profile we look for

We say no to partners we can't serve well. The shape that works:

- **Stage:** post-revenue (>$50K MRR), pre-Series-B
- **Pricing model:** has at least one usage-based dimension (per-token, per-call, per-GB) — the wedge is sharper than flat-fee
- **Current billing pain:** running on Stripe Billing + spreadsheets, or homegrown, or Lago/Metronome and unhappy with one specific gap
- **Engineering bandwidth:** can dedicate 20% of one engineer's time during cutover week
- **Risk tolerance:** comfortable being early-adopter; understands that "third logo" means we will sometimes break

If a prospect doesn't match this shape, we still talk to them — but as a future customer, not a design partner.

---

## Lifecycle

### Day 0 — Letter of intent

LOI is one page, not a contract. Names the parties, the 12-month free term, the case-study clause, and the cancellation terms (either side, 30 days notice, no penalty). Counter-signed PDF, kept in `private/design-partners/<tenant>/loi.pdf` (private repo).

Tenant ID is provisioned in the partner's preferred region. They receive credentials + a shared Slack Connect channel + this document.

### Days 1–7 — Discovery + migration plan

We map their *current* billing surface end-to-end before we touch Velox:

- Plan / price catalog (count of products, prices, currencies)
- Customer base (count, billing-cycle distribution, top-10 by MRR)
- Subscription state machine peculiarities (paused, free, trialing, custom)
- Coupons + credits in flight (must arrive in Velox without rounding losses)
- Webhook consumers (their internal services, partner integrations, accounting export)
- Tax registration footprint (single-jurisdiction, multi-jurisdiction, OIDAR)

Deliverable: a `migration-plan-<tenant>.md` checked into `private/design-partners/<tenant>/`. It's an itemized list of *exactly* what moves, what stays, and what gets sunset.

The partner reviews. We don't proceed without their named owner signing off.

### Weeks 2–4 — Sandbox cutover

The partner runs a fork of their integration against Velox in test mode while production stays on the existing system.

- Recipe instantiation or hand-built plan ladder (whichever fits — see [`/docs/recipes`](https://github.com/sagarsuperuser/velox/blob/main/docs/design-recipes.md))
- Stripe test-mode payment connector (their Stripe account, not ours)
- Webhook consumers re-pointed at Velox in test mode
- A subset of customers (5–10) replicated for a full billing-cycle dry run
- Reconciliation script: their numbers vs Velox numbers, line-by-line, both ways must agree

We surface any divergence in the weekly check-in. We do not move to production until at least one full billing cycle has passed in sandbox with zero unreconciled deltas.

### Cutover week (Week 5 ish)

Daily 15-minute standup. The partner names their cutover window. The maintainer is on-call for it.

- Switch payment connector to live mode
- Migrate active subscriptions in batch (script lives in `cmd/velox-import` for Stripe-origin tenants, hand-rolled for others)
- Keep the prior system in **read-only mode** for at least 90 days — we never burn the bridge
- First live invoice is generated, finalized, paid, reconciled — together, in the same call
- Incident runbook is rehearsed: what does "roll back to Stripe Billing" mean operationally, who has the runbook, where is it

If anything looks off in the first 48 hours, we pause new charges and reconcile before resuming. Never the reverse.

### Weeks 6–12 — Steady state

Weekly check-in continues for 8 weeks total. Agenda:

1. Anything that surprised the partner this week (10 min)
2. Reconciliation report — Velox totals vs partner's accounting (5 min)
3. Open issues + ETA (10 min)
4. Roadmap — what Velox is shipping next, what *they* would change about it (5 min)

After Week 8 the cadence drops to monthly unless either side wants more.

### Day 90 — Case-study draft

We send a draft case study (1 page, public-facing, screenshots cleared with their team). They edit. We publish at `/customers/<partner>` on velox.dev with a link from the homepage. This is the deliverable that closes the design-partner phase.

After this point the partner stays at zero cost for the remainder of the 12 months. They are no longer "design partner" — they are a customer with a discount and a direct line to the maintainer.

---

## What the partner gets

- Tenant provisioned in the region of their choice (currently US-East / EU-West)
- 12 months at $0 — no usage caps, no feature gating
- Direct line to the maintainer via Slack Connect (response < 4 business hours, < 1 business hour for production incidents)
- Roadmap influence — features they actually need are prioritized over features we *imagine* they need
- One co-branded case study published with their approval

## What the partner gives

- Named technical contact + named business contact
- Weekly 30-minute check-in (first 8 weeks)
- Honest, specific feedback — "the dunning logic surprised us when …" beats "looks good"
- Permission for the case-study clause
- Reasonable cooperation on the migration timeline (they don't ghost us mid-cutover)

## Mutual exits

Either side can exit with 30 days notice. We help export everything: customers, subscriptions, invoices, credit notes, audit log — in a format their next system can ingest. There is no penalty and no hard feelings.

We have already shipped GDPR data export (`POST /v1/customers/{id}/export`) — the same primitives back the operator-side full-tenant export documented in [`docs/ops/`](ops/).

---

## Communication channels

| Need | Channel | Response SLA |
|---|---|---|
| Production incident | Slack Connect (with `@here` ping) + `incident@velox.dev` | < 1 business hour |
| Bug / unexpected behavior | Slack Connect | < 4 business hours |
| Feature request | Slack Connect or GitHub issue | Triaged weekly |
| Security concern | `security@velox.dev` (encrypted optional) | See [`SECURITY.md`](../SECURITY.md) |
| Conduct concern | `conduct@velox.dev` | See [`CODE_OF_CONDUCT.md`](../CODE_OF_CONDUCT.md) |

Office hours: 9am–6pm IST Monday–Friday. Outside those, production incidents are answered best-effort.

---

## Incident response expectations

If Velox loses or mis-bills money for a partner, the maintainer will:

1. **Acknowledge** in Slack within the SLA above
2. **Triage** — root cause vs. workaround, blast radius (one customer? one tenant? all tenants?)
3. **Communicate** every 30 minutes during an active incident
4. **Restore** — workaround if root cause needs hours, fix if minutes
5. **Document** — within 5 business days, a written postmortem (timeline, cause, fix, prevention) shared with the partner; published in redacted form to `/incidents` on velox.dev if other tenants are exposed to the same class of issue

The audit log is immutable and queryable for the full retention window (default 18 months — see [`docs/ops/audit-log-retention.md`](ops/audit-log-retention.md)). Partners can pull their own forensic trail without waiting on us.

---

## Roadmap influence

A design partner can flag a missing feature in any check-in. The maintainer triages within 5 business days into one of:

- **Yes, shipping in the next 4 weeks** — gets a tracked PR
- **Yes, shipping later** — added to [`docs/90-day-plan.md`](90-day-plan.md) with the partner's name attached
- **No, here's why** — explained in writing; a "no" never sits silent

Features we have built mostly because a partner asked: documented in the changelog with a "thanks to <partner>" attribution where they consented.

---

## When this playbook breaks

If the partner relationship goes sideways — missed cutover dates, silent withdrawal, scope creep that distracts from the wedge — we have a private retro between the maintainer and the partner contact. We name what went wrong on each side, decide whether to continue, and update this playbook so the next partner doesn't hit the same edge.

This document is versioned in git. Material changes get a CHANGELOG.md entry. Partners are notified when terms that affect them change.
