# Velox demo recording script

> 5-minute screen-recorded walkthrough that paid design-partner candidates see
> after a cold-email reply. Scripted shot-by-shot so the recording is repeatable
> and the same beats survive a rerecord every quarter as the product evolves.
>
> **Audience:** VP Eng / staff engineer at an AI inference, vector DB, or
> dev-infra company who has just clicked through from a cold email or replied
> "tell me more". Already knows roughly what Velox is from the
> [positioning doc](../positioning.md) — they are looking for proof, not a pitch.
>
> **Goal of the recording:** prove the wedge in 5 minutes. Cold-emailable URL,
> safe to send to engineering buyers without a covering call.

## How to use this script

1. **Read the [positioning](../positioning.md) and the four
   [cold-email templates](cold-email-templates.md) first** so the talking points
   in the recording match the prospect's mental model from the email they
   replied to.
2. **Use a clean tenant.** Bootstrap a fresh Velox instance, sign in, instantiate
   the `anthropic_style` recipe — that's the canonical demo path. Do not
   record against a tenant you've been hand-tweaking — extra recipes / random
   coupons in the sidebar are visible distractions.
3. **Record at 1440×900** — large enough that text is readable on a
   half-screen window in the prospect's IDE / Slack, small enough that the
   YouTube embed fits in a Twitter card preview.
4. **No edits.** One take. If you stumble, restart. The friction-free
   one-take feel is part of the proof — every cut is a question of "wait, did
   the demo crash and they spliced it?".
5. **Length target:** 4:30 ± 0:20. Five minutes is the cap because that's
   what a VP Eng will watch on their phone while a meeting starts late.
6. **Captions:** auto-generate via Descript / YouTube; hand-correct the
   technical terms (`PaymentIntent`, `usage_events`, `RLS`, `Helm`,
   `recipe`).
7. **Hosting:** unlisted YouTube. Embed on velox.dev/demo. Link in cold-email
   template signatures once recorded.

## Pre-flight checklist (before you hit record)

- [ ] Local Velox up via `docker compose up -d` and `go run ./cmd/velox`
- [ ] Bootstrap completed; signed in as the seeded owner
- [ ] No leftover customers / subscriptions / invoices from prior runs
- [ ] Browser zoomed to 110% so live numbers are readable
- [ ] One terminal pre-loaded with the `curl` for `POST /v1/usage_events` —
      muscle-memory it once before recording so you don't fumble the JSON
- [ ] Laptop on AC power, notifications silenced, dock auto-hidden
- [ ] One Stripe test-mode tab open in a separate window so the
      "PaymentIntent appears here in the Stripe dashboard" beat is one
      Cmd-Tab away

## Shot list

### 0:00 – 0:25 · Hook

**On screen:** the velox.dev hero scrolled to the wedge sentence.

**Voiceover:**
> "Velox is the open-source billing engine for AI and usage-heavy SaaS. It
> runs in your own VPC. Five minutes — I'll show you the three things
> Stripe Billing structurally cannot do for an AI workload."

**Beat:** the three things land later (multi-dim meters, no Stripe Billing
fee, self-host). Naming them up front gives the viewer a frame so they
don't drift when each beat comes.

### 0:25 – 1:30 · Multi-dimensional meters

**On screen:** Velox dashboard → **Recipes** → click `anthropic_style` →
preview side panel showing meters + pricing rules + plan + dunning policy →
**Instantiate**.

**Voiceover:**
> "First, real AI pricing isn't a single number. It's
> `model × input/output × cached/uncached × tier`. On Stripe Billing this
> is six Meters and brittle subscription wiring. On Velox, dimensions are
> first-class on the meter."
>
> "I'll instantiate the `anthropic_style` recipe. Watch what gets created —
> meters, rating rules, pricing rules, the plan, the dunning policy, and a
> webhook subscription. One transaction. One API call. Atomically rolled
> back if anything fails."

**Beat:** zoom in on the preview panel as it appears so the viewer can
read the meter dimensions: `model`, `operation`, `cached`, `tier`.

### 1:30 – 2:30 · Send a usage event, see the live cost

**On screen:** Customer page → **Add embed dashboard** → copy the public
URL → paste into a second browser tab → terminal at the bottom.

**Voiceover:**
> "Here's a customer page. I'll mint a public cost-dashboard URL — token
> auth, ratelimited, no operator data leaks — and open it in a second tab
> as if I were the customer."
>
> "Now I'll send 10,000 input tokens against `gpt-4o`, cached, in tier 1."

**On screen:** terminal — `curl -X POST .../v1/usage_events -d
'{"meter":"tokens","customer":"...","dimensions":{"model":"gpt-4o","operation":"input","cached":true,"tier":1},"value":10000,"timestamp":"..."}'`

**On screen:** the public dashboard refreshes (or 5-second SSE) — the
"current cycle" total ticks up.

**Voiceover:**
> "Sub-second. Cycle-to-date charges, the projected total for the period,
> per-meter breakdown — all from the public token, no auth handoff."

### 2:30 – 3:30 · The Stripe Billing fee disappears

**On screen:** Velox invoice detail page for the customer's first invoice
→ click **PaymentIntent ID** → flips to the Stripe test dashboard showing
the matching `pi_…` row.

**Voiceover:**
> "Second thing Stripe Billing can't do: not charge you 0.5% on top of card
> processing. Velox uses PaymentIntent directly. Stripe still does the
> card and the tax computation under the hood — there's no Stripe
> Billing API call in the path. The fee disappears."

**Beat:** zoom on the Stripe dashboard's PaymentIntent row, then point at
the Stripe Billing column on the same dashboard — empty for this charge.
That visual contrast is the whole point of the beat.

### 3:30 – 4:30 · Self-host

**On screen:** terminal — `helm upgrade --install velox charts/velox
--namespace velox --create-namespace -f values.yaml` against an empty
kind cluster.

**Voiceover:**
> "Third: it runs in your VPC. Helm chart, Docker Compose for the
> single-VM case, Terraform module for AWS. Customer billing data never
> leaves your infrastructure — that's the whole compliance story for
> EU GDPR-strict, India RBI, healthcare-adjacent buyers."
>
> "From a fresh Kubernetes cluster, install completes in roughly two
> minutes. The full self-host runbook is on the docs site —
> backups, audit log, encryption at rest, the SOC 2 control mapping."

**On screen:** quick cut to the self-host docs sidebar at
`/docs/self-host` — show backup/restore, encryption, SOC 2 mapping
links so the viewer knows the docs exist without reading them on
camera.

### 4:30 – 4:50 · The ask

**On screen:** velox.dev/design-partners.

**Voiceover:**
> "I'm taking three design partners this quarter. 12 months free, weekly
> 30-minute check-in, co-branded case study at Day 90."
>
> "If your pricing is multi-dimensional, your compliance posture is
> sovereign-by-default, or your monthly Stripe Billing line crossed
> $5K — book 20 minutes. Link in the description, link in my email."

### 4:50 – 5:00 · Outro

**On screen:** GitHub repo at github.com/sagarsuperuser/velox.

**Voiceover:**
> "Velox. Open source. Self-host. AI-native. Audit the code while you
> wait for the call."

## What deliberately isn't in the script

- **Architecture deep-dive** — five minutes is for the wedge, not the
  internals. Engineering buyers who want internals click into the GitHub
  repo on their own. The recording's job is to make them click.
- **Feature inventory ("we also support…")** — every additional feature
  named is a feature the viewer was not asking about. The three-beat
  shape (multi-dim, no Stripe Billing fee, self-host) is the wedge from
  `docs/positioning.md`. Stay there.
- **Comparison slides vs Stripe / Lago / Orb** — the recipient is a
  technical buyer. Show, don't tell. The competitive table belongs on
  velox.dev/positioning, not in the demo recording.
- **Founder bio / origin story** — the cold email already established
  who you are. The demo proves you ship.
- **Pricing** — Velox has no cloud SKU yet. Mentioning pricing implies
  one exists. Do not.

## Re-record cadence

Quarterly. Update the recording when:

- The recipe instantiation flow changes shape (different meter count,
  different default plan)
- A pillar lands that changes the three-beat shape (e.g. AI-native
  agentic billing primitives ship in v1.1)
- The Stripe dashboard UI changes enough that the "PaymentIntent here,
  Stripe Billing column empty" visual contrast no longer reads
- A buyer cites the recording in an LOI conversation and we want to
  add a beat that turned out to land

## Last updated

| Date | Author | Change |
|---|---|---|
| 2026-04-27 | maintainer | Initial script — five-beat shape, 4:30 target, paired with cold-email templates |
