# Velox — Cold-email templates

> Pairs with [`outreach-list.md`](outreach-list.md). Each template targets one of the
> three segments in that list (AI inference, vector DBs, dev infra), with founder-to-founder
> and CEO-to-VP-Eng tone variants where the recipient profile differs.

## How to use

1. Pick the template that matches the segment of the prospect (column "segment" in
   the outreach list).
2. Replace `{{first_name}}`, `{{company}}`, `{{specific_observation}}`, `{{their_url}}`,
   and `{{calendar_link}}` with concrete values. **The first line must reference
   something specific to their public posture** — a rate-card cell, a regulatory
   constraint they've blogged about, a Stripe Meter API friction post they've written.
   Generic openers go to spam mentally even if they don't go to spam technically.
3. Trim aggressively. Target 90 words for the body, 8 for the subject.
4. Send from `<your-name>@velox.dev`, never from a generic `outreach@`. Founder-to-founder
   reply rate is 4–6×.
5. Track in the spreadsheet next to `outreach-list.md` — date sent, replied y/n,
   demo booked y/n, reason if no.

**One-touch rule.** Do not send a follow-up before Day 5. After Day 14 with no reply,
mark the row dead and move on — there are 50 names in the list, the funnel works on
volume not on persistence.

---

## Subject lines (pick one per segment, A/B if you want to)

| Segment | Subject (A) | Subject (B) |
|---|---|---|
| AI inference | `Open-source billing for {{company}}'s rate matrix` | `Stripe Billing fees on inference traffic — quick question` |
| Vector DB | `Self-host billing for {{company}} (data-sovereignty wedge)` | `Open-source billing engine — fits your EU footprint` |
| Dev infra | `{{company}} + open-source usage billing` | `An alternative to Stripe Meter API — quick read` |

Subject A leads with the wedge for that segment; B leads with the friction.
Both stay under 60 characters so the iPhone doesn't truncate.

---

## Template 1 — AI inference (founder-to-founder)

**When to use:** rows tagged AI-inference + Tier A in the outreach list. Recipient
is the founder/CEO and the company is < 50 engineers.

```
Subject: Open-source billing for {{company}}'s rate matrix

Hi {{first_name}},

Browsing your pricing page — {{specific_observation, e.g. "the input/output
× cache-hit/miss × tier breakdown across 16 cells for the {{model}} family"}}.
That kind of rate matrix is exactly the surface that breaks down on Stripe
Billing past a certain volume (every cell becomes a separate Meter, every
plan change is a multi-Meter migration).

I'm building Velox — open-source, self-host, AI-native usage billing. PaymentIntent
direct (no 0.5% Stripe Billing fee on top of card processing), multi-dim meters
as a first-class shape, encrypted-at-rest tenant isolation. The wedge is teams
like {{company}} where the rate card is genuinely multi-dimensional and the
Stripe Billing wrapper is the long pole.

Looking for 3 design partners — 12 months free in exchange for a weekly check-in
and a co-branded case study. Worth 20 minutes? {{calendar_link}}

— {{your_name}}
github.com/sagarsuperuser/velox
```

**Why this works:** specific opener proves you read their site (90% of cold email
fails this bar). The wedge is named by their pain, not by Velox's features. The
ask is small (20 min, not "intro call"). Calendar link removes one round-trip.

---

## Template 2 — AI inference (CEO-to-VP-Eng)

**When to use:** AI-inference rows where the contact is VP/staff/principal engineer
rather than the CEO. Tone shifts from "founder hi" to "engineering peer".

```
Subject: Stripe Billing fees on inference traffic — quick question

Hi {{first_name}},

I'm guessing {{company}} pays Stripe Billing on top of card processing fees for
the inference business. At usage-billed volume that's a noticeable line item —
0.5% of GMV is real money once revenue scales.

Building Velox — open-source self-host billing engine, PaymentIntent direct, no
Stripe Billing wrapper. Multi-dim meters, RLS tenant isolation, encrypted creds.
Designed for the rate matrix shape your pricing page already shows.

Three design partners signing this quarter, 12 months free, weekly check-in,
co-branded case study. Engineering-led decision, no procurement loop. Worth
20 minutes to walk you through the architecture? {{calendar_link}}

— {{your_name}}
github.com/sagarsuperuser/velox · docs/positioning.md
```

**Why this works:** VPs of engineering respond to architectural specifics
(PaymentIntent direct, RLS, no procurement loop) more than to founder warmth.
The github + positioning-doc link signals "you can audit this without a call".

---

## Template 3 — Vector DB / regulated infra (founder-to-founder)

**When to use:** vector-DB rows + any infra company where the data-sovereignty
pillar lands hardest (EU GDPR-strict, India RBI, healthcare-adjacent).

```
Subject: Self-host billing for {{company}} (data-sovereignty wedge)

Hi {{first_name}},

{{specific_observation, e.g. "Saw your post on the EU residency story for
{{company}} customers"}}. The billing layer is usually the last thing that
moves into the customer's region — vendors push you to a hosted SaaS billing
provider and the data-residency promise leaks.

Velox is open-source, self-host, runs in the customer's VPC. Encryption at rest
with the operator's key (not ours), RLS-isolated tenants, audit log immutable
for 18 months by default. PaymentIntent direct so no Stripe Billing fee on top.

I'm taking on three design partners — 12 months free in exchange for a weekly
check-in and a co-branded case study. {{company}}'s posture is the canonical
fit. Worth 20 minutes? {{calendar_link}}

— {{your_name}}
github.com/sagarsuperuser/velox · docs/compliance/soc2-mapping.md
```

**Why this works:** leads with their public posture (you've read their blog).
Links to the SOC 2 mapping doc — proves the compliance story is more than
marketing. The "data residency promise leaks" line names the pain
specifically.

---

## Template 4 — Dev infra (founder-to-founder)

**When to use:** dev-infra rows where the recipient has likely tangled with the
Stripe Meter API directly. Most webhooks/APIs/observability companies fall here.

```
Subject: {{company}} + open-source usage billing

Hi {{first_name}},

If you're metering {{specific_dimension, e.g. "events × destination × retry
attempt for {{company}}'s deliveries pricing"}} on Stripe Billing today, you've
probably hit the Meter API's per-event-name limits and the 24-hour aggregation
window. The Stripe team will tell you to "simplify the rate card" — which
isn't an option once customers depend on the granularity.

Building Velox — open-source self-host billing, multi-dim meters as a first-class
shape, sub-minute aggregation, idempotent ingest with 7-day backfill window.
PaymentIntent direct so no Stripe Billing fee. RLS tenant isolation, encrypted
creds, audit log built-in.

Three design partners signing this quarter — 12 months free, weekly check-in,
co-branded case study. {{company}}'s shape fits the wedge cleanly. 20 min?
{{calendar_link}}

— {{your_name}}
github.com/sagarsuperuser/velox
```

**Why this works:** names a specific Stripe Meter API friction the recipient has
almost certainly hit. The "Stripe team will tell you to simplify the rate card"
line is recognizable to anyone who's been on those calls — instant credibility.

---

## Reply playbook

If they reply, do not send another templated email. Your follow-up is human-written
and short:

- **Reply: "tell me more"** → send the [positioning doc](../positioning.md) +
  [SOC 2 mapping doc](../compliance/soc2-mapping.md) as PDFs in a single follow-up.
  Don't include the github link — they already have it. Offer two specific demo
  slots for the same week.
- **Reply: "we're not interested"** → reply with two sentences: thank them, ask
  the single specific reason. Mark the row dead. Don't argue. The reason becomes
  data for the next 50.
- **Reply: "we're interested but timing is wrong"** → ask their realistic re-engage
  date, calendar a reminder for one week before, and stop emailing them in between.
  Re-engaging at the named date is the highest-ROI follow-up shape.
- **No reply, Day 5** → one bump only, two-sentence body referencing the original
  email's specific observation. No "just following up" — that goes to spam in
  the recipient's mind even if it lands in inbox.
- **No reply, Day 14** → mark the row dead, move on, do not bump again.

---

## What deliberately isn't here

- **Mass-merge / sequencer template** — Lemlist / Apollo style multi-touch sequences.
  At 50 leads, hand-sent beats automation; at 500 you can revisit.
- **Generic "we built a thing" template** — every cold email Velox sends references
  the recipient's public posture by sentence two. If you can't find a specific
  observation, do not send.
- **Founder-bio paragraphs** — your signature carries one URL and one positioning-doc
  link. The recipient clicks if they want more about you. Padding the body with
  bio dilutes the wedge.
- **Discount / urgency framing** — "12 months free" is the only "discount" mentioned,
  and it's framed as design-partner consideration, not a price drop. Velox is not
  a price-competitive product against Lago + Stripe Billing on day one; positioning
  on price loses.

---

## Last updated

| Date | Author | Change |
|---|---|---|
| 2026-04-27 | maintainer | Initial draft — four templates (3 segments + a CEO-to-VP-Eng variant) plus subject A/B + reply playbook |
