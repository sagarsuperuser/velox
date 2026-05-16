# Velox Manual Test Runbook

Practical runbook for exercising Velox end-to-end. Three tiers — pick the
subset that matches the change you made.

| Tier | When | Time |
|------|------|------|
| **1 — Smoke** | Pre-merge / nightly | ~15 min |
| **2 — Full** | Pre-release | ~2 hrs |
| **3 — Deep** | Major releases, infra changes, post-mortems | ~3 hrs |

Each flow has a stable ID (A1, B2, …) for cross-referencing. Steps use
`- [ ]`; copy a section into a scratch doc when running it. This file is
the canonical source, not a progress tracker.

---

## Setup

### Prerequisites

- Go 1.25+, Docker & Compose, Node.js 22+, [Stripe CLI](https://stripe.com/docs/stripe-cli)
- A Stripe test account (keys go in the dashboard per-tenant, not env vars)

### First-time config

```bash
cp .env.example .env
# Required for local dev:
#   VELOX_BOOTSTRAP_TOKEN=<openssl rand -hex 32>
#   VELOX_ENCRYPTION_KEY=<openssl rand -hex 32>   (64 hex chars)
```

### Start the stack (4 terminals)

```bash
make up                      # 1: Postgres + Redis + Mailpit
make dev                     # 2: API on :8080 (auto-migrates)
cd web-v2 && npm run dev     # 3: Dashboard on :5173
stripe listen --forward-to localhost:8080/v1/webhooks/stripe/<vlx_spc_id>   # 4: Stripe webhooks (after Settings → Payments → Connect)
```

`make bootstrap` (first run only) prints operator email + password and the
test/live API keys. Mailpit web UI at http://localhost:8025 captures all
outbound mail.

### Shell setup

```bash
export API=http://localhost:8080
export KEY=vlx_secret_test_…
```

JSON bodies referencing shell vars must use double-quoted JSON with
backslash-escaped quotes: `-d "{\"api_key\":\"$KEY\"}"`. Single-quoted
JSON works when no shell var is referenced.

### Test cards

| Card | Behavior |
|------|----------|
| `4242 4242 4242 4242` | Always succeeds |
| `4000 0000 0000 0341` | Attaches OK, declines on charge (dunning trigger) |
| `4000 0000 0000 9995` | Always declines |

---

# Tier 1 — Smoke (~15 min)

## FLOW S1: End-to-end smoke

Brings the stack up, runs the full money path, signs out. Pre-merge canary.

### S1.1 Stack health
- [ ] `make up` — containers start clean.
- [ ] `make dev` — logs show `migrations: applied N` + `using app database connection (RLS enforced)`.
- [ ] `curl localhost:8080/health` → `{"status":"ok"}`.
- [ ] `curl localhost:8080/health/ready` → 200 with `database`, `scheduler` ok.
- [ ] Frontend at http://localhost:5173 loads.

### S1.2 Bootstrap + sign in
- [ ] `make bootstrap` (if no tenants) prints test/live secret keys + publishable test key. Copy the secret test key.
- [ ] Sign in at `/login`. Redirect to dashboard.
- [ ] Cookie `velox_session` set, `HttpOnly: ✓`. No API key in localStorage.

### S1.3 Stripe connection
- [ ] Settings → Payments → paste `sk_test_...` + `pk_test_...` → Connect. `vlx_spc_...` shown.
- [ ] Terminal 4: `stripe listen --forward-to localhost:8080/v1/webhooks/stripe/<vlx_spc_...>`. Paste the `whsec_...` back.
- [ ] Settings shows "Connected".

### S1.4 Build the graph
- [ ] Pricing → rating rule `api_calls` flat $0.01. Meter `api_calls` sum, link to rule. Plan `starter` $29/mo, attach meter.
- [ ] Customers → create "Smoke Corp", external_id `smoke_corp`, email any@any.test. Billing profile: address + USD + 10% tax.
- [ ] Customer detail → Set Up Payment → `4242 4242 4242 4242`.
- [ ] Mint a test clock (avoids 30-day wait):
  ```bash
  curl -sS -X POST "$API/v1/test-clocks" -H "Authorization: Bearer $KEY" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"smoke\",\"frozen_time\":\"$(date -u +%FT%TZ)\"}" | jq .
  ```
- [ ] Create the customer pinned to the clock (ADR-027 customer-level attach): Customers → New Customer → tick **Pin to test clock** dropdown → select your clock → Create. Customer Detail header shows the test-clock badge.
- [ ] Customer detail → New Subscription → Starter plan. No clock dropdown — the dialog shows an amber inheritance hint ("This subscription will inherit the customer's test clock — &lt;name&gt;") because the customer is pinned. Server inherits automatically.

### S1.5 Bill + charge
- [ ] Ingest 1,000 events:
  ```bash
  TS=$(date -u +%FT%TZ)
  jq -n --arg ts "$TS" '[range(1000) | {external_customer_id:"smoke_corp",event_name:"api_calls",quantity:"1",idempotency_key:"smoke_\($ts)_\(.)"}]' > /tmp/events.json
  curl -sS -X POST "$API/v1/usage-events/batch" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" --data-binary @/tmp/events.json | jq .
  ```
- [ ] Advance the clock 31 days: `POST /v1/test-clocks/$CLK/advance` with `frozen_time = now+31d` (BSD `date -u -v+31d` / GNU `date -u -d '+31 days'`).
- [ ] `curl -sS -X POST "$API/v1/billing/run" -H "Authorization: Bearer $KEY"` → 1 invoice generated.
- [ ] Invoice auto-finalized, `payment_status=succeeded`. Line items: prorated base + usage + tax.
- [ ] Stripe CLI shows `payment_intent.succeeded`. Dashboard MRR > $0.

### S1.6 Sign out
- [ ] Sidebar → Sign Out. Redirect to /login.
- [ ] Stale cookie on `/v1/whoami` → 401.

**S1 passing = core engine healthy.**

## FLOW S2: AI-native end-to-end smoke

Walks the wedge demo path: instantiate an AI-native recipe → set up a customer on an in_advance plan → ingest LiteLLM-shaped events → observe hybrid invoice → fetch the public cost dashboard. ~15 min. Run this BEFORE every DP demo.

Prereqs: S1 passing (stack healthy, operator key in `$KEY`).

### S2.1 Recipe + plan
- [ ] `POST /v1/recipes/anthropic_style/instantiate {"livemode":false}` → 201. Creates `tokens_input` + `tokens_output` meters + a `pro-anthropic` plan with multi-dim rules.
- [ ] Pricing → edit the plan → set **Base fee billed = At start of period**, base price $99/mo, save. Plan now in_advance with metered usage.

### S2.2 Customer + day-1 invoice
- [ ] Create customer `external_id=cus_demo_ai` with PM `4242 4242 4242 4242`.
- [ ] Create active subscription on `pro-anthropic` → day-1 invoice generated: `billing_reason=subscription_create`, $99 base only, auto-charged.

### S2.3 LiteLLM ingest
- [ ] POST a LiteLLM payload directly (simulates the proxy callback):
  ```bash
  curl -sS -X POST "$API/v1/integrations/litellm/spend" \
    -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -d '{
      "id":"smoke_call_1","call_type":"completion",
      "model":"claude-3-5-sonnet-20241022","custom_llm_provider":"anthropic",
      "user":"cus_demo_ai",
      "usage":{"prompt_tokens":1200,"completion_tokens":350,"total_tokens":1550},
      "response_cost":0.018,"endTime":'$(date +%s)'
    }' | jq .
  ```
  → `{"accepted":2,"skipped":0,"errors":[]}`.
- [ ] Repeat the curl 4 more times with `smoke_call_2` … `smoke_call_5` → 10 events total (5 input + 5 output).
- [ ] `GET /v1/usage-events?external_customer_id=cus_demo_ai&limit=20` → 10 events, all with `dimensions.model=claude-3-5-sonnet-20241022` + `dimensions.provider=anthropic`.

### S2.4 Hybrid invoice at cycle close
- [ ] Mint a test clock + advance ~1 month past sub start (see FLOW S1.4 / TC2 for the curl shape).
- [ ] `POST /v1/billing/run` → 1 cycle invoice. Open it: TWO line types — `tokens_input` + `tokens_output` for the elapsed period AND the $99 base line covering the UPCOMING period. The base line shows "Covers <upcoming range> (in advance)" sub-line.

### S2.5 Public cost dashboard
- [ ] Customer detail → **Public cost-dashboard URL** → Generate URL. Copy the `https://…/v1/public/cost-dashboard/vlx_pcd_…`.
- [ ] `curl <that URL>` (no auth header) → JSON with `customer_id`, `tenant_id`, `billing_period`, `subscriptions[]`, `usage[]` (per-meter + rules), `totals`, `projected_total_cents`. **No PII** (email/display_name/billing_profile absent).
- [ ] Click Rotate in the dashboard → old URL goes 401 immediately; the new URL works.

**S2 passing = wedge demo path is healthy. The AI-native pitch (LiteLLM → multi-dim meters → hybrid invoice → embeddable cost view) works end-to-end.**

---

# Tier 2 — Full Suite

One flow per shipping feature. Run only what your change touched.

**Priority signal:**
- **Demo-blocking** (run before every DP demo): S1, S2, A1-A3, K1-K2, TC1-TC4, B1, B6, B13-B17, CU8, X15, all relevant I-series. Catches wedge regressions and the money path.
- **Compliance / correctness** (run on quarterly review or any tax/dunning change): B2, B10, B11, D1-D4, C1-C3, X1, X5, X7.
- **Operator UX polish** (run only when reworking that surface): K3, R5, B12, B18, CU6, U1, U3, U7-U10. Skip on routine pre-merge if you didn't touch the UI.

The matrix isn't enforced — operators decide based on the change. Default to "run everything your change touched + run S1/S2 always."

## Tenant timezone

Single tenant-wide timezone used for date input and timestamp display
(UTC for storage and billing math). Set in Settings → Account → Timezone.

- [ ] Change timezone to `Asia/Kolkata` or `America/Los_Angeles` → dashboard timestamps shift, zone abbreviation appended (e.g. `2:14 PM PDT`).
- [ ] API key expiry / coupon valid-until / list-page from-to filters interpret civil dates as start/end of day in tenant TZ.
- [ ] Subscription billing math stays UTC ("monthly on the 5th" = 5th UTC).
- [ ] Wire format always UTC ISO 8601 with `Z`.

## FLOW A1: Sign-in

- [ ] Empty form → inline error, no request.
- [ ] Wrong password → 401 "Invalid email or password".
- [ ] 5 consecutive wrong passwords → 5th returns 429 "too many failed attempts — try again in 15 minutes". A 6th attempt during the lock returns 429 again WITHOUT extending the timer (fixed window from the lockout-trigger time, not sliding). Successful login before hitting 5 resets the counter.
- [ ] Without REDIS_URL set, the lockout doesn't enforce — a real DP must run Redis. Boot logs a warning if Redis is unreachable.
- [ ] Right credentials → redirect to `/`, dashboard loads.
- [ ] Cookie `velox_session`: HttpOnly, SameSite=Lax. No `velox_*` in localStorage.
- [ ] Reload → still signed in.
- [ ] Sign out → cookie cleared, redirect to /login. Stale cookie → 401.

### Password reset

- [ ] Forgot password → submit any email → 200 (no enumeration).
- [ ] Reset email lands in Mailpit (http://localhost:8025).
- [ ] Click link → set new password (12+ chars) → /login?reset=success → sign in.
- [ ] Reused token → 422. Token >1h old → 422. Password <12 chars → 422.

## FLOW A2: /v1/whoami

- [ ] Cookie path: `curl -b /tmp/c.txt $API/v1/whoami` → `{tenant_id, user_id, email, livemode}`.
- [ ] Bearer path: `curl -H "Authorization: Bearer $KEY" $API/v1/whoami` → `{tenant_id, key_id, key_type, livemode}`.
- [ ] No credentials → 401.
- [ ] Cookie + Bearer with disagreeing identities → cookie wins.
- [ ] Revoked API key on Bearer → 401 immediately. Cookie sessions unaffected.
- [ ] Publishable key on Bearer → `key_type:"publishable"`. Most write endpoints → 403.

## FLOW A3: Test/Live mode toggle

- [ ] Top-right pill: amber "Test mode" default. Click → emerald "Live mode"; toast "Switched to live mode".
- [ ] **Stays on the same page** (no nav). On `/customers`, `/invoices`, `/audit-log`, `/usage`, `/credits`, `/dunning`, `/webhooks/events` — the page rerenders with the new mode's rows in place.
- [ ] **Back-and-forth is fast**: toggle Test→Live, scroll the list; toggle Live→Test → prior cache renders instantly (no spinner) before any background refetch.
- [ ] Mode-scoped pages reflect the new mode after toggle (no stale rows): Customers, Invoices, Subscriptions, API Keys, Audit Log, Usage Events, Credits, Credit Notes, Dunning, Coupons, Pricing (plans / meters / rating rules), Webhooks (endpoints + event stream), Dashboard (MRR / active customers / recent invoices), **Settings → Payments** (Stripe credentials are keyed `(tenant_id, livemode)` — toggling swaps the connected-state card, masked secret, webhook-secret state, and the "Stripe — test/live mode" header).
- [ ] Mode-agnostic Settings tabs stay the same on toggle (one `tenant_settings` row per tenant, no `livemode` column): Settings → General, Settings → Invoicing, Settings → Tax. Recipes and Onboarding are likewise tenant-wide. Keyed remount still refetches but content is identical.
- [ ] Test Clocks: sidebar entry visible only in test mode. Toggling to live mode hides it; navigating to `/test-clocks` redirects to `/`.
- [ ] On a detail page (e.g. `/customers/cus_test_…`), toggle → page refetches and surfaces a 404 / "Not found" cleanly (entity doesn't exist in the other mode).
- [ ] WebhookEvents `/webhooks/events` SSE stream tears down + reopens on toggle — the frame buffer empties and fills with new-mode events; status pill goes connecting → live.
- [ ] `/v1/whoami` reflects new `livemode` immediately.
- [ ] `POST /v1/auth/mode` without cookie → 401.
- [ ] Live mode + missing live Stripe creds → red banner with "Connect Stripe" link.
- [ ] Logout while in either mode → both per-mode caches gc'd; signing back in starts fresh.
- [ ] **Cross-tab sync**: open the dashboard in two browser tabs. Toggle test→live in Tab A; switch to Tab B without clicking anything. Tab B's pill flips amber→emerald automatically (BroadcastChannel push from Tab A). Tab B's queries refetch live data on next focus — no stale TEST label over live data.
- [ ] **URL params dropped on toggle**: navigate to `/customers?status=active&cursor=cus_test_xxx`, toggle modes. URL becomes `/customers` (search string stripped). The opposite-mode page does not show an empty list because the dead cursor was carried over.
- [ ] **Per-mode invoice numbering**: in test mode, create an invoice → `INV-000001`. Toggle to live mode, create a real invoice → also starts at `INV-000001` (or wherever the live counter sits — independent from test). Test exploration no longer burns live invoice numbers.
- [ ] **Per-mode rate-limit buckets**: hammer the dashboard in test mode until you see `429 Too Many Requests`. Toggle to live mode — live requests should still be allowed (separate bucket). Inspect Redis: `KEYS rl:tenant:*` shows `tenant:<id>:test` and `tenant:<id>:live` as distinct keys.

---

## FLOW K1: API key permissions

- [ ] Secret key: full read/write everywhere.
- [ ] Publishable key: read-only — POST → 403.
- [ ] Revoked key: any request → 401 `api key revoked`.
- [ ] Create dialog: raw key shown once, copy button, "you won't see this again" warning.

## FLOW K2: Expiration

- [ ] Create key with presets: No expiration / 30d / 90d / 1y / Custom.
- [ ] Custom: today is disabled in calendar grid + Today button (tooltip explains minDate).
- [ ] Tenant TZ consistency: pick "30d" → hint "Key will expire on <date> at 11:59 PM <TenantTZ>". Stored UTC matches "23:59:59.999 in tenant TZ".
- [ ] Create with `expires_at = now+90s` via API → 200 until expiry, 401 `api key expired` after.
- [ ] Backdate `expires_at` via psql → 401 `api key expired`.
- [ ] Keys ≤7 days from expiry → yellow "Expires in Xd" badge.
- [ ] Expired keys collapsed under "Expired keys" section; Revoke still enabled.

## FLOW K3: API Keys page UX

- [ ] Create dialog: name (≤100 chars), key type (Secret/Publishable), expiration preset (Custom requires date).
- [ ] Submit success → raw key shown ONCE with Copy button. Closing the dialog removes the raw value from memory; it never re-appears in the active-keys list.
- [ ] Per-row Revoke → typed confirm; revoked key 401s on next request.

## FLOW K4: Rotate

- [ ] Rotate with `expires_in_seconds=300` → new raw_key returned; old key works ~5 min.
- [ ] Rotate with `expires_in_seconds=0` → old key 401 `invalid api key` immediately.
- [ ] Rotate revoked key → 422 `cannot rotate a revoked key`.
- [ ] `expires_in_seconds > 604800` → 422.

---

## Test Clocks (test mode only)

## FLOW TC1: Test Clocks page

- [ ] Sidebar "Test mode" group + "Test Clocks" entry only when livemode=false. Live mode → entry hidden, `/test-clocks` redirects to `/`.
- [ ] Empty state with "New test clock" button.
- [ ] New dialog: optional name + datetime-local picker (defaults to now). Submit → detail page.

## FLOW TC2: Detail + Advance

- [ ] Detail header: name + status pill + Advance/Delete. Advance disabled when status≠ready.
- [ ] Advance dialog presets: `+1h / +1d / +1mo` + custom. Earlier-than-current time → inline error.
- [ ] Submit → API responds in <500ms with `status: "advancing"` and the new `frozen_time`. Dashboard shows "Advancing…" badge (non-blocking — operator can navigate to other pages and the clock continues catching up in the background; ADR-015).
- [ ] `psql` (or any tab) shows `test_clocks.status='advancing'` while the worker runs. Polling `/v1/test-clocks/{id}` every 1.5s flips to `status='ready'` when the worker's catchup loop completes.
- [ ] Generated invoices appear in `/invoices` for the elapsed cycles — one per closed billing period, with `created_at` / `issued_at` aligned to the test-clock timeline (not wall-clock).
- [ ] Catchup failure (UI smoke): set `VELOX_TEST_CLOCK_INJECT_FAILURE="manual UI test"` on the velox process before clicking Advance → the next catchup attempt fails with "injected: manual UI test", clock flips to **internal_failure**, destructive banner surfaces "Catchup failed during last advance. Reason: injected: manual UI test" (ADR-018). The env is one-shot — cleared after firing — so the next click of **Retry advance** runs cleanly, status returns to `ready`. Partial invoices from earlier successful passes remain visible.
- [ ] **Retry advance** (ADR-018): from the internal_failure card, click **Retry advance** → status flips back to `advancing`, the worker resumes the idempotent catchup loop, simulation state preserved (customer + sub + earlier successful advances all intact). Re-fix the underlying issue first; otherwise the retry just re-fails with a fresh reason on the card. Retry from `ready` or `advancing` → 409.
- [ ] **Restart resilience (UI smoke):** start velox with `VELOX_TEST_CLOCK_CATCHUP_DELAY_MS=2000` so each billing pass takes ~2s — gives a deterministic window to time `kill -TERM`. Kick off +1y advance; status flips to `advancing` and stays there for ~24s on a single-sub fixture. Within that window, `kill -TERM` the velox process. Restart **without** the env (or with it, doesn't matter). On boot, slog logs `"test clock: re-enqueued in-flight catchup on boot"`, the worker resumes from the partial frozen_time, and the clock flips to `ready` within seconds. Verify in DB: invoice count matches a single-pass run, no gaps in `billing_period_start`.
- [ ] Detail page lists **Attached customers** (Stripe-parity primary surface, ADR-027) above **Subscriptions on this clock** (drill-down). Each customer row links to `/customers/:id`; each sub row links to `/subscriptions/:id`. Counts in the headers match: `N pinned` customers, total subs ≥ N.
- [ ] **Soft-delete + cascade-cancel** (ADR-016 + ADR-027): create a clock; create 2 customers each pinned to it (each with one active sub) → 2 attached customers / 2 subs. Click Delete. Confirmation dialog reads `"This removes the clock and cancels every active subscription it drives (2 subscriptions across 2 customers)."` After delete: clock disappears from `/test-clocks`, both subs show `status='canceled'` in `/subscriptions`, customers stay (their `test_clock_id` retains the historical pointer for audit), and `psql` confirms `test_clocks.deleted_at IS NOT NULL` (row preserved, hidden by the live filter).
- [ ] **Terminal-state preservation**: before deleting the clock, manually cancel one of the pinned customers' subs. After clock delete, that sub stays canceled (its status doesn't get re-stamped — already terminal). The OTHER customer's sub goes from active → canceled as expected.

## FLOW TC3: Pinning (ADR-027 customer-level)

- [ ] Test mode → Create Customer dialog has "Pin to test clock" dropdown. Empty = wall-clock customer.
- [ ] Live mode → dropdown hidden on Create Customer.
- [ ] Customer Detail header shows test-clock badge when pinned.
- [ ] Customer Detail → Create Subscription dialog has no clock dropdown. Shows amber inheritance hint when the customer is pinned. Server inherits automatically.
- [ ] Subscriptions page → Create Subscription dialog: same behaviour driven by the customer dropdown. Pick a non-pinned customer → no hint, sub goes wall-clock. Pick a pinned customer → amber hint appears below the customer selector ("This subscription will inherit the customer's test clock — &lt;name&gt;"). Switch back to a non-pinned customer → hint disappears.
- [ ] `/subscriptions/:id` for a sub on a pinned customer → "Test clock" badge linking to detail.
- [ ] Test Clock Detail page → "Attached customers" section above "Subscriptions on this clock" — shows customers attached to this clock, each linking to the customer detail.
- [ ] `POST /v1/subscriptions` against a pinned customer → response shows `test_clock_id` matching the customer's clock (server-side inherit; the API does not accept a per-sub `test_clock_id`, mirroring Stripe).
- [ ] `POST /v1/subscriptions` with a stray `test_clock_id` field in the body → field is silently ignored; sub still inherits from the customer.
- [ ] `PATCH /v1/customers/:id` does NOT change `test_clock_id` (immutable per ADR-027 / Stripe parity).

## FLOW TC4: Catchup correctness (ADR-028 disjoint flows + per-sub period loop)

- [ ] Pin monthly sub at "now". Advance 1 month → exactly 1 invoice generated; clock flips ready.
- [ ] Advance 3 months → 3 sequential-period invoices in ONE Advance click (not 3 cron ticks).
- [ ] **Large advance: pin monthly sub, Advance +1y → 12 sequential invoices in one click.** Status flips to `ready`. No `internal_failure`.
- [ ] **+1y boundary: target == frozen_time + exactly 1 year is accepted; +1y +1s is rejected with 422 and `errs.field=frozen_time`** (Stripe-parity per-call cap).
- [ ] **SPA: open Advance dialog, set target > +1y → submit button disabled and inline error names the maximum allowed target.** Toast on submit-attempt reads "Advance cannot exceed 1 year per call — chunk longer ranges into successive advances".
- [ ] **Chunked +5y: click Advance four times in a row (+1y each) → 60 sequential invoices total, status returns to `ready` after each chunk.**
- [ ] **Disjoint cron**: while a clock-pinned sub is at next_billing < frozen_time (gap state), watch slog for ≥1 wall-clock scheduler tick (5 min in dev). The tick must NOT bill the clock-pinned sub. Confirm in DB: invoice count for the sub unchanged across the tick boundary. Only operator Advance click bills clock-pinned subs.
- [ ] **Disjoint cron — full coverage (ADR-029)**: same gap-state setup as above, but extend the assertion to every time-aware path. Across the wall-clock tick boundary: no new charge attempt (`auto_charge_pending=true` row stays untouched), no tax retry (`tax_retry_count` unchanged), no threshold scan firing, no dunning advance (`invoice_dunning_runs.next_action_at` not bumped), no credit expiry on the customer's grants, no reminder-list logging. Then click Advance: all six concerns fire in one operator action — the catchup orchestrator's Phases 1-6 process the deferred consequences in lockstep with simulated time. `slog | grep "scheduler tick"` shows the cron heartbeat is unchanged; the per-phase telemetry from catchup carries the actual work.
- [ ] Advance backwards → 422 `must be strictly after current frozen_time`.
- [ ] **Second advance while advancing → 409.** Force `advancing` via `psql -c "UPDATE test_clocks SET status='advancing' WHERE id='<clock>'"`, then `POST /v1/test-clocks/<clock>/advance` with any future time → `409 Conflict` + `{"error":{"type":"invalid_request_error","code":"invalid_state","message":"clock is advancing, must be ready to advance"}}`. Restore via `UPDATE … SET status='ready'`. After restore, `frozen_time` and `last_failure_reason` must match pre-test state — rejected advance leaves no side effects.
- [ ] Delete clock → pinned subs cancelled; non-pinned subs unaffected.

## FLOW TC5: Dunning via catchup (clock-pinned failure recovery)

The headline test-clock use case — verifies the full Stripe-parity dunning state machine fires correctly under simulated time, including the (a) initial-run binding to frozen-time and (b) catchup-driven advance through retry → escalation → final action.

- [ ] Setup: clock at `frozen_time=2024-02-01`; pinned customer with Stripe test decline card `4000 0000 0000 0341` attached; active monthly sub. Policy: `grace=3d, retry_schedule=[3d, 5d], max=3, final_action=pause`.
- [ ] Advance clock past the first cycle close → invoice finalizes → auto-charges → Stripe declines → dunning run created in the **same Advance** (inline from ChargeInvoice, not waiting for the async webhook) with `created_at` and `next_action_at` anchored on the invoice's cycle-close instant (NOT advance-end frozen_time). ADR-035.
- [ ] **Single-click full walk (Stripe Test Clocks parity)**: from a state where dunning has just started at cycle-close T, advance the clock to T + grace + sum(retry_schedule) + 1d in ONE Advance click. After the advance: run is `state=escalated`, `attempt_count=max`, `resolution=retries_exhausted`, all retry events present in the dunning timeline at distinct simulated-time timestamps (started at T, retry #1 at T+grace, retry #2 at T+grace+retry[0], escalated co-instant with the final retry). NO need to click Advance multiple times — Phase 5 loops until all due retries fire.
- [ ] If `final_action=pause` → owning sub `status=paused`, `pause_collection_behavior=keep_as_draft` (Stripe parity).
- [ ] **Email cadence**: Mailpit shows exactly **N+1 emails per invoice for an N-retry exhausted run** — 1 initial payment-failed + (N-1) dunning-warning ("Attempt k of N") + 1 dunning-escalation. NOT 2 emails per retry (the pre-fix double-send where every `payment_intent.payment_failed` webhook also fired a generic payment-failed email alongside dunning's warning). Verify by querying `SELECT email_type, COUNT(*) FROM email_outbox WHERE payload->>'invoice_number' = '<inv>' GROUP BY email_type`.
- [ ] **Per-customer dunning policy assignment (ADR-036)** — create a second policy on `/dunning-policies` (e.g. "Enterprise", `grace=7d, retry_schedule=[5d, 5d, 5d, 5d], max=5, final_action=manual_review`). Assign it to a customer via the customer detail "Dunning policy" → Change dropdown. Save → re-trigger a failed-payment cycle for that customer → the resulting dunning run carries `policy_id` = the assigned policy; retries follow the new cadence (Enterprise: 7d grace + four 5d gaps), not the tenant default. Re-assign the customer back to "Tenant default" via the dropdown → next-fired run picks up the default policy.
- [ ] **Policy CRUD invariants** — `/dunning-policies` admin page: create a new policy with `max_retry_attempts=5` and `retry_schedule=[72h, 120h]` → save rejects with inline error naming the missing entries (`max_retry_attempts (5) requires at least 4 retry_schedule entries — got 2`). Delete the default policy → rejected ("promote another policy first"). Delete a non-default policy with assigned customers → rejected ("N customer(s) still assigned; reassign first").
- [ ] **Four terminal actions (ADR-036 amendment)** — dropdown options: `Pause collection (keep drafting invoices)`, `Cancel subscription`, `Mark invoice uncollectible`, `Leave open — manual review`. Trigger an exhausted run for each and observe:
  - `pause` → `subscriptions.pause_collection` is set with `behavior=keep_as_draft`; subsequent cycles still draft an invoice for the period (NOT skipped). Status stays `active`. Stripe-aligned semantics, NOT the pre-amendment hard pause that silently dropped cycles.
  - `cancel_subscription` → `subscriptions.status='canceled'`, no further cycle billing.
  - `mark_uncollectible` → the unpaid invoice flips to `status='uncollectible'` (NOT voided). Subscription itself stays untouched.
  - `manual_review` → run state=escalated; no sub/invoice mutation; operator notified.

## FLOW TC6: Trial expiration via catchup

- [ ] Setup: clock-pinned customer; sub created with `trial_period_days=14` → sub `status=trialing`, `trial_end_at = frozen+14d`.
- [ ] `customer.subscription.trial_will_end` fires 3d of simulated time before trial_end_at (Stripe parity).
- [ ] Advance clock to `trial_end_at + 1s` → catchup flips sub to `active`, generates first cycle invoice (`billing_reason=subscription_cycle`).
- [ ] Generated invoice `total_amount_cents` = plan base; `billing_period_start` = trial_end_at; all timestamps in frozen-time.
- [ ] EndTrial (operator override) before clock advance → sub `status=active` immediately, no waiting for `trial_end_at` to be crossed.

## FLOW TC7: Plan change at period boundary via catchup

- [ ] Setup: clock-pinned active sub on plan A ($29/mo). ChangeItem with `new_plan_id=B` ($49/mo), `immediate=false`.
- [ ] Sub item `pending_plan_id=B`, `pending_plan_effective_at = current_billing_period_end` (frozen-time-derived).
- [ ] Advance clock past `current_billing_period_end` → catchup `billOnePeriod` swaps plan_id A→B atomically before billing next period.
- [ ] First post-swap invoice line items reference plan B; total reflects $49 base.
- [ ] Downgrade variant (B cheaper than A, immediate=true): proration credit appears in `customer_credit_ledger` with `entry_type=adjustment`, amount = unused portion of A.

## FLOW TC8: Subscription cancellation at period end (via catchup)

Yearly-sub and future-dated `cancel_at` variants are impractical to verify on wall-clock — test clock is the only path.

- [ ] Setup: clock-pinned active monthly sub. `POST /subscriptions/:id/schedule-cancel` with `at_period_end=true`.
- [ ] Sub `cancel_at_period_end=true`.
- [ ] Advance clock past `current_billing_period_end` → catchup `FireScheduledCancellation` → sub `status=canceled`, `canceled_at` and `ended_at` in frozen-time.
- [ ] No new invoice generated for the period after cancellation.
- [ ] Future-dated `cancel_at` variant (set `cancel_at = frozen+200d` on a yearly sub): advance to before → sub still active. Advance past → sub canceled at the simulated `cancel_at` instant.

## FLOW TC9: Pause collection auto-resume (via catchup)

The ONLY end-to-end manual-test coverage of `pause_collection` with `resumes_at` auto-resume. B6's "Pause → Resume" line tests `Service.Pause` (status flip), which is a different mechanism.

- [ ] Setup: clock-pinned active sub. `POST /subscriptions/:id/pause-collection` with `resumes_at = frozen+7d`, `behavior=keep_as_draft`.
- [ ] Sub `pause_collection_resumes_at = frozen+7d`, `pause_collection_behavior=keep_as_draft`.
- [ ] Advance clock through a cycle boundary while paused → invoice generated but stays DRAFT (no finalize, no charge, no dunning) — engine respects pause_collection.
- [ ] Advance clock past `resumes_at` → catchup auto-clears `pause_collection_*` columns; next cycle bills normally; previously-draft invoice can be finalized manually if intended.

## FLOW TC10: Credit grant expiry firing (via catchup)

The ONLY end-to-end manual-test coverage of credit expiry actually firing. C1 verifies the grant is created with `expires_at` populated but doesn't exercise the cron's `ExpireCredits` job; test clock is the only practical way to verify the expiry mechanic.

- [ ] Setup: clock-pinned customer. Grant $50 credit with `expires_at = frozen+30d`.
- [ ] Customer credit balance = $50; ledger entry `entry_type=grant`.
- [ ] Advance clock past `expires_at` → catchup Phase 4 fires → new ledger entry `entry_type=expiry`, `amount_cents=-5000`, `created_at` in frozen-time. Balance back to $0.
- [ ] Customer detail page Credits tab shows both grant and expiry entries with frozen-time dates.
- [ ] Non-expiring grants (`expires_at IS NULL`) survive arbitrary advances — only expirable grants get expired.

---

## Customer Portal

## FLOW CP1: Magic-link login

- [ ] `POST /v1/public/customer-portal/magic-link {email}` → 202 (always, any email).
- [ ] Real customer → email lands in Mailpit with link to `/portal/magic?token=…`.
- [ ] `CUSTOMER_PORTAL_URL` unset → server logs `magic-link delivery failed err="customerportal: CUSTOMER_PORTAL_URL not set"`.
- [ ] **`POST /v1/public/customer-portal/magic/consume {token}` → 200 + 401 on replay.** Recipe:
  1. Mint: `curl -s -X POST $API/v1/public/customer-portal/magic-link -H 'Content-Type: application/json' -d '{"email":"<real-customer-email>"}'` → `202 Accepted`.
  2. Extract the raw token from the queued email (the hash is what's stored; the raw token only exists in the email payload):
     `TOKEN=$(psql "$DB" -At -c "SELECT split_part(payload->>'magic_link_url', 'token=', 2) FROM email_outbox WHERE email_type='portal_magic_link' ORDER BY created_at DESC LIMIT 1;")`
  3. Consume: `curl -s -i -X POST $API/v1/public/customer-portal/magic/consume -H 'Content-Type: application/json' -d "{\"token\":\"$TOKEN\"}"` → `200` + body `{"token":"vlx_cps_…","customer_id":"vlx_cus_…","livemode":false,"expires_at":"<iso ~24h out>"}`. The response `token` is a **portal session** (different prefix and secret from the input magic-link).
  4. Replay the same token: same curl again → `401` + `{"error":{"type":"authentication_error","code":"unauthorized","message":"invalid or expired magic link"}}`. Same envelope covers never-existed / expired / already-used (anti-enumeration).
  5. DB sanity (optional): `psql "$DB" -c "SELECT id, used_at IS NOT NULL AS consumed FROM customer_portal_magic_links ORDER BY created_at DESC LIMIT 1;"` → `consumed=t` after step 3.
- [ ] Returned token as Bearer on `/v1/me/invoices` → invoice list.
- [ ] `/portal/login` → email form. Submit real customer's email → "Check your email" card.
- [ ] Click email link → `/portal/magic?token=…` → "Signing you in…" → `/portal`.
- [ ] `/portal/magic` no token / invalid token → "Sign-in link not valid" + "Request a new link".
- [ ] `/portal` without session → redirect to `/portal/login`.
- [ ] `/portal` loads: Payment method, Subscriptions, Invoices sections. Header shows tenant company name + Sign out.

## FLOW CP2: Customer cancels subscription

- [ ] `GET /v1/me/subscriptions` (Bearer = portal token) → only that customer's subs.
- [ ] `POST /v1/me/subscriptions/{id}/cancel` → 200, status canceled.
- [ ] Cancel another customer's sub → 404 (no enumeration).
- [ ] Webhook `subscription.canceled` payload includes `canceled_by:"customer"`.
- [ ] UI: each non-canceled sub has Cancel button → typed confirm dialog (`CANCEL`) → row shows `canceled` badge after.

## FLOW CP3: Customer updates payment method

- [ ] Customer must have existing PaymentSetup.
- [ ] `POST /v1/me/payment-method/update {return_url}` → 201 `{url}` (Stripe Checkout setup-mode).
- [ ] Open URL → enter new card → Stripe redirects to return_url → webhook flips `payment_setups.setup_status=ready`.
- [ ] If invoices had `auto_charge_pending=true` → next scheduler tick collects them.
- [ ] No PaymentSetup → 400 `missing_payment_setup`. No live Stripe → 503 `stripe_unavailable`.

## FLOW CP4: Invoice PDF download

- [ ] `curl -OJ -H "Authorization: Bearer $PORTAL_TOKEN" $API/v1/me/invoices/$INV_ID/pdf` → PDF, `Content-Type: application/pdf`, `Content-Disposition: attachment`.
- [ ] Different customer's invoice → 404.

## FLOW CP5: Branding

- [ ] `GET /v1/me/branding` (Bearer = portal token) → tenant company name, logo URL, brand color, support URL.

## FLOW E: Email delivery (SMTP)

Single delivery path: when SMTP isn't configured every send returns
`ErrSMTPNotConfigured`. No stdout fallback. Local dev = Mailpit
(`docker compose up -d mailpit`, `SMTP_HOST=localhost:1025 SMTP_TLS=none`).

Boot warnings on startup (one each when var unset; never fatal):
- `SMTP NOT CONFIGURED`
- `HOSTED_INVOICE_BASE_URL NOT SET` — invoice / receipt / dunning / payment-failed CTAs render with no link
- `CUSTOMER_PORTAL_URL NOT SET`
- `PAYMENT_UPDATE_URL NOT SET`

- [ ] **E1 STARTTLS**: `SMTP_TLS=starttls SMTP_PORT=587` + creds. Trigger invoice email → `email_outbox` row pending → dispatched within seconds → recipient receives.
- [ ] **E2 Implicit TLS**: `SMTP_TLS=implicit SMTP_PORT=465`. Same expectation; verifies `tls.Dial` path.
- [ ] **E3 Not configured**: unset `SMTP_HOST`. Boot → `SMTP NOT CONFIGURED` warning. Trigger send → outbox claims, logs `ErrSMTPNotConfigured`, retries with backoff, lands in DLQ.
- [ ] **E4 5xx bounce**: send to `foo@invalid` → `customers.email_status='bounced'`, `email_bounce_reason` carries the SMTP error.
- [ ] **E5 Per-provider**: verify SendGrid / Postmark / SES / Mailgun / Resend per `docs/ops/email-setup.md`.
- [ ] **E6 Mailpit dev path**: `SMTP_HOST=localhost:1025 SMTP_TLS=none` → mail lands at http://localhost:8025 with HTML+text bodies.

## FLOW EX: Streaming CSV exports

- [ ] **EX1 customers**: `curl -OJ $API/v1/exports/customers.csv` → `customers-YYYYMMDD-HHMMSS.csv`. Date filter `?from=…&to=…` works. Invalid `from` → 400.
- [ ] **EX2 invoices**: `$API/v1/exports/invoices.csv` → invoice rows incl. amounts, period, lifecycle timestamps.
- [ ] **EX3 subscriptions**: `$API/v1/exports/subscriptions.csv` → subs with `plan_ids` (pipe-delimited).
- [ ] **EX4 usage-events**: requires `from`+`to`; missing → 400. Span >366d → 400.
- [ ] Publishable key can call all exports (read-only perm).
- [ ] Streaming verified: large export shows progressive output via `tail -f`.

---

## Billing Engine

## FLOW B1: Arrears + proration (default `in_arrears` plans)

Default `base_bill_timing=in_arrears`: the recurring base + any usage settles at period end. Mid-period sub starts prorate the base. See B15 / B16 for `in_advance` variants.

- [ ] Plan created without `base_bill_timing` → API returns `base_bill_timing: "in_arrears"`.
- [ ] New sub mid-month on this plan → `billing_period_end` = 1st of next month, **NO invoice generated at create time** (cycle path handles it at period close).
- [ ] Run billing before period close → 0 invoices.
- [ ] Backdate `current_period_end` → 1 invoice with prorated base + usage + tax + due date + invoice-number prefix.
- [ ] Invoice line items: base line's `billing_period_start/end` matches the invoice's (current period).

## FLOW B2: Tax precision (basis points)

- [ ] Tax `7.25%` → `tax_rate_bp=725` (no float column).
- [ ] $100 subtotal → tax exactly $7.25.
- [ ] 3 line items $33.33+$33.33+$33.34: per-line tax sums exactly to invoice tax.

## FLOW B3: Idempotency

- [ ] Run billing twice in same period → no duplicate invoice. Logs `invoice already exists for billing period (idempotent skip)`.

## FLOW B4: Auto-charge retry

- [ ] Decline-on-charge card → invoice has `auto_charge_pending=true`, `payment_status=pending`.
- [ ] Update card → next scheduler tick → `payment_status=succeeded`, `auto_charge_pending=false`.

## FLOW B5: Idempotency-Key header

- [ ] POST with `Idempotency-Key: test-123` → 201.
- [ ] Same body + key → same response, 1 row.
- [ ] Same key + different body → 409.

## FLOW B6: Subscription lifecycle

- [ ] Trial 7 days (on either timing) → no invoice during trial. After trial → first invoice generated by the cycle scheduler.
- [ ] Pause → confirm dialog → no billing, no metering. Resume → bills next period.
- [ ] Cancel on `in_arrears` plan → confirm dialog → status canceled, no future billing, no credit grant.
- [ ] Cancel on `in_advance` plan mid-period → confirm dialog → status canceled AND a credit grant lands on the customer's balance for the unused portion of the already-billed period. Description: `Cancel proration — unused portion of <sub_code> base fee (period <start> to <end>, canceled <date>)`. See B17 for the full flow.
- [ ] Cancel on `in_advance` plan AT or AFTER `current_period_end` → no proration credit (period was used in full).

## FLOW B7: Plan change + proration

- [ ] Active sub on Starter → change to Enterprise "immediately" → proration invoice generated.
- [ ] Downgrade immediately → credits to balance.
- [ ] Plan change without immediately → no proration; applies at next period boundary.
- [ ] **Plan change across `base_bill_timing`** (rare): changing a sub from an `in_advance` plan to an `in_arrears` plan (or vice versa) is supported at the next period boundary — the destination plan's timing takes effect, no special cross-boundary proration. Mid-cycle immediate flip across timings is not supported (the engine handles whichever timing the destination declares from cycle-close onward). Operator-facing rule: cross-timing changes should be scheduled `effective=next_period`, not `immediate`.
- [ ] **Plan billing-fields immutability (ADR-034)**: with at least one live sub on a plan, `PATCH /v1/plans/{id}` with a different `base_amount_cents`, `base_bill_timing`, or `meter_ids` → **422** with message naming the blocked field(s) + live-sub count + "Create a new plan instead." Display-only fields (`name`, `description`, `tax_code`, `status`) STILL mutate cleanly on the same call. On a plan with zero live subs, all fields are mutable (covers typo correction at plan creation). Canceled / archived subs do NOT count as live for the guard.

## FLOW B8: Usage caps

- [ ] `usage_cap_units=5000`, `overage_action=block`, ingest 8000 → billed 5000.
- [ ] Switch to `overage_action=charge`, ingest 8000 → billed 8000.

## FLOW B9: Customer price overrides

- [ ] POST /v1/price-overrides → that customer's invoice uses override price.
- [ ] Other customers → default rule price.

## FLOW B10: Manual tax + cross-border zero-rating

- [ ] `tax_home_country="IN"`, `tax_rate_bp=1800`, `tax_name="IGST"`.
- [ ] Domestic IN customer: $100 → $18, name `IGST`, PDF `IGST (18.00%)`.
- [ ] Export US customer: $100 → $0, name `IGST (zero-rated export)`.
- [ ] Customer with no country → 18% applies.
- [ ] Customer with `tax_exempt=true` → $0, blank name.
- [ ] Clear `tax_home_country` → US customer back to 18%.
- [ ] India B2B reverse-charge: PDF carries supplier GSTIN under company line + "Tax payable on reverse charge basis: YES".
- [ ] EU reverse-charge: PDF retains EU wording.
- [ ] Stripe Tax `taxability_reason=not_collecting` round-trip → line item `tax_reason='not_collecting'`, badge in dashboard.
- [ ] `tax_status='exempt'` customer → `tax_reason='customer_exempt'`, PDF carries customer-exemption legend.
- [ ] Invalid country codes (`INDIA`, `in `, `XX`) → 422.

## FLOW B11: Tax-ID validation

- [ ] `in_gst` + `27AAEPM1234C1Z5` → accepted. Legacy `gstin` → normalized to `in_gst` on write.
- [ ] `eu_vat` + `DE123456789`, `au_abn` + `51824753556` → accepted.
- [ ] Unknown Stripe code (`za_vat`, `br_cnpj`) → accepted as-is.
- [ ] Malformed `in_gst` / `eu_vat` / `au_abn` → 422 with format-specific message.
- [ ] Empty `tax_id` → always accepted.

## FLOW B12: Subscription activity timeline

- [ ] Create → activate → pause → resume → plan change → cancel.
- [ ] `GET /v1/subscriptions/{id}/timeline` → events ascending; each carries timestamp, source, event_type, status, description, actor.
- [ ] Operator cancel → "Subscription canceled". Customer cancel → "Subscription canceled by customer".
- [ ] Status colors: emerald/amber/red/violet/blue.
- [ ] Subscription detail UI shows Activity card; resolved actor renders "by {actor_name}".
- [ ] Nonexistent sub ID → 404.

## FLOW B13: Multi-dimensional meters

- [ ] `POST /v1/usage-events` with dimensions `{model, operation, cached, tier}` → 201; value stored as NUMERIC.
- [ ] Decimal preserved end-to-end (`10000.5` round-trips).
- [ ] Replay same idempotency_key → 200 with original event id, no duplicate.
- [ ] Rule with `dimension_match={"operation":"input"}` claims only input events. More-specific match (`+cached:true`) wins over less-specific.
- [ ] Aggregations sum / count / max / last_during_period / last_ever all bill correctly. Switching aggregation between cycles re-bills next cycle without affecting past invoices.
- [ ] `cmd/velox-bench` sustains 50k events/sec on local Postgres.

## FLOW B14: Billing thresholds

- [ ] PATCH sub `billing_thresholds:[{meter_id, usage_gte:10000}]`. Ingest 9999 → no early finalize. Ingest 1 more → invoice auto-finalized within 1 tick, `billing_reason="threshold"`. New events start a new period.
- [ ] PATCH `{amount_gte:50000}` → cross $500 → same shape.
- [ ] Cross threshold + immediately `POST /v1/billing/run` → idempotent skip.
- [ ] Subscription detail "Spend Thresholds" card: empty state with Set button. Edit dialog has subtotal cap, reset_billing_cycle checkbox, per-item rows. Save shows `$1,000.00` (from cents) and `≥10000.5 units`. Clear thresholds → flips to empty.
- [ ] Canceled/archived subs → Set/Edit hidden.

## FLOW B15: `in_advance` plan happy path (ADR-031)

Verifies the day-1 invoice + the cycle-close invoice that bills the upcoming period's base.

- [ ] Pricing → New Plan: select **Base fee billed = At start of period**. Create plan `pro-advance` $49/mo, no meters.
- [ ] Plan Detail → Properties shows `Base fee billed: At start of period`.
- [ ] Create customer with PM (`4242 4242 4242 4242`).
- [ ] Create active subscription on `pro-advance` → **invoice generated immediately**:
  - `billing_reason = "subscription_create"`
  - Period = today → period_end (e.g. first of next month)
  - Single base-fee line, qty 1, $49 (or prorated if mid-month — UI says "prorated X/Y days")
  - Total = $49 + tax
  - `payment_status=succeeded` if PM ready (auto-charged), else `auto_charge_pending=true` + Mailpit shows the no-PM email.
- [ ] Invoice Detail row's "Covers" sub-line not surfaced (line period == invoice period on this day-1 invoice).
- [ ] Advance clock (or wait) to period close → cycle invoice generated:
  - `billing_reason = "subscription_cycle"`
  - Invoice's `billing_period` = the just-elapsed period
  - Single base-fee line, but the line's `billing_period_start/end` = **next period** (period_end → next_period_end)
  - Invoice Detail shows the "Covers <next period range> (in advance)" sub-line under the base row
  - Total = full base, no proration

## FLOW B16: Hybrid `in_advance` base + `in_arrears` usage on one invoice

The standard B2B SaaS shape: platform fee charged at period start, usage settles at period end. Run on top of B15.

- [ ] Plan `pro-advance-metered`: `Base fee billed = At start of period`, $99/mo, with one meter `api_calls` flat $0.01.
- [ ] Day 1: create sub → day-1 invoice carries ONLY the base fee ($99). Usage line absent (no events).
- [ ] Ingest 1,000 events over the period.
- [ ] Period close → cycle invoice:
  - Base line: $99, `billing_period_start/end = next period`, sub-line "Covers <next period> (in advance)"
  - Usage line: $10 (1,000 × $0.01), `billing_period_start/end = elapsed period` (matches invoice header — sub-line suppressed)
  - Single invoice carries both — no separate invoice for the upcoming base.
- [ ] Tax applies to both lines; per-line `tax_amount_cents` populated.
- [ ] Auto-charge fires once for the combined total.

## FLOW B17: `in_advance` cancel proration credit

- [ ] Setup: customer with PM, sub on `pro-advance` ($49/mo), day-1 invoice paid (B15).
- [ ] Cancel mid-period (e.g. day 15 of a 30-day period) → status `canceled`.
- [ ] Customer Detail → Credits tab → new ledger entry:
  - `entry_type = grant`
  - `amount_cents ≈ $24.50` (≈ 15/30 of $49)
  - Description: `Cancel proration — unused portion of <sub_code> base fee (period <start> to <end>, canceled <today>)`
- [ ] Cancel at or after `current_period_end` → no proration credit (period was used in full).
- [ ] Cancel on a pure `in_arrears` sub → no proration credit (nothing was prebilled).
- [ ] Mixed sub (one in_advance item + one in_arrears item) → credit covers only the in_advance item's unused portion.
- [ ] Future invoices on this customer auto-apply the credit (C1 behavior).

## FLOW B18: Meter Detail page

- [ ] Default rule card renders the latest version of the linked rating rule (edit rule → version badge bumps).
- [ ] Add dimension-matched rule: k=v rows, priority, rating-rule select → save → table refetches in priority order.
- [ ] Dimension value coercion: `true/false` → bool, numeric strings → number, else string.
- [ ] Per-row delete: typed `delete` confirm; already-finalized invoices unaffected.

---

## Pricing Recipes

## FLOW R1: List + preview

- [ ] `GET /v1/recipes` → 3 entries (anthropic_style, openai_style, replicate_style) — all AI-native after the Phase 2 wedge-alignment trim.
- [ ] `POST /v1/recipes/{key}/preview` → projected products/prices/meters/dunning/webhooks (no DB writes).
- [ ] Unknown key → 404.

## FLOW R2: Instantiate

- [ ] `POST /v1/recipes/anthropic_style/instantiate {livemode:false}` → 201 with all created IDs. DB now has products + prices + meters + dunning policy + webhook endpoint.
- [ ] Pricing rules carry `dimension_match` JSONB.
- [ ] Audit log: one entry per resource, `actor=recipe:<key>`.
- [ ] Repeat for all 5 recipes — each completes <500ms.

## FLOW R3: Per-tenant idempotency

- [ ] Instantiate same recipe twice → second call 409 `recipe already instantiated`.
- [ ] Different tenant, same recipe → 201.
- [ ] `DELETE /v1/recipes/{key}/instance` cleans up product+prices+meters+webhook+dunning atomically.

## FLOW R4: Atomic rollback

- [ ] Inject mid-instantiate failure (e.g. invalid webhook URL) → 422; zero rows created.
- [ ] No `tenant_recipe_instances` row.

## FLOW R5: Dashboard UI

- [ ] `/recipes` → 3 cards (anthropic_style, openai_style, replicate_style). Preview opens side panel; Instantiate dialog names side-effects and redirects to `/products` on confirm.
- [ ] Uninstall from the Installed card → `recipe_instances` row drops; plans/meters/etc. stay (no cascade). Re-install without renaming originals → 422 name collision; re-install after archiving originals → succeeds.

---

## Invoices

## FLOW I1: Multiple meters

- [ ] Plan with API ($0.01/call) + Storage ($0.10/GB max). Ingest 2000 calls + 50 GB → invoice has 3 line items: base $29 + API $20 + storage $5.

## FLOW I2: Negative usage

- [ ] Ingest 1000 then -200 (correction) → meter shows net 800, billed for 800.

## FLOW I3: Manual line items

- [ ] POST /v1/invoices → draft. Add Line Item "Setup fee" $250, "Consulting" 2×$150 → total $550.
- [ ] Finalize → auto-charges via Stripe.

## FLOW I4: Void

- [ ] Void invoice with credits applied → credits reversed, balance restored, Stripe PI canceled, active dunning resolved, audit log entry.

## FLOW I5: Collect + payment timeline

- [ ] Finalized unpaid → POST /v1/invoices/{id}/collect → PI created.
- [ ] GET /v1/invoices/{id}/payment-timeline → all attempts in order with ts/amount/status/PI id.
- [ ] **Coalesced rows (ADR-020)**: a paid invoice shows ONE "Invoice paid · $29.00" row, NOT a separate "Payment succeeded" row beneath it. A voided invoice with a previously-pending PI shows ONE "Invoice voided" row, NOT a duplicate "Payment canceled" row. A dunning-recovered invoice shows "Invoice paid · after 3 retry attempts" — no separate "Dunning resolved" row.
- [ ] **Failure rows fold inside-out**: each failed charge collapses to ONE row carrying the dunning attempt label ("Automatic retry scheduled" or "Payment retry #N attempted"), the PI id, the amount, the decline reason, and a `Customer notified by email` sub-line. No separate Stripe `payment_intent.payment_failed` row at the same instant; no separate "Payment-failed email sent" row beneath. The Stripe ↔ dunning pairing is by chronological **index** (k-th Stripe failure ↔ k-th dunning attempt), which is the only correlation that works for test-clock-pinned invoices — the dunning row is in simulated time, the Stripe webhook is in wall-clock time, and the two can differ by months.
- [ ] **Charged-card sub-line (ADR-020)**: paid invoice's "Invoice paid" row carries `via Visa •••• 4242` beneath the amount. Holds even when the customer paid via the hosted-invoice URL **without saving the PM** (lookup goes directly to Stripe, not the local payment_methods table). Non-card PMs (bank, wallet) or Stripe lookup failures render no sub-line — graceful, not broken.
- [ ] **Unpaired rows survive**: a Stripe `payment_intent.payment_failed` with no dunning twin (dunning disabled, or webhook arrived ahead of the dunning event) stays as its own "Payment failed" row. A payment-failed email row whose dispatch is still pending or failed stays visible — delivery problems must not silently disappear.

## FLOW I5b: Invoice attention banner

Server-derived from invoice fields. Suppressed for healthy / paid / voided / draft invoices.

### Critical
- [ ] **tax_location_required**: US customer missing postal_code, finalize → red banner "Customer address required", primary action **Edit billing profile**, secondary **Retry tax**.
- [ ] **tax_calculation_failed (provider auth)**: revoke Stripe key → red banner code `tax.provider_auth`, action **Rotate API key**.
- [ ] **payment_failed**: card `4000 0000 0000 9995` → red banner code `payment.declined`, message = truncated `last_payment_error`, actions `[Update payment method, Retry payment]` (ADR-023). Only ONE banner — no hardcoded duplicate below the unified one. Update payment method opens Stripe Checkout in a new tab; Retry payment calls Collect.

### Warning
- [ ] **tax pending**: amber banner with same code/actions, severity warning.
- [ ] **overdue**: past `due_at` → amber banner code `lifecycle.overdue`, actions **Charge now** + **Send reminder**.

### Info
- [ ] **payment_processing**: muted banner, no actions.
- [ ] **payment_scheduled**: `auto_charge_pending=true` → muted banner, action **Charge now**.
- [ ] **awaiting_payment**: muted banner, actions **Charge now** + **Send reminder**.

### Banner shape
- [ ] Severity styling: critical=red+AlertCircle, warning=amber+AlertTriangle, info=muted+Info.
- [ ] Reason badge + dotted code in mono. `since` = relative time. `doc_url` → "Learn more ↗".
- [ ] `detail` (raw provider payload) → `<details>` "Provider response" disclosure.
- [ ] Healthy/paid/voided/draft → no banner.

### Retry tax
- [ ] Banner showing → click **Retry tax** → button "Retrying…" → audit log row `action='retry_tax'` with before/after attention codes.
- [ ] Issue fixed → invoice has `tax_status='ok'`, banner disappears, toast "Tax recalculated successfully".
- [ ] Still failing → banner refreshes with new reason. Each click bumps `tax_retry_count`.
- [ ] Retry on non-pending/non-failed invoice → 409.
- [ ] **Auto-finalize (ADR-017)**: subscription-cycle invoice in `tax_status=pending` → click Retry tax with the underlying issue fixed → invoice flips to `status='finalized'` automatically (one click, not two). Status pill goes Open; hosted-invoice URL appears; auto-charge flow kicks off if a PM is on file.
- [ ] **Manual drafts stay draft**: create a manual invoice, force its tax to pending via tooling, fix the issue, click Retry tax → tax becomes ok BUT invoice stays draft (operator must finalize explicitly). Toast: "Tax computed; click Finalize to issue."

### Background tax-retry reconciler (ADR-017)
- [ ] Force a subscription-cycle invoice into `tax_pending` with `tax_error_code='provider_outage'` and `tax_next_retry_at IS NULL` (e.g. by simulating a Stripe Tax 5xx during finalize). Watch the scheduler tick (default 5min in local) — within one tick the invoice should retry; if the underlying issue resolved, it auto-finalizes.
- [ ] Same shape with `tax_error_code='customer_data_invalid'` → reconciler does NOT touch it (non-retryable code). Manual operator action still required.
- [ ] After 8 attempts the row exits the reconciler scan: `psql -tAc "SELECT id, tax_retry_count, tax_next_retry_at FROM invoices WHERE id='vlx_inv_xxx';"` shows `tax_retry_count=8`, `tax_next_retry_at=NULL`. Banner stays live for the operator; worker stops.
- [ ] Backoff respected: after a single failed retry, `tax_next_retry_at` is ~5min ahead (±10% jitter); after the 5th, ~12 hours ahead. Sub-5-min ticks don't double-process the row.

### List + draft cleanup
- [ ] `/invoices` rows: severity-tinted dot next to invoice number; tooltip surfaces typed reason. Healthy/draft = no dot.
- [ ] Draft rows show "draft" pill (Dashboard) or em dash (Invoices, Subscription detail) instead of `payment_status='pending'`.
- [ ] Invoice detail draft row: muted "Draft invoice — finalize to issue and begin collection." hint.

## FLOW I6: Email + PDF preview

- [ ] Invoice detail → Email → outbox row → PDF attached → Mailpit shows delivery.
- [ ] Preview PDF → renders in overlay; close via X / backdrop.

### Branded HTML body

Multipart text+HTML with tenant chrome. Configure tenant `company_name`, `logo_url`, `brand_color`, `support_url`.

- [ ] Invoice email HTML: tenant logo + name in header, 2px brand-color accent bar, line-items summary, "Amount due" card, **View & pay invoice** CTA styled with brand_color.
- [ ] CTA URL → `{HOSTED_INVOICE_BASE_URL}/invoice/{public_token}`.
- [ ] Footer: "Contact support" + "Powered by Velox Billing".
- [ ] Plain-text part still present.
- [ ] Receipt email: same chrome, CTA "View receipt".
- [ ] Dunning email: attempt N of M, next retry date, CTA "Update payment".
- [ ] Payment-update-request email: CTA uses `PAYMENT_UPDATE_URL` token URL.
- [ ] Operator emails (portal magic-link, password-reset) intentionally plain text — no HTML chrome.

## FLOW I7: Zero-amount invoice

- [ ] Plan `base_amount_cents=0`, no meters → either no invoice or $0 auto-paid (no Stripe charge).

## FLOW I8: Currency consistency

- [ ] Tenant default USD → switch to EUR → new invoices EUR, existing unchanged.
- [ ] Customer with `billing_profile.currency=GBP` → invoices GBP regardless of tenant default.

## FLOW I9: Credit note on void

- [ ] Void invoice → issue CN → error "cannot issue credit note on voided invoice". CN not created.

## FLOW I11: `create_preview`

- [ ] `POST /v1/invoices/create_preview {subscription_id}` → invoice shape with `id=null`, no DB row.
- [ ] Plan-change confirmation dialog renders preview before commit.
- [ ] Cost-dashboard projection populated when engine returns a value.
- [ ] **`in_advance` preview** (ADR-031): for a sub on an `in_advance` plan, preview's `billing_period_start/end` is the **upcoming** period (matches what the cycle invoice will stamp). Base line description carries the "in advance for upcoming period" suffix. Usage line totals match the elapsed period (per the engine's stamping). Totals identical to in_arrears preview — only the period labels differ.

## FLOW I12: One-off invoice composer

- [ ] Customer detail → "New invoice" → inline composer.
- [ ] Add line items → totals tick live → Save draft → row in customer's invoice list with `status=draft`, `subscription_id=null`.
- [ ] Finalize → standard PaymentIntent path.

## FLOW I10: Hosted invoice page

- [ ] Draft invoice has no `public_token`. Finalize → token minted (`vlx_pinv_` + 64 hex).
- [ ] Detail page: **Copy Link** button. **Rotate** typed-confirm dialog (type `ROTATE`). Buttons hidden on draft.

### Public render (open in incognito)
- [ ] Loads without login. Header: tenant logo + company_name + support_url. Optional 3px accent bar.
- [ ] Invoice meta: number (mono), amount due (large tabular), due date.
- [ ] Bill-to + From columns. Line-items table with tabular numerals.
- [ ] Totals: subtotal, optional discount, optional tax with rate, reverse-charge note if applicable, total, amount paid, **Amount due** bold.
- [ ] **Pay {amount}** primary button (brand_color). **Download PDF** secondary.
- [ ] Footer: "Secured by Stripe" + "Powered by Velox Billing".

### Pay
- [ ] Click Pay → `POST /v1/public/invoices/{token}/checkout` → Stripe Checkout. Pay with `4242…` → redirect to `{baseURL}/invoice/{token}?paid=1`.
- [ ] Provisional "Processing your payment…" banner (green spinner).
- [ ] Webhook arrives → invoice paid → page auto-refetches → "Paid on {date}" banner; Pay button gone.
- [ ] PDF still downloads on paid.
- [ ] **Card saved to customer (ADR-021)**: after a successful Pay, dashboard customer detail shows the card under "Payment method" with brand + last 4 + status "Active". The next invoice for this customer auto-charges this PM (no email + Update Payment round-trip). Holds for a customer who had no PM on file before — the Stripe customer is lazily minted on first Pay.
- [ ] **Interactive decline suppresses email (ADR-023)**: Pay with `4000 0000 0000 0002` (decline) → invoice goes to `payment_status=failed` → activity timeline shows the lifecycle row "Payment failed" but NO "Payment-failed email sent" row (customer was watching). Mailpit shows zero new emails. Auto-charge decline (e.g. dunning retry) still emails — only the interactive flow suppresses.

### Variants
- [ ] Voided invoice → "Voided on {date}" banner, no Pay, PDF works.
- [ ] Draft invoice URL → 404.
- [ ] Rotated → old URL 404, new works.

### Security
- [ ] Public JSON has no `tenant_id, subscription_id, tax_id, stripe_*_id`.
- [ ] 61+ req/min same IP → 429 with `Retry-After`.
- [ ] Operator `POST /v1/invoices/{id}/rotate-public-token` requires `PermInvoiceWrite`.

---

## Dunning

## FLOW D1: Retry cycle + escalation

- [ ] Decline card → run billing → dunning run created. Page shows stat cards (Active, Escalated, Recovered, At Risk $) + tab filters with counts.
- [ ] Sidebar Dunning badge shows count.
- [ ] Run state Active, "No retries yet", `next_action_at` scheduled.
- [ ] Backdate `next_action_at` → next tick increments attempt count.
- [ ] After max retries → state Escalated.

## FLOW D2: Resolution

- [ ] "Payment recovered" → invoice marked paid.
- [ ] "Manually resolved" → run resolved without touching invoice.

## FLOW D3: Per-customer override

- [ ] Customer detail → Dunning Override → max_retries=5, grace=7d → applies on next failure.
- [ ] Reset to Default → override removed.

## FLOW D4: Self-service payment update

- [ ] Trigger payment failure → email/log carries `http://localhost:5173/update-payment?token=vlx_pt_…`.
- [ ] Open in incognito → page loads without login, shows customer + invoice + amount, "Secured by Stripe".
- [ ] Click Update → Stripe Checkout setup → new card → redirect → webhook updates PM.
- [ ] Re-open same URL → "Link expired or invalid". Random token → same. No token → "No payment update token provided".

---

## Credits & Credit Notes

## FLOW C1: Credits lifecycle

- [ ] Grant $50 expires 30d → balance $50, ledger Expires column populated.
- [ ] Run billing → applied, amount_due reduced, Stripe charged remainder. Ledger entry "Applied to invoice <number>".
- [ ] Grant $500 + $79 invoice → fully credited, amount_due $0, balance $421, Stripe NOT charged.
- [ ] Deduct $20 → confirmation → balance reduced, ledger entry.

## FLOW C2: Credit notes

- [ ] Unpaid invoice → Issue Credit "Billing error" $20 → preview → Issue → amount_due reduced.
- [ ] Paid invoice → "Credit to balance" $15 → customer balance +$15; CN listed in invoice "Post-payment adjustments".
- [ ] Paid invoice → "Refund to payment method" $10 → Stripe refund; CN badge "Refunded"; balance unchanged.
- [ ] CN > amount_due (unpaid) or > amount_paid (paid) → error.
- [ ] CN page: stat cards (Total Credited, Refunded, Applied to Balance, Issued), tab filters with counts, search, draft CNs show Issue+Void.

## FLOW C3: Coupons

- [ ] Create `PRO20` 20% off, restricted to Enterprise.
- [ ] Redeem on Starter → "coupon is not valid for this plan". Enterprise → applied.
- [ ] Coupon detail Edit dialog: change name → header h1 updates without refresh; audit log records `coupon.updated`.
- [ ] Clear `expires_at` / `max_redemptions` → header tiles flip to "No expiry" / "N redeemed".
- [ ] Restrictions: setting only `min_amount` collapses card to single row. Clearing all three → card disappears.
- [ ] `min_amount: -50` → 422 inline on field.
- [ ] Archived coupon → Edit hidden, Restore visible.

---

## Webhooks

## FLOW W1: Stripe signature verification

- [ ] Valid payload + signature ≤300s → 200, processed.
- [ ] Replay 5+ min later → rejected (timestamp tolerance).
- [ ] Modified payload + original signature → rejected.

## FLOW W2: Outbound secret rotation (72h grace)

- [ ] Rotate Secret on endpoint → modal shows new `whsec_…` + green "Previous secret valid until {ts}" card.
- [ ] API response includes `secret` + `secondary_valid_until`.
- [ ] Endpoints table row shows "Dual-signing until {ts}".
- [ ] Trigger event during grace → header carries TWO `v1=` entries; both old and new verify.
- [ ] Backdate `secondary_secret_expires_at` → header carries one entry; only new verifies.

## FLOW W3: Delivery stats

- [ ] Endpoints page Success Rate column: green ≥95%, amber 70–94%, red <70%.
- [ ] Replay failed event → success rate updates.

## FLOW W4: Live event stream + replay

- [ ] Webhooks → Events → recent deliveries with state dot.
- [ ] Trigger event → row streams in <1s without refresh (SSE).
- [ ] Click delivery → side panel: URL, status, headers, body.
- [ ] Replay failed → fresh attempt fires; original preserved.
- [ ] Multi-retry event → "Diff" tab shows payload diff between attempts.
- [ ] Stop Redis or dispatcher → readiness degraded; UI loads but stops streaming.

---

## Customers

## FLOW CU1: Settings + billing profile

- [ ] Settings: company name change → "Saved" indicator. Navigating with unsaved changes prompts.
- [ ] Currency change → new invoices use it; existing unchanged.
- [ ] Edit billing profile (address, tax ID) → PDF reflects update.
- [ ] **Tax-retry flush on profile update.** Customer with a draft invoice stuck on `tax_error_code = customer_data_invalid` (e.g. US customer missing `postal_code`). Edit billing profile → fill the missing field → Save. Without per-invoice clicking: invoice's `tax_status` flips to `succeeded` (or back to `failed` with a different code if still wrong), and `slog | grep "billing profile flush retried tax errors"` shows `processed >= 1`. Other stuck-tax codes (e.g. `provider_outage`) are NOT replayed by the flush — only `customer_data_invalid`.

## FLOW CU2: Operator customer-portal API

- [ ] `GET /v1/customer-portal/{customer_id}/overview` → active subs, recent invoices, credit balance.
- [ ] `/subscriptions`, `/invoices` scoped to that customer.

## FLOW CU3: GDPR export + erasure

- [ ] `GET /v1/customers/{id}/export` → customer + profile + invoices + subs + ledger + balance.
- [ ] Stripe IDs redacted (last 4 visible); PM details redacted.
- [ ] Delete with active subs → "cancel them before deletion".
- [ ] Cancel subs, `POST /v1/customers/{id}/delete-data` → display_name="Deleted Customer", email cleared, profile PII anonymized, status archived, invoices preserved, audit entry.
- [ ] Re-export deleted customer → anonymized.

## FLOW CU4: Archive cascade

- [ ] Archive → confirm → amber banner "data is read-only". Action buttons hidden.
- [ ] Run billing → no invoices for archived customer's subs. Existing invoices and credits still readable.
- [ ] Restore → banner gone, actions reappear.
- [ ] Customers list → Archived tab.

## FLOW CU6: Brand color + logo URL

- [ ] Settings → Business → Logo URL accepts public HTTPS URL. Live thumbnail renders. Invalid → "Couldn't load image".
- [ ] Brand color: native color picker + hex input + Clear. Invalid hex (`#zzz`, missing `#`, uppercase) → 422 client+server.
- [ ] Save → invoice PDF: company name tinted, 2px accent bar.
- [ ] Clear color → next PDF byte-identical to pre-migration neutral.

## FLOW CU9: Sent emails on customer page

Mirrors Stripe's customer-page "Sent emails" section (docs.stripe.com/invoicing/send-email). Operator audit / support surface for "did we tell the customer?"

- [ ] Customer Detail → "Sent emails" card lists every customer-facing email sent for this customer in the last 30 days, newest first. Empty state: "No emails sent in the last 30 days."
- [ ] After running a dunning catchup that exhausts an N-retry policy: the card shows N+1 rows for the run's invoice — 1 "Payment failed" + (N-1) "Payment retry — action required" + 1 "Retries exhausted". Each row's recipient matches `billing_profile.email` (else `customer.email`); each carries a wall-clock send time (real-time SMTP dispatch instant, NOT the simulated cycle date).
- [ ] Failed delivery rows show the SMTP error inline (red text under the type label).
- [ ] Invoice-number link on each row navigates to the invoice list filtered to that number.
- [ ] **Invoice activity timeline** no longer shows standalone "Dunning reminder emailed" / "Dunning escalation emailed" rows for those emails — the customer-page section is the canonical view (avoids wall-clock-vs-simulated time-domain mismatch in the invoice activity timeline). The initial Stripe-webhook payment-failed email still folds onto the `dunning_started` row as a "Customer notified by email" sub-line (same time domain — both wall-clock).
- [ ] `GET /v1/customers/{id}/sent-emails` returns `{sent_emails: [...]}` with fields `id, email_type, recipient, status, invoice_number?, last_error?, created_at, dispatched_at?` — 30-day window, ORDER BY created_at DESC.

## FLOW CU8: Cost-dashboard public projection

- [ ] Customer detail → "Public cost-dashboard URL" card → click **Generate URL**. Operator sees `vlx_pcd_<64 hex>` token and full URL. Copy button works.
- [ ] `POST /v1/customers/{id}/rotate-cost-dashboard-token` → `{token, public_url}`. Token prefix `vlx_pcd_` + 64 hex; URL is `{VELOX_API_BASE_URL or relative}/v1/public/cost-dashboard/<token>`.
- [ ] `GET /v1/public/cost-dashboard/{token}` (no auth) → sanitized projection:
  - Present: `customer_id`, `tenant_id`, `billing_period {start, end, source}`, `subscriptions[]` (id + plan_name + currency + period), `usage[]` (meter_key + meter_name + unit + currency + total_quantity + total_amount_cents + rules[]), `totals[]`, `projected_total_cents`.
  - **Absent**: email, display_name, external_id, metadata, billing_profile, plan_id, rating_rule_version_id, warnings.
- [ ] No active sub → `billing_period.source='no_subscription'`, empty arrays, **NOT 5xx** (200 with the empty envelope).
- [ ] Rotate → previous URL returns 401 `invalid cost-dashboard token` immediately. Audit log records the rotation (`action=rotate`, `resource_type=customer`, `metadata.surface=cost_dashboard_token`); the plaintext token is NEVER in the audit row.
- [ ] Tampered / unknown token → 401 (same 401 as rotated — anti-enumeration). Wrong prefix (no `vlx_pcd_`) → 401 fast-path without DB lookup.
- [ ] Rate limit: 61+ req/min/IP → 429 (shares the hosted-invoice bucket; tighter than the general 100/min).
- [ ] Embeddable widget (not yet shipped): a `<VeloxCostDashboard token baseUrl />` React widget is the natural next step — defer until first design partner asks. The JSON projection is consumer-ready as-is; partners can render their own UI today.

---

## Platform

## FLOW P1: Feature flags

- [ ] `GET /v1/feature-flags` → seeded flags: `billing.auto_charge`, `billing.tax_basis_points`, `webhooks.enabled`, `dunning.enabled`, `credits.auto_apply`, `billing.stripe_tax`.
- [ ] `PUT /v1/feature-flags/webhooks.enabled {enabled:false}` → events not delivered. Re-enable → resumes.
- [ ] Per-tenant override: `PUT /…/overrides/{tenant_id}` disables for one tenant; DELETE falls back to global.
- [ ] Toggle reflects within 30s.

## FLOW P2: Audit log

- [ ] Several actions (create customer, grant credits, void invoice, change plan) → all logged.
- [ ] Stat cards: Total, Today, Unique Actors, Destructive Actions.
- [ ] Destructive rows have red left border. Expand → metadata + "View" link.
- [ ] Filters: resource type, action, date range. Export CSV → all entries.

## FLOW P3: Usage summary

- [ ] `GET /v1/usage-summary/{customer_id}?from=…&to=…` → per-meter aggregated totals matching ingestion.

## FLOW P4: Empty billing cycle

- [ ] No subs due → trigger billing → "0 invoice(s) generated", clean exit, dashboard unchanged.

## FLOW P5: Health checks

- [ ] `/health` → 200 `{"status":"ok"}`. `/health/ready` → 200 with database, scheduler ok.
- [ ] Stop Postgres → `/health/ready` → 503 `degraded` with `database: error:…`. `/health` still 200.
- [ ] Kill scheduler goroutine or wait past 2× interval → readiness shows scheduler degraded.

## FLOW P6: Tax fallback metrics

- [ ] `curl -H "Authorization: Bearer $METRICS_TOKEN" /metrics | grep velox_tax_fallback_total` → counter registered.
- [ ] Reasons increment correctly: `no_country` (customer missing country), `no_client_for_mode` (one-mode tenant), `api_error` (invalid Stripe key).
- [ ] Happy path → counter unchanged.

---

## UI / UX

## FLOW U1: Dashboard

- [ ] 4 KPI cards: MRR (sparkline+trend), Active Customers, Failed Payments (red if >0), Revenue 30d.
- [ ] Revenue bar chart, Recent Activity (last 5 invoices clickable).
- [ ] Get Started checklist: 6 steps, auto-tracks against server state, self-hides at 100%. Dismiss persists per-tenant.
- [ ] No "Trigger Billing" button (use `POST /v1/billing/run`).

## FLOW U3: Usage Events page

- [ ] Stat cards: Total Events, Total Units, Active Meters, Active Customers.
- [ ] Meter breakdown bars.
- [ ] Filters: customer, date range. Stat cards stay constant when paging (reflect all filtered rows).
- [ ] Decimal precision: `0.5 + 0.5 + 0.0001` → `1.0001` (no rounding).
- [ ] Export CSV.

## FLOW U7: Edge cases

| Case | Expected |
|------|----------|
| Zero usage | Base fee only |
| Meter without rating rule | Usage silently skipped |
| Invalid `external_customer_id` on ingest | "customer not found" |
| Invalid `event_name` on ingest | "meter not found" |
| Void already voided invoice | Error |
| Finalize non-draft invoice | Error |
| Duplicate subscription code | Humanized error |
| Cancel canceled subscription | Error |
| Esc from modal with form data | "Unsaved changes" prompt |
| Typed destructive confirm | `VOID` / `CANCEL` / `DELETE` required to enable submit; wrong word keeps button disabled |

(Duplicate Idempotency-Key behaviour — same-body cached, different-body 409 — covered by FLOW B5.)

## FLOW U8: Request-ID in error toasts

- [ ] Force any API error → toast shows `Request ID: req_…` (clickable to copy).
- [ ] Even when response envelope fails to parse → Request-Id from `Velox-Request-Id` header still appears.
- [ ] `grep req_… server.log` matches the toast.

## FLOW U10: Public pages

- [ ] `/invoice/:token` (FLOW I10), `/update-payment` (FLOW D4), `/checkout/success`, `/login` all load without auth.

---

# Tier 3 — Deep / Rare

Major releases, infra changes, post-mortems.

## FLOW X1: RLS multi-tenant isolation

- [ ] Bootstrap Tenant A + key A; create "Alpha Corp". Bootstrap Tenant B + key B; list customers with key B → Alpha NOT visible.
- [ ] `GET /v1/customers/{alpha_id}` with key B → 404.
- [ ] Same check for invoices, subs, credits — cross-tenant reads must 404.

## FLOW X2: Bootstrap lockdown

- [ ] No `VELOX_BOOTSTRAP_TOKEN` → POST /v1/bootstrap → 403 `bootstrap disabled`.
- [ ] Wrong token → 403 `invalid bootstrap token`.
- [ ] Correct token, tenants exist → 409 `bootstrap already completed`.
- [ ] `make bootstrap` CLI always works.

## FLOW X3: Rate limiting

- [ ] 100+ concurrent requests → first 100 ok, rest 429 with `Retry-After` + `X-RateLimit-*` headers.
- [ ] Wait 10s, 20 more → ~16 allowed (GCRA refill 1.67/sec).
- [ ] Tenant A exhausted → Tenant B succeeds (separate buckets).
- [ ] Stop Redis → requests succeed (fail-open in dev). `APP_ENV=production` → fail-closed.
- [ ] `/health`, `/health/ready`, `/metrics` not rate-limited.

## FLOW X4: Security headers + metrics auth

- [ ] `curl -I /v1/customers` carries: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Cache-Control: no-store`, `Referrer-Policy: strict-origin-when-cross-origin`.
- [ ] Staging/prod: `Strict-Transport-Security` present.
- [ ] `METRICS_TOKEN=secret123` set → `/metrics` 401 unauth, 200 with Bearer. Unset → `/metrics` accessible (dev).

## FLOW X5: PII encryption at rest

- [ ] `VELOX_ENCRYPTION_KEY` set (64 hex). Create customer + billing profile.
- [ ] `SELECT display_name, email FROM customers …` → values prefixed `enc:`. API returns plaintext.
- [ ] `SELECT legal_name, email, phone, tax_id FROM customer_billing_profiles …` → all 4 prefixed `enc:`.
- [ ] Pre-encryption rows still read correctly (no `enc:` prefix → returned as-is).

## FLOW X7: Stripe Tax

- [ ] `PUT /v1/feature-flags/billing.stripe_tax {enabled:true}`. Customer with full address → invoice tax calculated by Stripe; `tax_name` shows jurisdiction; per-line tax populated.
- [ ] Invalid Stripe key → invoice generates with $0 tax (graceful fallback); counter `velox_tax_fallback_total{reason="api_error"}` +1.
- [ ] `tax_exempt=true` → $0 tax regardless.
- [ ] India-registered Stripe account → blocked at account level → use FLOW B10.
- [ ] **Re-connect flushes stuck tax (ADR-019)**: with Stripe disconnected, advance a test clock to generate an invoice → invoice goes `tax_status=pending` with `tax_error_code=provider_not_configured`. Reconnect Stripe in Settings → Payments. Toast reads `Connected test mode as <Account>` with description `Retrying 1 invoice that was stuck on tax in the background.` Reload `/invoices` after a moment — invoice flipped to `Open` (engine-generated → auto-finalized via ADR-017 chain). No per-invoice manual Retry-tax click required.

## FLOW X8: Migration rollback

- [ ] `make migrate-status` → version N. `migrate rollback` → N-1. `make migrate` → N.
- [ ] `docker compose down -v && make up && make dev` → fresh DB applies all migrations; status matches `ls migrations/*.up.sql | wc -l`.

## FLOW X9: Config validation

- [ ] No `VELOX_ENCRYPTION_KEY` in production → fatal.
- [ ] Key not 64 hex / not valid hex → fatal.
- [ ] `APP_ENV=production` no `REDIS_URL` → warn "rate limiting will fail open".
- [ ] `APP_ENV=foo` → warn listing expected values. `PORT=not-a-port` → warn.
- [ ] `DB_MAX_IDLE_CONNS > DB_MAX_OPEN_CONNS` → warn.
- [ ] All valid → zero WARN-level config logs.

## FLOW X10: OpenTelemetry tracing

```bash
docker run -d --name jaeger -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:2
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 go run ./cmd/velox
```

- [ ] Hit several endpoints. Jaeger UI at :16686, service `velox`.
- [ ] HTTP spans (method+path), `billing.RunCycle` with `batch_size`, `billing.BillSubscription` with `subscription_id`/`tenant_id`.
- [ ] HTTP → billing parent-child relationship visible.

## FLOW X11: Large batch usage ingestion

- [ ] POST /v1/usage-events/batch with 1000 events → `{accepted:1000, rejected:0}`.
- [ ] Include duplicate idempotency keys → duplicates rejected.
- [ ] Run billing → aggregated correctly.

## FLOW X14: Self-host (Compose)

- [ ] `docker compose up -d postgres redis mailpit` → 3 sidecars healthy.
- [ ] `make bootstrap` + `make dev` + `cd web-v2 && npm run dev`. `/health` and `/health/ready` 200.
- [ ] `RUN_MIGRATIONS_ON_BOOT=true` (default) applies all migrations idempotently.
- [ ] Mail catches at `localhost:8025`.

## FLOW X15: LiteLLM integration (ADR-033)

The wedge integration. Validates the adapter accepts LiteLLM's `StandardLoggingPayload`, resolves customer + meters, and dedupes replays.

- [ ] Create a customer with `external_id="cus_litellm_test"`.
- [ ] Instantiate the `anthropic_style` recipe (creates `tokens_input` + `tokens_output` meters).
- [ ] Single payload happy path:
  ```bash
  curl -X POST "$API/v1/integrations/litellm/spend" \
    -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -d '{
      "id":"litellm_test_1","call_type":"completion",
      "model":"claude-3-5-sonnet-20241022","custom_llm_provider":"anthropic",
      "user":"cus_litellm_test",
      "usage":{"prompt_tokens":1200,"completion_tokens":350,"total_tokens":1550},
      "response_cost":0.018,"endTime":1730000000
    }'
  ```
  → `{"accepted":2,"skipped":0,"errors":[]}`. `GET /v1/usage-events?external_customer_id=cus_litellm_test` shows TWO events (tokens_input qty 1200 + tokens_output qty 350), both with `dimensions.model="claude-3-5-sonnet-20241022"` + `dimensions.provider="anthropic"`.
- [ ] Idempotent replay: same POST again → `accepted=0` (events already exist; the `(tenant, customer, meter, idempotency_key)` UNIQUE caught the replay). No duplicates in the event list.
- [ ] Missing `user`: payload with `"user":""` → response has `errors[]` populated, batch otherwise OK. **NOT 5xx.**
- [ ] Unknown customer: `"user":"cus_nonexistent"` → same partial-failure shape: `errors[].error` says `customer "cus_nonexistent" not found (set user=...)`.
- [ ] Non-token-bearing call: `"call_type":"image_generation"` → `accepted=0, skipped=1`. No events emitted.
- [ ] Zero-token completion (error / empty response): `"usage":{"prompt_tokens":0,"completion_tokens":0}` → `accepted=0, skipped=1`.
- [ ] Batch shape: POST `{"events":[<payload1>,<payload2>,...]}` → each payload mapped independently. Per-row failures don't fail the batch.
- [ ] Bare array shape: POST `[<payload1>,<payload2>]` → same handling as `events:[...]`.
- [ ] Embedding call: `"call_type":"embedding","usage":{"prompt_tokens":500,"completion_tokens":0}` → ONE event (`tokens_input` only), `accepted=1`.
- [ ] Dimension promotion: `"metadata":{"team_id":"team_eng","request_tags":["prod"],"x_other":"ignored"}` → emitted events have `dimensions.team_id="team_eng"` and `dimensions.request_tags=["prod"]`; `x_other` stays in the event's `metadata.litellm_metadata.x_other` (NOT promoted to dimensions for pricing dispatch).
- [ ] Cost surfacing: `cost_breakdown:{input_cost:0.012,output_cost:0.045,total_cost:0.057}` → input event's `metadata.velox.litellm_cost_usd=0.012`, output event's = 0.045. Velox billing math is unaffected — pricing rules drive the invoice amount; LiteLLM's cost is audit-only.
- [ ] Auth: POST without `Authorization` header → 401. Publishable key (no `usage:write`) → 403.
- [ ] Audit-trail sanity: each ingested event shows `origin=api` in `usage_events.origin` (no separate "litellm" origin in v1; revisit when an operator asks).

---

# Diagnostics

## Server won't start
- `VELOX_ENCRYPTION_KEY` rejected → FLOW X9 (must be 64 hex chars).
- Postgres unreachable → `docker compose ps`, `make up`.
- Port 8080 in use → `lsof -i :8080`, kill stale.
- Migration dirty → resolve SQL error, then `migrate force <version>`.

## Sign-in fails
- 401 → wrong creds. Re-bootstrap or use password-reset.
- 429 → 5 wrong attempts, 15-min lockout. Check `users.locked_until`.
- CORS: `CORS_ALLOWED_ORIGINS` must include frontend origin.
- Cookie not set → check `Set-Cookie` on response. `Secure` in dev should be off.
- Cookie present but every request 401s → check `dashboard_sessions.expires_at` / `revoked_at`.

## Invoice didn't generate
- Subscription not due → period end in future. Backdate for testing.
- Already billed → FLOW B3 (idempotent skip).
- Subscription paused / customer archived / trial active → no billing.
- `billing.auto_charge` off → invoice generated but not charged.

## Stripe Tax errors
- Unsupported home country → FLOW B10 manual fallback.
- Missing customer address → counter `velox_tax_fallback_total{reason="no_country"}` +1.
- Tenant in disconnected mode → `{reason="no_client_for_mode"}`.
- Invalid key → `{reason="api_error"}`.

## Rate limit not triggering
- Redis not connected → `redis-cli ping`, check `REDIS_URL`.
- Sequential curl too slow — use parallel.
- Public endpoints (`/health`, `/metrics`, `/v1/bootstrap`) intentionally unrestricted.

## PII not encrypted
- `VELOX_ENCRYPTION_KEY` not set when row created → backward-compat plaintext (FLOW X5).
- Wrong field — only customer display_name/email + billing profile legal_name/email/phone/tax_id are encrypted.

## Webhook signature fails
- Wrong `whsec_…` after `stripe listen` restart (CLI rotates per run).
- Clock skew >5 min → rejected (FLOW W1).
- Wrong webhook URL — must be `/v1/webhooks/stripe/<vlx_spc_…>`.
