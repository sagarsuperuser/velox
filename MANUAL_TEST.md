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
- [ ] `POST /v1/recipes/anthropic_style/instantiate {"livemode":false}` → 201. Creates ONE `tokens` meter + the `ai_api_pro` plan with per-`{model, token_type}` pricing rules (ADR-044: input / output / cache_read / cache_write_5m / cache_write_1h per model).
- [ ] Pricing → edit the `ai_api_pro` plan → set **Base fee billed = At start of period**, base price $99/mo, save. Plan now in_advance with metered usage.

### S2.2 Customer + day-1 invoice
- [ ] Create customer `external_id=cus_demo_ai` with PM `4242 4242 4242 4242`. Note its internal `id` (`cus_…`) from the response — used as `customer_id` below.
- [ ] Create active subscription on `ai_api_pro` → day-1 invoice generated: `billing_reason=subscription_create`, $99 base only, auto-charged.

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
  → `{"accepted":2,"skipped":0}` (one event per token role).
- [ ] Repeat the curl 4 more times with `smoke_call_2` … `smoke_call_5` → 10 events total (5 input + 5 output).
- [ ] `GET /v1/usage-events?customer_id=<internal cus_ id>&limit=20` → 10 events on meter `tokens`, each with `dimensions.model=claude-3.5-sonnet` (canonical recipe family, ADR-044), `dimensions.model_raw=claude-3-5-sonnet-20241022` (verbatim), `dimensions.provider=anthropic`, and `dimensions.token_type` ∈ {`input`,`output`} (5 each). (The list filter is the internal `customer_id`, not `external_customer_id`.)

### S2.4 Hybrid invoice at cycle close
- [ ] Mint a test clock + advance ~1 month past sub start (see FLOW S1.4 / TC2 for the curl shape).
- [ ] `POST /v1/billing/run` → 1 cycle invoice generated.
- [ ] Invoice has a **Tokens** usage line for `input` and for `output` both with **non-zero** amounts, each priced at the recipe's claude-3.5-sonnet decimal rates.
- [ ] Invoice has the $99 base line covering the UPCOMING period; the base line shows "Covers &lt;upcoming range&gt;" (date range only — no "(in advance)" parenthetical).
- [ ] Usage line totals equal what `create_preview` showed (cycle == preview) — holds here because this sub has no `usage_cap_units` and no mid-period plan/item change; preview does not replicate cap-scaling or segment proration (ADR-045).

### S2.5 Public cost dashboard
- [ ] Customer detail → **Public cost-dashboard URL** → Generate URL. Copy the `https://…/v1/public/cost-dashboard/vlx_pcd_…`.
- [ ] `curl <that URL>` (no auth header) → JSON with `customer_id`, `tenant_id`, `billing_period`, `subscriptions[]`, `usage[]` (per-meter + rules), `totals`, `projected_total_cents`. **No PII** (email/display_name/billing_profile absent).
- [ ] Click Rotate in the dashboard → old URL goes 401 immediately; the new URL works.

**S2 passing = wedge demo path is healthy. The AI-native pitch (LiteLLM → one `tokens` meter priced by `{model, token_type}` → hybrid invoice → embeddable cost view) works end-to-end.**

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
- [ ] API key expiry / list-page from-to filters interpret civil dates as start/end of day in tenant TZ.
- [ ] **Subscription billing dates are anchored in the tenant timezone (ADR-058).** Set tenant TZ = `Asia/Kolkata`. Create an anniversary-monthly sub starting the **1st of a month** (e.g. Jun 1, in IST). The first period is **Jun 1 → Jul 1** = **30 days** (a June anniversary), NOT `Jun 1 → Jul 2` / 31 days. A mid-cycle upgrade prorates against the **30-day** cycle.
- [ ] Verify the SAME result regardless of whether the server runs `TZ=UTC` or `TZ=Asia/Kolkata` — the period and proration denominator must not depend on the host timezone.
- [ ] **Anniversary month-end clamps and restores, never ratchets (ADR-055).** Create an **anniversary**-monthly sub on a clock-pinned customer, activated on **Jan 31**. Advance the clock month by month: the cycle boundaries (`next_billing_at` / invoice periods) land on **Jan 31 → Feb 28 → Mar 31 → Apr 30 → May 31 → Jun 30** — clamped to month-end in short months and **restored to the 31st** in long ones, NOT drifting to the 3rd (`subscriptions.billing_anchor_day = 31`). A day-30 anchor gives Feb 28 then the 30th; a leap **Feb 29** anchor (monthly or yearly) bills Feb 28 in common years and Feb 29 in leap years. Calendar-monthly subs are unaffected (still the 1st; `billing_anchor_day = 0`).
- [ ] Calendar-monthly anchored on the **31st** rolls to the **1st of next month** (does not skip February: Jan 31 → Feb 1).
- [ ] **Invoice period shows the INCLUSIVE last day (ADR-058 follow-up).** The invoice (detail-page header, Invoices-list Period column, and the PDF / hosted invoice / portal) shows **"Jun 1, 2028 – Jun 30, 2028"** for a June period — the last day actually covered — NOT the exclusive boundary "Jun 1 – Jul 1". Same string on every surface (one backend-authored `billing_period_display`). A one-off invoice (no period) shows no Period row. A per-line **"Covers <start> – <inclusive end>"** note (shown on a proration/mixed line whose span differs from the invoice's) is likewise inclusive — "Covers Jun 15, 2028 – Jun 30, 2028", not "– Jul 1". The raw API `billing_period_start`/`billing_period_end` stay unchanged (half-open RFC3339 instants).
- [ ] Wire format always UTC ISO 8601 with `Z` (storage/display is UTC; the *calendar arithmetic* for period boundaries is done in the tenant TZ per ADR-058).

## FLOW A1: Sign-in

- [ ] Empty form → inline error, no request.
- [ ] Wrong password → 401 "Invalid email or password".
- [ ] 5 consecutive wrong passwords locks the account for 15 min, but the lock is **invisible in the response** — the 5th (and any attempt during the window) still returns the same generic **401 "invalid email or password"**, never a distinct 429/`account_locked`. This is deliberate anti-enumeration: a distinct locked response is an oracle confirming the email is a real account. Verify the lock fired by then submitting the **correct** password during the window → also 401 (Authenticate refuses it while locked). A successful login before the 5th attempt resets the counter.
- [ ] Lockout still fires with **`REDIS_URL` unset** (or Redis stopped) — the counter degrades to a per-process one, it does not switch off (velox-ops #21). Same observable: 5 wrong then a correct password → still 401 during the window. (Stop Redis mid-session and the WARN "serving from in-process counter" logs once.)
- [ ] Right credentials → redirect to `/`, dashboard loads.
- [ ] Cookie `velox_session`: HttpOnly, SameSite=Lax. No `velox_*` in localStorage.
- [ ] Reload → still signed in.
- [ ] Sign out → cookie cleared, redirect to /login. Stale cookie → 401.
- [ ] **Auth events are audited (2026-06-12)**: after the above, open `/audit-log` → a **"login"** row (resource `user`, Actor = the operator, **not** "System"), a **"logout"** row, and (if you toggled test/live) a **"mode_changed"** row — each with the operator as actor and your IP. A **failed** login writes **NO** audit row (it's in the server security log instead); confirm the failed attempts above did not add audit-log rows.

### Password reset

- [ ] Forgot password → submit any email → 200 (no enumeration).
- [ ] Reset email lands in Mailpit (http://localhost:8025).
- [ ] Click link → set new password (12+ chars) → /login?reset=success → sign in.
- [ ] **Reset is audited (2026-06-12)**: `/audit-log` shows a **"password_reset_requested"** row (when the email matched a real account) and a **"password_reset_completed"** row on the affected `user`, scoped to the operator's tenant.
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
- [ ] Mode-scoped pages reflect the new mode after toggle (no stale rows): Customers, Invoices, Subscriptions, API Keys, Audit Log, Usage Events, Credits, Credit Notes, Dunning, Pricing (plans / meters / rating rules), Webhooks (endpoints + event stream), Dashboard (MRR / active customers / recent invoices), **Settings → Payments** (Stripe credentials are keyed `(tenant_id, livemode)` — toggling swaps the connected-state card, masked secret, webhook-secret state, and the "Stripe — test/live mode" header).
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
- [ ] **Per-mode rate-limit buckets**: hammer the dashboard in test mode until you see `429 Too Many Requests`. Toggle to live mode — live requests should still be allowed (separate bucket). Inspect Redis: `KEYS rl:*` shows the rate-limit buckets, keyed `rl:<namespace>:<scope>:<id>` (e.g. `rl:general:ip:1.2.3.4`); test- and live-mode buckets are distinct keys.

---

## FLOW K1: API key permissions

- [ ] Secret key: full read/write everywhere.
- [ ] Publishable key: read-only — POST → 403.
- [ ] Revoked key: any request → 401 `invalid or expired API key`.
- [ ] Create dialog: raw key shown once, copy button, "you won't see this again" warning.

> **Wire-message convention (ADR-026):** the API never reveals *why* a key failed — revoked, expired, and unknown all return the same generic 401 `invalid or expired API key`. The specific reason is logged server-side only. Don't assert revoked-vs-expired-vs-unknown from the response body.

## FLOW K2: Expiration

- [ ] Create key with presets: No expiration / 30d / 90d / 1y / Custom.
- [ ] Custom: today is disabled in calendar grid + Today button (tooltip explains minDate).
- [ ] Tenant TZ consistency: pick "30d" → hint "Key will expire on <date> at 11:59 PM <TenantTZ>". Stored UTC matches "23:59:59.999 in tenant TZ".
- [ ] Create with `expires_at = now+90s` via API → 200 until expiry, then 401 `invalid or expired API key` (generic — see K1 note).
- [ ] Backdate `expires_at` via psql → 401 `invalid or expired API key`.
- [ ] Keys ≤7 days from expiry → yellow "Expires in Xd" badge.
- [ ] Expired keys collapsed under "Expired keys" section; Revoke still enabled.

## FLOW K3: API Keys page UX

- [ ] Create dialog: name (≤100 chars), key type (Secret/Publishable), expiration preset (Custom requires date).
- [ ] Submit success → raw key shown ONCE with Copy button. Closing the dialog removes the raw value from memory; it never re-appears in the active-keys list.
- [ ] Per-row Revoke → typed confirm; revoked key 401s on next request.

## FLOW K4: Rotate

- [ ] Rotate with `expires_in_seconds=300` → new raw_key returned; old key works ~5 min.
- [ ] Rotate with `expires_in_seconds=0` → old key 401 `invalid or expired API key` immediately.
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
- [ ] **`+1mo` preset is a calendar month (2026-06-13):** with the clock frozen at e.g. Jun 15, clicking `+1mo` fills Jul 15 — same day-of-month, landing exactly ON the monthly cycle boundary — not Jul 16 (+31 days).
- [ ] Submit → API responds in <500ms with `status: "advancing"` and the new `frozen_time`. Dashboard shows "Advancing…" badge (non-blocking — operator can navigate to other pages and the clock continues catching up in the background; ADR-015).
- [ ] `psql` (or any tab) shows `test_clocks.status='advancing'` while the worker runs. Polling `/v1/test-clocks/{id}` every 1.5s flips to `status='ready'` when the worker's catchup loop completes.
- [ ] Generated invoices appear in `/invoices` for the elapsed cycles — one per closed billing period, with `created_at` / `issued_at` aligned to the test-clock timeline (not wall-clock).
- [ ] **"Last advance results" card (2026-06-13)** — once the clock polls back to `ready` after the advance above, the detail page shows a **Last advance results** card beneath "Current clock time": the simulated span ("Advanced \<from\> → \<to\>") and a row per non-zero outcome — e.g. **Invoices generated: 3**. The count matches the invoices that actually appeared in `/invoices`. Advance again over a period where nothing is due → the card reads **"No billing activity — nothing was due in this period."** Trigger a partial-failure advance (FLOW TC2 injection knob) → after it lands in `internal_failure` the card still shows what completed, with the caveat line "This advance ended with an error…". `psql`: `SELECT last_advance_summary FROM test_clocks WHERE id='<clk>'` holds the JSON.
- [ ] Catchup failure (UI smoke): set `VELOX_TEST_CLOCK_INJECT_FAILURE="manual UI test"` on the velox process before clicking Advance → the next catchup attempt fails with "injected: manual UI test", clock flips to **internal_failure**, destructive banner surfaces "Catchup failed during last advance. Reason: injected: manual UI test" (ADR-018). The env is one-shot — cleared after firing — so the next click of **Retry advance** runs cleanly, status returns to `ready`. Partial invoices from earlier successful passes remain visible.
- [ ] **Retry advance** (ADR-018): from the internal_failure card, click **Retry advance** → status flips back to `advancing`, the worker resumes the idempotent catchup loop, simulation state preserved (customer + sub + earlier successful advances all intact). Re-fix the underlying issue first; otherwise the retry just re-fails with a fresh reason on the card. Retry from `ready` or `advancing` → 409.
- [ ] **Restart resilience (UI smoke):** start velox with `VELOX_TEST_CLOCK_CATCHUP_DELAY_MS=2000` so each billing pass takes ~2s — gives a deterministic window to time `kill -TERM`. Kick off +1y advance; status flips to `advancing` and stays there for ~24s on a single-sub fixture. Within that window, `kill -TERM` the velox process. Restart **without** the env (or with it, doesn't matter). On boot, slog logs `"test clock: re-enqueued in-flight catchup on boot"`, the worker resumes from the partial frozen_time, and the clock flips to `ready` within seconds. Verify in DB: invoice count matches a single-pass run, no gaps in `billing_period_start`.
- [ ] Detail page lists **Attached customers** (Stripe-parity primary surface, ADR-027) above **Subscriptions on this clock** (drill-down). Each customer row links to `/customers/:id`; each sub row links to `/subscriptions/:id`. Counts in the headers match: `N pinned` customers, total subs ≥ N.
- [ ] **Soft-delete + cascade-cancel + customer detach** (ADR-016 + ADR-027): create a clock; create 2 customers each pinned to it (each with one active sub) → 2 attached customers / 2 subs. Click Delete. Confirmation dialog reads `"This removes the clock and cancels every active subscription it drives (2 subscriptions across 2 customers)."` After delete: clock disappears from `/test-clocks`, both subs show `status='canceled'` in `/subscriptions`, `psql` confirms `test_clocks.deleted_at IS NOT NULL` (row preserved, hidden by the live filter), and both customers stay but are **detached** — `SELECT test_clock_id FROM customers` is **NULL** for each (no longer pinned to the dead clock), and the customer-detail header no longer shows the simulated/test-clock badge. The canceled subs keep their `test_clock_id` pointer (historical cache).
- [ ] **No stranded sub after clock delete (2026-06-13):** after the delete above, create a NEW subscription for one of the (now detached) customers. It is a normal **wall-clock** sub — `test_clock_id` is NULL, it appears for the wall-clock scheduler, and it bills on the next tick / advances its period.
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
- [ ] **Disjoint cron — full coverage (ADR-029)**: same gap-state setup as above, but extend the assertion to every time-aware path. Across the wall-clock tick boundary: no new charge attempt (`auto_charge_pending=true` row stays untouched), no tax retry (`tax_retry_count` unchanged), no threshold scan firing, no dunning advance (`invoice_dunning_runs.next_action_at` not bumped), no credit expiry on the customer's grants. Then click Advance: all five concerns fire in one operator action — the catchup orchestrator's Phases 1-5 process the deferred consequences in lockstep with simulated time. `slog | grep "scheduler tick"` shows the cron heartbeat is unchanged; the per-phase telemetry from catchup carries the actual work.
- [ ] **Usage ingests in simulated time on an advanced clock (2026-06-13):** pin a usage-plan sub, Advance +1 month, then POST a usage event with NO timestamp → event's `timestamp` = frozen_time (not wall-clock). POST one timestamped inside the simulated current period (wall-clock future) → 200, accepted. POST one past frozen_time + 5 min → 422 "must not be in the future". Backfill timestamped between wall-now and frozen_time → 200, `origin=backfill`. Advance again → the cycle invoice bills those events. Live-mode keys: behavior unchanged (wall-clock gates, no clock lookup).
- [ ] **Usage summary follows the clock (2026-06-13):** with the clock advanced into a future month, `GET /v1/usage/summary/{customer}` (no `?from/?to`) defaults to the FROZEN month's window, not the wall-clock month.
- [ ] Advance backwards → 422 `must be strictly after current frozen_time`.
- [ ] **Second advance while advancing → 409.** Force `advancing` via `psql -c "UPDATE test_clocks SET status='advancing' WHERE id='<clock>'"`, then `POST /v1/test-clocks/<clock>/advance` with any future time → `409 Conflict` + `{"error":{"type":"invalid_request_error","code":"invalid_state","message":"clock is advancing, must be ready to advance"}}`. Restore via `UPDATE … SET status='ready'`. After restore, `frozen_time` and `last_failure_reason` must match pre-test state — rejected advance leaves no side effects.
- [ ] Delete clock → pinned subs cancelled; non-pinned subs unaffected.

## FLOW TC5: Dunning via catchup (clock-pinned failure recovery)

The headline test-clock use case — verifies the full Stripe-parity dunning state machine fires correctly under simulated time, including the (a) initial-run binding to frozen-time and (b) catchup-driven advance through retry → escalation → final action.

- [ ] Setup: clock at `frozen_time=2024-02-01`; pinned customer with Stripe test decline card `4000 0000 0000 0341` attached; active monthly sub. Policy: `grace=3d, retry_schedule=[3d, 5d], max=3, final_action=pause`.
- [ ] Advance clock past the first cycle close → invoice finalizes → auto-charges → Stripe declines → dunning run created in the **same Advance** (inline from ChargeInvoice, not waiting for the async webhook) with `created_at` and `next_action_at` anchored on the invoice's cycle-close instant (NOT advance-end frozen_time). ADR-035.
- [ ] **Single-click full walk (Stripe Test Clocks parity)**: from a state where dunning has just started at cycle-close T, advance the clock to T + grace + sum(retry_schedule) + 1d in ONE Advance click. After the advance: run is `state=escalated`, `attempt_count=max`, `resolution=retries_exhausted`, all retry events present in the dunning timeline at distinct simulated-time timestamps (started at T, retry #1 at T+grace, retry #2 at T+grace+retry_schedule[0], escalated co-instant with the final retry). NO need to click Advance multiple times — Phase 5 loops until all due retries fire.
- [ ] If `final_action=pause` → owning sub `status=paused`, `pause_collection_behavior=keep_as_draft` (Stripe parity).
- [ ] **Card-less clock-pinned sub reaches a terminal under catchup (2026-06-23, ADR-060)**: pin a customer with **no saved card** to a clock; advance past a cycle close → invoice finalizes `auto_charge_pending` (no charge) and catchup Phase 3.5 creates a dunning run for it; keep advancing through grace + retries → run escalates to `final_action` (e.g. sub `status=paused`). Without this the card-less simulated invoice would never dun.
- [ ] **Email cadence**: Mailpit shows exactly **N+1 emails per invoice for an N-retry exhausted run** — 1 initial payment-failed + (N-1) dunning-warning ("Attempt k of N") + 1 dunning-escalation. NOT 2 emails per retry. Verify by querying `SELECT email_type, COUNT(*) FROM email_outbox WHERE payload->>'invoice_number' = '<inv>' GROUP BY email_type`.
- [ ] **Per-customer dunning policy assignment (ADR-036)** — create a second policy on `/dunning-policies` (e.g. "Enterprise", `grace=7d, retry_schedule=[5d, 5d, 5d, 5d], max=5, final_action=manual_review`). Assign it to a customer via the customer detail "Dunning policy" → Change dropdown. Save → re-trigger a failed-payment cycle for that customer → the resulting dunning run carries `policy_id` = the assigned policy; retries follow the new cadence (Enterprise: 7d grace + four 5d gaps), not the tenant default. Re-assign the customer back to "Tenant default" via the dropdown → next-fired run picks up the default policy.
- [ ] **Policy CRUD invariants** — `/dunning-policies` admin page: create a new policy with `max_retry_attempts=5` and `retry_schedule=[72h, 120h]` → save rejects with inline error naming the missing entries (`max_retry_attempts (5) requires at least 4 retry_schedule entries — got 2`). Delete the default policy → rejected ("promote another policy first"). Delete a non-default policy with assigned customers → rejected ("N customer(s) still assigned; reassign first").
- [ ] **Four terminal actions (ADR-036 amendment)** — dropdown options: `Pause collection (keep drafting invoices)`, `Cancel subscription`, `Mark invoice uncollectible`, `Leave open — manual review`.
- [ ] `pause` → `subscriptions.pause_collection` is set with `behavior=keep_as_draft`; subsequent cycles still draft an invoice for the period (NOT skipped). Status stays `active`.
- [ ] `cancel_subscription` → `subscriptions.status='canceled'`, no further cycle billing.
- [ ] `mark_uncollectible` → the unpaid invoice flips to `status='uncollectible'` (NOT voided). Subscription itself stays untouched. Audit row `invoice.update` with `action=marked_uncollectible`. Webhook `invoice.marked_uncollectible` fires.
- [ ] `manual_review` → run state=escalated; no sub/invoice mutation; operator notified.
- [ ] **A failed terminal action keeps the run re-attemptable, not falsely "escalated" (2026-06-23)**: set `final_action=cancel_subscription` (or pause/uncollectible) and make the mover fail at exhaustion (e.g. the sub is in a conflicting state). The run lands `state=active`, `resolution=action_failed`, `next_action_at` set (NOT `escalated`/`retries_exhausted`) — so a later due tick re-runs the action and it escalates once it succeeds. Pre-fix the run showed a clean "escalated" beside a sub/invoice that never actually closed.
- [ ] **An ambiguous retry doesn't write off a paid invoice (2026-06-23)**: during dunning, make a retry charge return an *unknown* outcome (Stripe 5xx/timeout — drop the result) on an invoice whose PaymentIntent actually **succeeded**. The dunning run does NOT tick `attempt_count` for that retry and does NOT exhaust → no spurious cancel/uncollectible; the reconciler then marks the invoice `paid`. A *definite* decline still ticks and escalates normally.
- [ ] **Operator-driven uncollectible from the dunning resolve dialog** — on an active dunning run, click Resolve → pick **Write off invoice** → confirm. The dunning run flips to `resolved` with `resolution=invoice_not_collectible` AND the underlying invoice flips to `status=uncollectible` (cross-flow per ADR-036). Invoice detail page reflects the change: status banner reads "Marked uncollectible — recorded as bad debt", Collect Payment / Mark Uncollectible buttons disappear, Record Payment + Void + Issue Credit remain.
- [ ] **Uncollectible page UX (Stripe parity — verified across Stripe + Chargebee + Recurly 2026-05-20)** — on an `uncollectible` invoice: InvoiceAttention banner is hidden, OperatorContext/Diagnosis card is hidden, status banner explains the bad-debt classification + that the subscription stays active + recovery options. Buttons present: Void, Email, Issue Credit, Record Payment, Copy Link, Preview/Download PDF. Buttons absent: Collect Payment, Mark Uncollectible, Finalize, Add Line Item.
- [ ] **Stripe-parity offline recovery: uncollectible → paid** — click Record Payment on an uncollectible invoice, optionally enter a reference (e.g. "Cheque #1234"), confirm. Invoice flips to `status=paid`, `payment_status=succeeded`, `paid_at` set, `stripe_payment_intent_id` prefixed `out_of_band:` so reports can distinguish operator-recorded payments from engine charges. Audit row carries `recovered_from_status=uncollectible`. Webhooks `invoice.payment_recorded` AND `invoice.paid` both fire (the latter from MarkPaid on every paid transition — card, credits, offline, dunning recovery). Active dunning run (if any) resolves to `payment_recovered`.
- [ ] **Uncollectible → Void reverses the committed tax exactly once (product-audit G1):** on a `stripe_tax` invoice with a committed `tax_transaction_id`, Mark Uncollectible (files one tax reversal), then Void it. The Void files **no second reversal** — both transitions use the same invoice-stable reversal reference (`inv_taxrev_<id>`), so Stripe dedups to one reversal (verify: one reversal on the Stripe Tax transaction, not two). Pre-fix the two used distinct references (`inv_uncoll_` vs `inv_void_`) and reversed the tax twice → the tenant under-remitted. (Manual/none tax providers have no `tax_transaction_id`, so no reversal either way.)
- [ ] **Audit timestamps: wall-clock primary, sim-time in metadata (ADR-030 amendment 2026-05-28)** — on the clock-pinned sub, click Cancel from the subscription detail page. Open `/audit-log`, find the just-written `subscription.cancel` row. `created_at` is wall-clock (within ~5s of system time the operator clicked) — NOT the test clock's simulated frozen_time. Row shows an amber **test clock** chip next to the action label. Expand the row: the "Timestamp" cell carries an amber subline reading "Effect on test clock `<clock_id>` at `<simulated frozen_time>`". `metadata.sim_effective_at` matches the sub's current period end (the simulated effect-time of the cancellation); `metadata.test_clock_id` matches the sub's pin.
- [ ] **Dunning resolve on a clock-pinned invoice stamps simulated `paid_at`** — from an active dunning run on a clock-pinned invoice, click Resolve → Payment recovered. Reload invoice detail. `invoice.paid_at` lands in simulated time (the test clock's current frozen_time), NOT wall-clock.
- [ ] **Payment reconciler stamps simulated `paid_at` for clock-pinned PaymentUnknown invoices** — simulate an ambiguous charge outcome on a clock-pinned invoice (Stripe API timeout / 5xx → invoice lands `payment_status=unknown` with a populated `stripe_payment_intent_id`). Wait ~70s wall-clock for the reconciler to fire. After resolution, `invoice.paid_at` lands in simulated time (test clock's frozen_time at the moment of the original charge attempt), NOT today's wall-clock.
- [ ] **Dropped failure-webhook is fully recovered by the reconciler — dunning + email, not just a status flip (ADR-049 Phase 2)** — charge a card that declines, but DROP the `payment_intent.payment_failed` webhook (e.g. stop `stripe listen` / point the endpoint away) so the invoice sits `payment_status=unknown` (ambiguous) or `processing`. Wait for the reconciler tick. The invoice flips to `failed` AND a dunning run exists for it AND a `payment_failed` email is enqueued (`email_outbox`) AND `payment.failed` fired (`webhook_outbox`) — identical to the webhook path.
- [ ] **A redelivered `payment_intent.payment_failed` notifies once, not twice (2026-06-13, migration 0118)** — decline a charge (`4000 0000 0000 9995`) on a non-interactive auto-charge so the customer gets a payment-failed email, then **resend the same event** (`stripe events resend <evt_id>`, or re-POST the identical webhook). Mailpit shows exactly **one** "payment failed" email and the webhook event stream shows exactly **one** `payment.failed` (the redelivery is a no-op — `slog` logs "duplicate or out-of-order payment failure for this payment intent"). A genuinely **new** retry that fails (a fresh PaymentIntent) DOES notify again.
- [ ] **A redelivered `payment_intent.succeeded` settles once, not twice** — auto-charge an invoice to `paid`, then **resend the same event** (`stripe events resend <evt_id>`, or re-POST the identical webhook). The invoice stays `paid`, Mailpit shows exactly **one** receipt, and the webhook stream shows exactly **one** `invoice.paid` — the redelivery is a no-op (terminal-state guard logs "payment already settled; skipping duplicate success settlement"; the webhook store also dedups on `(tenant, livemode, stripe_event_id)`). A genuinely **new** PaymentIntent that succeeds (e.g. dunning recovery on the same invoice) DOES settle + receipt again. (Success twin of the `payment_intent.payment_failed` row above; the guard differs because `paid` is terminal while failure is not.)
- [ ] **Stale `processing` is swept once the PI settles (ADR-049 Phase 2)** — leave an invoice at `payment_status=processing` past the 30-min processing cool-off (drop the success webhook). Once Stripe's PI is `succeeded`, the reconciler marks the invoice `paid` and enqueues the receipt; while the PI is still `processing`/`requires_action` at Stripe, the reconciler leaves it alone (no premature settle). A `succeeded` webhook that lands DURING the reconciler's Stripe round-trip wins — the reconciler's fresh re-read sees the invoice already paid and skips (no duplicate receipt).
- [ ] **Manual invoice on a clock-pinned customer carries the "simulated" badge (ADR-030 addendum)** — pin a customer to a test clock (no subscription needed), Advance the clock, then create a one-off invoice from Customer Detail → New invoice. The invoice header (next to the status pill) and the Invoices-list row both show an amber **Simulated** badge; the issued/paid dates are simulated frozen-time. `invoices.is_simulated = true` in DB. The header **Terms** matches the term picked in the composer (e.g. Net 7 → "Net 7") and equals `Due − Issued`, NOT the tenant default. Picking **Due on receipt** is honored as 0 days → `Due == Issued` and Terms reads "Due on receipt" (NOT silently coerced to Net 30). The **Period** field is absent (a one-off invoice has no billing cycle — it is shown only for subscription-cycle invoices with a real span).
- [ ] **Manual invoice Issued/Due anchor to finalize, not draft-create** — compose a draft on a clock-pinned customer, **Advance the clock**, then Finalize. The header **Issued** date is the *finalize* moment (the advanced clock time), NOT the earlier draft-create time, and **Due** = Issued + the term. On the activity timeline the **Invoice created** and **Invoice finalized** rows show *different* timestamps (created = compose time, finalized = the later finalize time), not the same instant. Cycle invoices are unaffected (born finalized at build time).
- [ ] **Credits always precede card charges (2026-06-13)**: grant a customer a credit balance smaller than an unpaid flagged invoice → next scheduler tick (or clock Advance) charges only the remainder, never the full amount; balance fully covering the invoice → invoice flips to paid with NO Stripe charge and the flag clears. Same on a dunning retry: credits granted after the failure reduce (or fully settle) the retried charge.
- [ ] **Manual invoice collection mirrors cycle invoices** — finalize a manual invoice for a customer with **no saved card**: no "your invoice" email is auto-sent; instead the customer receives a **"set up payment method"** email and `invoices.auto_charge_pending = true` (the scheduler charges automatically once a card is attached). For a customer **with a saved card**: the invoice auto-charges, the customer gets a **payment receipt** on success (no separate invoice email), and `auto_charge_pending` stays false. Either way the operator can still send the invoice explicitly via the **Send** button. Matches what a subscription-cycle invoice does at finalize.
- [ ] **No-payment-method nudge resends the SETUP link, not the invoice email (2026-06-14)** — on that finalized-unpaid no-card invoice, the attention card's button reads **"Resend setup link"**. Click it → a "Setup link resent" toast (no recipient dialog) and Mailpit shows another **"set up payment method"** email (`POST /v1/invoices/{id}/resend-setup-link`) — the same Checkout-setup link, NOT the hosted-invoice "pay this invoice" email. For an invoice in a different attention state (e.g. `payment_failed`), the `send_reminder` button still opens the email dialog and sends the hosted-invoice pay link.
- [ ] **Activity vs real-time lanes don't interleave clocks** — on a clock-pinned invoice that has been finalized + emailed, the invoice detail shows two cards: **Activity** (billing lifecycle + dunning, simulated dates) and a wall-clock lane holding the customer emails. The email "sent" rows are NOT mixed into the Activity list (which would sort a real-time row before the simulated event that triggered it). If a clock-pinned invoice has a standalone Stripe payment outcome (failed/canceled with no dunning twin to fold it), it joins the wall-clock lane too and the card title reads **"Real-time activity"** instead of "Notifications"; on a non-simulated invoice, Stripe payment rows stay in **Activity** where operators expect them.
- [ ] **Void of a clock-pinned invoice with a pending PI shows no duplicate "Payment canceled" row** — finalize a clock-pinned invoice (pending PI), then Void it. The timeline shows the void but NOT a separate "Payment canceled" Stripe row (the PI-cancel webhook is folded into the void).
- [ ] **Credit-note timeline lane follows its own time domain (2026-06-13, migration 0117)** — on a clock-pinned in_advance sub, advance the clock and **downgrade** the plan so the engine issues a clawback credit note against the simulated prebill: its "Credit note issued" row lands in the **Activity** lane (simulated dates), NOT "Real-time activity," and `SELECT is_simulated FROM credit_notes` is **true** for that row. Then on the **same** invoice issue a credit note via the operator action (Issue Credit): that row lands in the **Real-time activity** lane with a wall-clock timestamp, and its `is_simulated` is **false**. (On a non-simulated invoice there is one lane and both credit notes show wall-clock dates — unaffected.)

## FLOW TC6: Trial expiration via catchup (ADR-037)

Trial transitions flip status AT `trial_end_at` (Phase 0.5), not at
the next cycle close. First chargeable period anchors on
`trial_end_at` per the `(billing_time × interval)` matrix.

**Calendar + monthly + trial (the common case):**

- [ ] Create sub on clock-pinned customer with `trial_days=14`, `billing_time=calendar`, monthly plan. Result: `status=trialing`, `trial_end_at = frozen+14d`, `current_period_start = trial_end_at` (day-snapped), `current_period_end = first-of-next-month-after-trial_end_at` (the post-trial stub).
- [ ] Dashboard timeline reads `Created → Trial ends → First charge`. Stat card heading says **Trial period** (not Billing Period) with the trial dates. `isPast` checkmarks honor frozen_time, not wall-clock.
- [ ] Advance clock to `trial_end_at + 1s` → catchup Phase 0.5 fires: sub.`status=active`, sub.`activated_at = trial_end_at` (exact instant, not advance-end). No cycle invoice yet — `next_billing_at` is the stub's close.
- [ ] `subscription.trial_ended` webhook fires once, `triggered_by="schedule"`, `ended_at = trial_end_at`.
- [ ] Continue advance past `next_billing_at` (the first-of-next-month) → catchup Phase 1 bills the stub period prorated. `billing_period_start` = `trial_end_at`, `billing_period_end` = first-of-next-month, `total_amount_cents` = plan base × `stub_days / full_cycle_days` (full_cycle_days = days from period_start to the next billing boundary for the plan's interval — 28–31 for monthly).

**Anniversary + monthly + trial:**

- [ ] Same setup with `billing_time=anniversary` → no stub. `current_period_start = trial_end_at`, `current_period_end = trial_end_at + 1 month`. First chargeable cycle is a full month from trial-end. All other assertions identical.

**Yearly plan + trial:**

- [ ] Sub with `trial_days=14` on a yearly plan → `current_period_end = trial_end_at + 1 year` regardless of `billing_time`. (Stripe and Lago both force anniversary semantics for yearly; calendar setting is ignored.)

**EndTrial-early (operator override):**

- [ ] Before clock crosses `trial_end_at`, operator clicks **End trial now**. Sub instantly: `status=active`, `trial_end_at = now (truncated)`, `current_period_start = now (day-snapped)`, `current_period_end = next-anchor-for-billing_time-and-interval`. No 8-day "deferred billing" gap.

**In_advance + trial coverage:**

- [ ] Setup with an `in_advance` plan + trial. At trial expiry (either via clock advance OR EndTrial), `BillOnCreate` fires for `[current_period_start, current_period_end]` — covers the first paid period at the activation instant. Invoice `billing_reason=subscription_create`. In_arrears plans skip this (cycle billing handles them at period close).

## FLOW TC7: Plan change at period boundary via catchup

- [ ] Setup: clock-pinned active sub on plan A ($29/mo). ChangeItem with `new_plan_id=B` ($49/mo), `immediate=false`.
- [ ] Sub item `pending_plan_id=B`, `pending_plan_effective_at = current_billing_period_end` (frozen-time-derived).
- [ ] Advance clock past `current_billing_period_end` → catchup `billOnePeriod` swaps plan_id A→B atomically before billing next period.
- [ ] First post-swap invoice line items reference plan B; total reflects $49 base.
- [ ] Downgrade variant (B cheaper than A, immediate=true) on a **paid** in_advance source: the unused portion of A is credited via a **credit note** against the paid invoice (ADR-048) — balance credit = **gross** (net + the reversed tax slice on a taxed prebill; gross == net when untaxed), proportional tax reversed.

## FLOW TC8: Subscription cancellation at period end (via catchup)

Yearly-sub and future-dated `cancel_at` variants are impractical to verify on wall-clock — test clock is the only path.

- [ ] Setup: clock-pinned active monthly sub. `POST /subscriptions/:id/schedule-cancel` with `at_period_end=true`.
- [ ] Sub `cancel_at_period_end=true`.
- [ ] Advance clock past `current_billing_period_end` → catchup `FireScheduledCancellation` → sub `status=canceled`, `canceled_at` and `ended_at` in frozen-time.
- [ ] No new invoice generated for the period after cancellation.
- [ ] Future-dated `cancel_at` variant (set `cancel_at = frozen+200d` on a yearly sub): advance to before → sub still active. Advance past → sub canceled at the simulated `cancel_at` instant.

## FLOW TC8b: Mid-period cancel of an UNPAID in-advance prebill (#22)

Setup: clock-pinned customer on an in_advance plan (e.g. $30/mo), day-1 invoice finalized but left **unpaid** (no payment method / declined). Cancel immediately (`POST /subscriptions/:id/cancel`) mid-period, not at period end.

- [ ] **Partial consumption** (cancel ~halfway through the period): the prebill invoice's **amount due drops to the consumed portion** (e.g. $30 → ~$15) — a credit note for the unused portion is linked on the invoice. Invoice is NOT voided and stays collectible for the consumed amount.
- [ ] **No consumption** (cancel at/near period start): the prebill invoice **status → voided** (no credit note).
- [ ] Customer credit balance is **unchanged** on both paths — no balance credit is granted for an invoice that was never paid.
- [ ] Paid-invoice contrast (mark the prebill paid first, then cancel mid-period): unused portion goes to the **customer credit balance**; the invoice is not voided/reduced. **On a taxed prebill (ADR-048):** the clawback is issued as a **credit note** against the paid invoice — the balance credit is the **gross** unused (net + the tax the customer paid on it), and the credit note carries the proportional reversed tax (a Stripe Tax reversal is filed for `stripe_tax`). Zero-tax prebills are unchanged (gross == net). The same applies to an immediate **plan swap** of a paid in_advance period.

## FLOW SUB-CARD: Subscription billing-cycle card surface

Locks in the 2026-05-20 "Renews on" annotation + alignment tooltip (Stripe/Lago/Chargebee/Recurly converging UX pattern).

- [ ] Active sub detail page → stat card row shows primary label **"Renews <date>"** (the exclusive next-billing date — e.g. "Renews Jul 1, 2028") with a muted secondary line **"Period: <start> – <inclusive last day>"** (e.g. **"Period: Jun 1, 2028 – Jun 30, 2028"** — the last day actually covered, NOT the exclusive "Jul 1"; ADR-058 follow-up). The "Current period: <range>" pre-redesign labeling is gone.
- [ ] Details panel below shows **"Renews on"** row (exclusive boundary, "Jul 1, 2028") above **"Current period"** row (inclusive range, "Jun 1, 2028 – Jun 30, 2028") — both filled for active subs; the two differ by one day by design (the renewal fires ON the boundary; the period covers through the day before). The timeline "Period End" dot and the cost-dashboard cycle bar likewise show the inclusive last day.
- [ ] **"Billing alignment"** row (renamed from "Billing Time") shows `Calendar` or `Anniversary` with a `?` hover tooltip explaining: (a) alignment is set at activation; (b) calendar+monthly anchors to first-of-next-month at first cycle close; (c) **scheduled** plan-interval changes (`immediate=false`) preserve the existing day-of-month anchor at the boundary; (d) **immediate** cross-interval swaps (e.g. yearly → monthly with immediate=true) re-anchor the cycle on the swap day (see FLOW B21).
- [ ] Trialing subs: stat card shows trial-specific labels instead (no "Renews on" until trial ends).
- [ ] **Activity timeline chip: wall-clock primary + sim-effective subline (ADR-030 amendment, 2026-05-29)**: on a clock-pinned active sub's detail page, click any item-add/**plan-change**/cancel/etc. action. The new audit row in the Activity card shows: (a) primary timestamp = wall-clock (within seconds of the operator's click), (b) amber **test clock** chip next to the description (NOT "simulated"), (c) subline reading "Effect on test clock `<id>` at `<simulated time>`". Rows on a wall-clock (non-clock-pinned) sub show NO chip and NO subline.
- [ ] **Plan-change audit carries test-clock context on the global Audit Log too (2026-06-03)**: on a clock-pinned sub, change the plan, then open `/audit-log`. The `subscription.item_updated` ("Changed plan") row shows the **wall-clock** click time as its primary timestamp (today — NOT the test clock's far-future simulated date), an amber **test clock** chip, and the "Effect on test clock at `<simulated time>`" subline on expand.

## FLOW TIMELINE-ORDER: Activity timeline ordering (invoice + subscription)

Locks in the 2026-05-21 `sort.SliceStable` fix. On a test-clock-pinned sub, the inline cycle close → charge fail → dunning start cascade stamps three audit events at the EXACT same simulated instant.

- [ ] On invoice detail page activity for a clock-pinned in_arrears sub with a known-failed charge at cycle close: lifecycle events (Invoice created → Invoice finalized) render BEFORE dunning events (Automatic retry scheduled). Same-timestamp ties preserve insertion order, not random.
- [ ] Activity timeline detail timestamps (e.g. "Auto-resumes Jun 20, 2029" on Collection paused, "On Jun 30, 2029" on Cancellation scheduled, "New trial end: Jul 1, 2029" on Trial extended) render in **tenant TZ**, matching the row's main timestamp — NOT in UTC. Regression check for the 2026-05-21 `formatAuditTimestamp` UTC-format bug.

## FLOW SUB-REALIGN: Calendar-billing subs auto-realign anchor at cycle close

Locks in the 2026-05-21 fix: when a sub with `billing_time=calendar` has its period anchor drifted to mid-month (e.g., post-yearly→monthly plan swap preserves the yearly anniversary day-of-month, then monthly cycles preserved day-N indefinitely), the next cycle close MUST snap back to the next calendar boundary. Velox's stance partway between Stripe-flexible/Lago/Chargebee (always preserve day-of-month) and Stripe-legacy/Recurly (always re-anchor): honor the operator's configured `billing_time`.

**Setup** — reproducing the drifted-anchor state from scratch (the only realistic path; you can't directly create a calendar+monthly sub anchored on day-20 because calendar billing always snaps to day-1 at activation):

1. Create a test clock pinned at e.g. `2028-05-20 00:00 IST` (any non-month-start day works).
2. Attach a customer to the clock.
3. Create a yearly in_advance plan ($120/yr is fine for the math).
4. Create a sub on that customer + yearly plan + `billing_time=calendar` + `start_now=true`. Yearly plans always anchor on the activation day-of-month regardless of `billing_time` (no industry analog for "calendar yearly stub to Jan 1"), so the period anchors on day-20: `(May 20 2028, May 20 2029)`. Day-1 invoice for $120 fires.
5. Create a second monthly plan (e.g. $30/mo, in_advance).
6. From the subscription detail page → Change Plan dialog → pick the monthly plan + leave **"Apply at next period boundary"** selected (i.e., `immediate=false`). This stages the swap to fire at the yearly cycle close.
7. Advance the clock past `2029-05-20` (the yearly anniversary). Cycle close fires:
   - Pending plan applies → item now on monthly $30.
   - New period = `domain.NextBillingPeriodEnd(May 20 2029, calendar, monthly, IST)` = first-of-next-month = `Jun 1 2029`. So new period = `(May 20 2029, Jun 1 2029)` — **12-day calendar-snap stub**.
   - In_advance invoice: `$30 × 12/30 = $12.00` prorated (the `0d03ebd` fix). Line item description carries `prorated 12/30 days`.

If you skip steps 1-7 and want the drifted-but-not-yet-realigned shape (`(May 20, Jun 20)` mid-month period that hasn't seen its first cycle close yet), there's no direct UI path — would need a direct DB update which we explicitly avoid per `feedback_no_speculative_backfill`. The above setup gets you to the realistic post-swap state and immediately exercises the auto-realign.

**Assertions after the setup completes:**

- [ ] Sub's `current_billing_period` is `(May 20 2029, Jun 1 2029)` and `next_billing_at = Jun 1 2029` — verify in DB. period_start is the yearly anniv (= cycle-close instant); period_end is the calendar-snapped first-of-next-month.
- [ ] **In_advance stub proration** (the `0d03ebd` regression check): cycle-close invoice line item description reads `Mo - base fee (qty 1, prorated 12/30 days)`. Amount = `$30 × 12 / 30 = $12.00` (not the full $30).
- [ ] Advance clock past Jun 1 → next cycle close → new period = `(Jun 1, Jul 1)` full month. Now `next_billing_at = Jul 1`. In_advance invoice for $30 full (no proration; full cycle).
- [ ] Advance clock past Jul 1 → next cycle close → `(Jul 1, Aug 1)`. Day-1 anchored forever after.

**Anniversary negative guard** (repeat the setup with `billing_time=anniversary` at step 4):

- [ ] After step 7, period = `(May 20 2029, Jun 20 2029)` — day-of-month preserved, NO calendar snap. Full monthly $30 invoice (not prorated; 30-day full cycle from anchor).
- [ ] Advance past Jun 20 → `(Jun 20, Jul 20)`. Then `(Jul 20, Aug 20)`. Anniversary day-20 anchored forever.

## FLOW TC9: Pause collection auto-resume (via catchup)

End-to-end coverage of `pause_collection` with `resumes_at` auto-resume. The dashboard's Pause Collection dialog now exposes the "Auto-resume on" date input (Stripe-parity — Stripe's pause-collection modal has the same field). API path is still available for SDK callers.

- [ ] Setup: clock-pinned active sub. Open Pause Collection dialog → enter `Auto-resume on = frozen+7d` → confirm. Toast: "Collection paused — auto-resumes on the date you picked".
- [ ] Sub row in DB: `pause_collection_resumes_at = frozen+7d`, `pause_collection_behavior=keep_as_draft`.
- [ ] Advance clock through a cycle boundary while paused → invoice generated but stays DRAFT (no finalize, no charge, no dunning) — engine respects pause_collection.
- [ ] Advance clock past `resumes_at` → catchup Phase 0.7 auto-clears `pause_collection_*` columns AT `resumes_at` (not waiting for next cycle close); next cycle bills normally; previously-draft invoice can be finalized manually if intended.
- [ ] Activity timeline shows "Collection paused" (manual via dashboard) or "Collection paused by dunning" (when reached as a dunning final_action), with "Cycle keeps drafting; no charge until resumed" or "Auto-resumes …" detail line.
- [ ] Activity timeline shows "Collection auto-resumed — Scheduled resume date reached" for the schedule-driven resume (distinct from "Collection resumed" emitted by manual API call).
- [ ] API parity: `PUT /v1/subscriptions/{id}/pause-collection` with `{"behavior":"keep_as_draft","resumes_at":"..."}` produces the same sub state as the dialog path (resume is `DELETE` on the same route).

## FLOW TC10: Credit grant expiry firing (via catchup)

The ONLY end-to-end manual-test coverage of credit expiry actually firing. C1 verifies the grant is created with `expires_at` populated but doesn't exercise the cron's `ExpireCredits` job; test clock is the only practical way to verify the expiry mechanic.

- [ ] Setup: clock-pinned customer. Grant $50 credit with `expires_at = frozen+30d`.
- [ ] Customer credit balance = $50; ledger entry `entry_type=grant`.
- [ ] Advance clock past `expires_at` → catchup Phase 4 fires → new ledger entry `entry_type=expiry`, `amount_cents=-5000`, `created_at` in frozen-time. Balance back to $0.
- [ ] Customer detail page Credits tab shows both grant and expiry entries with frozen-time dates.
- [ ] Non-expiring grants (`expires_at IS NULL`) survive arbitrary advances — only expirable grants get expired.

---

## FLOW E: Email delivery (SMTP)

Single delivery path: when SMTP isn't configured every send returns
`ErrSMTPNotConfigured`. No stdout fallback. Local dev = Mailpit
(`docker compose up -d mailpit`, `SMTP_HOST=localhost:1025 SMTP_TLS=none`).

Boot warnings on startup (one each when var unset; never fatal):
- `SMTP NOT CONFIGURED`
- `HOSTED_INVOICE_BASE_URL NOT SET` — invoice / receipt / dunning / payment-failed CTAs render with no link
- `PAYMENT_UPDATE_URL NOT SET`
- `DASHBOARD_BASE_URL NOT SET` — password-reset emails won't send

- [ ] **E1 STARTTLS**: `SMTP_TLS=starttls SMTP_PORT=587` + creds. Trigger invoice email → `email_outbox` row pending → dispatched within seconds → recipient receives.
- [ ] **E2 Implicit TLS**: `SMTP_TLS=implicit SMTP_PORT=465`. Same expectation; verifies `tls.Dial` path.
- [ ] **E3 Not configured**: unset `SMTP_HOST`. Boot → `SMTP NOT CONFIGURED` warning. Trigger send → outbox claims, logs `ErrSMTPNotConfigured`, retries with backoff, lands in DLQ.
- [ ] **E4 5xx bounce**: send to `foo@invalid` → `customers.email_status='bounced'`, `email_bounce_reason` carries the SMTP error.
- [ ] **E5 Per-provider**: verify SendGrid / Postmark / SES / Mailgun / Resend per `docs/ops/email-setup.md`.
- [ ] **E6 Mailpit dev path**: `SMTP_HOST=localhost:1025 SMTP_TLS=none` → mail lands at http://localhost:8025 with HTML+text bodies.
- [ ] **E7 Receipt + payment-failed land via outbox (no detached goroutine)**: simulate `payment_intent.succeeded` for an invoice. Within ~100ms (before the dispatcher's 5s tick), `SELECT id, email_type, status FROM email_outbox WHERE email_type='payment_receipt' ORDER BY created_at DESC LIMIT 1;` returns a row with `status='pending'`. Wait 5-10s. Re-query: same row now `status='dispatched'`, receipt visible in Mailpit. Same for `payment_intent.payment_failed` → `email_type='payment_failed'`. **Determinism variant** (when timing is tight): from a separate psql session, `SELECT pg_advisory_lock(76540004);` to pause the email dispatcher; fire the webhook; confirm the row sits at `status='pending'` indefinitely; `SELECT pg_advisory_unlock(76540004);` and the row dispatches on the next tick.

## FLOW EX: Streaming CSV exports

- [ ] **EX1 customers**: `curl -OJ $API/v1/exports/customers.csv` → `customers-YYYYMMDD-HHMMSS.csv`. Date filter accepts BOTH RFC3339 (`?from=2026-01-01T00:00:00Z&to=2026-12-31T23:59:59Z`) AND bare YYYY-MM-DD (`?from=2026-01-01&to=2026-12-31` — `from` anchors at UTC 00:00:00, `to` at UTC 23:59:59 inclusive). Invalid `from` → 400 with field-level error. Same contract as the audit-log + usage endpoints via `internal/api/timefilter`. **Row-completeness check**: `wc -l customers-*.csv` minus 1 (header row) equals `SELECT COUNT(*) FROM customers WHERE tenant_id='<your tenant>' AND created_at BETWEEN from AND to`. Verify a tenant with >50 customers exports all of them.
- [ ] **EX2 invoices**: `$API/v1/exports/invoices.csv` → invoice rows incl. amounts, period, lifecycle timestamps. **Row-completeness check**: on a tenant with >100 invoices, `wc -l invoices-*.csv` matches `SELECT COUNT(*) FROM invoices WHERE tenant_id='<your tenant>'`.
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

## FLOW B2: Tax precision (NUMERIC(7,4), ADR-042/043)

Velox stores tax rates at 4-decimal precision (`tax_rate NUMERIC(7,4)`)
matching Stripe Tax's `percentage_decimal` shape. The legacy
`tax_rate_bp bigint` column was dropped in migration 0105 (ADR-043) — <!-- currency-ok: documents the dropped legacy column -->
`tax_rate` is the only rate storage. NYC 8.875%, Quebec 9.975%, Hawaii
4.7120% all round-trip exactly.

- [ ] Settings → Tax rate input accepts decimal percent directly (e.g. `8.875`). No bp dance.
- [ ] Manual provider: set tax 7.25% in Settings → `tenant_settings.tax_rate=7.2500` (no `tax_rate_bp` column exists). <!-- currency-ok: states the column was removed -->
- [ ] $100 subtotal at 7.25% → `invoices.tax_amount_cents=725, tax_rate=7.2500`.
- [ ] Manual provider precision: set tax `8.8750` in Settings, invoice a `$100.00` subtotal → `tax_amount_cents=888` (`$8.88`). Engine math is integer parts-per-million: `8.8750%` = `88750 ppm`, `tax = round(subtotal_cents × 88750 / 1_000_000)` = `round(10000 × 88750 / 1_000_000)` = `round(887.5)` = `888` (banker's round-half-to-even). The `1_000_000` is the ppm base, not a percent divisor. No float drift; the 4-decimal rate round-trips exactly.
- [ ] 3 line items $33.33+$33.33+$33.34 at 7.25%: `SUM(invoice_line_items.tax_amount_cents) = invoices.tax_amount_cents = 725` exactly, AND the residual lands by largest remainder, not on the last line (ADR-046). Exact per-line tax is 241.6425 / 241.6425 / 241.7150; the engine **floors each to 241¢ (sum 723¢), then hands the 2 leftover cents to the largest fractional remainders** — the $33.34 line (.7150) and one $33.33 line (.6425; ties → lowest index) → `242 / 241 / 242`. The $33.34 line must NOT be docked: no line with a larger `amount_cents` may carry a smaller `tax_amount_cents` than a smaller line.
- [ ] **Stripe-side high-precision case (NYC):** invoice an NY customer (10118 / Manhattan) with `stripe_tax` (needs an NY registration in the Stripe test dashboard) for `$100.00` → `invoice_line_items.tax_rate = 8.8750` + `tax_jurisdiction = US-NY`, `tax_amount_cents = 888`. (Stripe returns `percentage_decimal: "8.875"` in its document-level `tax_breakdown` and leaves the per-line breakdown null — seeded via `TestMapResult_DocLevelRateFallback`.)
- [ ] **Invoice-level rate is statutory, not effective (ADR-047):** the same invoice's `invoices.tax_rate = 8.8750` (statutory, from the lines) — NOT the effective `8.8800` (`888×100/10000`). Single-rate invoices store the real rate; only multi-jurisdiction invoices fall back to the blended effective rate.
- [ ] **Customer-facing display shows `8.875%`:** hosted invoice page and PDF tax line read `Sales Tax (8.875%)` (4-dp, trailing zeros trimmed via `formatTaxRate`), NOT `8.88%`; amount stays `$8.88`.
- [ ] **Tax name is a clean label, not the raw Stripe enum:** the tax line reads `Sales Tax`, NOT `sales_tax` (`invoices.tax_name = "Sales Tax"`; Stripe `tax_type` mapped via `taxTypeDisplayName` — `vat→VAT`, `gst→GST`, etc.).
- [ ] **Multi-line NY invoice ($40/$35/$25 = $100):** each line `tax_rate=8.8750` + `tax_jurisdiction=US-NY`; per-line `tax_amount_cents` 355/311/222 (Stripe verbatim) sum to `invoices.tax_amount_cents=888`; `invoices.tax_rate=8.8750`.
- [ ] **Proration math uses integer day-ratio (B7.4):** mid-cycle plan upgrade on a 30-day period with 18 days remaining → proration line item amount = `(new_amount - old_amount) × 18 / 30` exactly (banker's rounded). No `float64` ULP drift visible on amounts up to ~$36M.
- [ ] **Proration tax carries the provider and is committed (B7.5):** on a `stripe_tax` (or `manual`) tenant, the mid-cycle upgrade's proration invoice has `tax_amount_cents` computed on the prorated **net** (e.g. `1700 × 8.875% → 151`) AND a non-blank `tax_provider` matching the create invoice (`stripe_tax`) with a `tax_calculation_id` — NOT blank. For `stripe_tax`, the calculation is committed to a tax transaction (`tax_transactions` row / `invoices.tax_transaction_id` set), the same as a cycle invoice — so the proration tax is reported, not just charged.
- [ ] **Proration carries the reverse-charge / exempt status (2026-06-14):** on a customer with `tax_status=reverse_charge` (or `exempt`), the mid-cycle upgrade's proration invoice has `tax_amount_cents=0` AND `tax_reverse_charge=true` (or `tax_exempt_reason` populated), so the proration invoice's PDF shows the reverse-charge / exemption legend — same as the customer's cycle invoices. Pre-fix the proration invoice came out with `tax_reverse_charge=false` / blank reason, dropping the legend on that one invoice.
- [ ] **Exempt/zero-tax DOWNGRADE clawback — no tax slice, no upstream reversal (B7.6b):** on a `tax_status=exempt` (or `reverse_charge`) customer whose Pro prebill was $0 tax, downgrade Pro→Starter at 15/30. The clawback credit note is **net-only** — `total_cents == subtotal_cents` (e.g. $50.00), `tax_amount_cents=0` — and **no Stripe Tax reversal is filed** (the source invoice has no `tax_transaction_id`, so `ReverseTax` is skipped). Contrast B7.6's taxed source, which files a reversal. PAID source → $50.00 balance credit; UNPAID source → `amount_due` drops $50.00 and the balance is unchanged.
- [ ] **Clawback reverses what was CHARGED, not the current exemption status (B7.6c):** charge the Pro prebill while the customer is `standard` ($200 + 8.875% = $217.75, `tax_transaction_id` committed). THEN set the customer `tax_status=exempt`. THEN downgrade Pro→Starter at 15/30 on the paid prebill. The credit note is **$54.44 = net $50.01 + tax reversed $4.43** and a Stripe reversal **is** filed against the source transaction — the clawback grosses up by the *source invoice's* ratio (`FindBaseInvoiceForPeriod`), not by today's exempt status. The exempt flip only affects FUTURE invoices: the next Starter cycle invoice is $0 tax. (Trap: expecting $0 / no reversal "because the customer is exempt now" — wrong; reversal is anchored to the source.)

## FLOW B2b: Per-unit rate precision (decimal, ADR-045)

Per-unit pricing rates are decimal cents (Stripe `unit_amount_decimal` shape) so
sub-cent rates bill linearly. Fixed fees and invoice line amounts/totals stay
whole cents — only the RATE gains precision.

- [ ] Pricing → new flat rating rule, price `$0.000003` per unit → saves (input is not clamped to 2 decimals, not rounded to `$0.00`).
- [ ] Rule detail renders the sub-cent rate (`$0.000003`), not `$0.00`.
- [ ] `GET /v1/rating-rules/<id>` → `flat_amount_cents` is a JSON string (`"0.0003"`), not a number. It's a decimal per-unit rate **in cents** — `0.0003`¢/unit — which is what enables sub-cent linear pricing.
- [ ] Meter on that rule + customer with 1,000,000 usage units + cycle close → usage line `amount_cents=300` ($3.00) exactly — i.e. `0.0003`¢/unit × 1,000,000 units = `300`¢ (not `0`, which is what rounding the rate to int cents would give; not `300000000`, which is billing `300`¢ *per unit*).
- [ ] **Unit Price column shows the full-precision rate, not `$0.00` (ADR-054)**: that usage line's Unit Price reads the effective rate (`$0.000003`), not `$0.00`, on the invoice detail page, the PDF, and the public hosted page — `GET /v1/invoices/{id}` line carries `unit_amount_decimal` (e.g. `"0.0003"` cents) alongside the whole-cent `unit_amount_cents`. Quantity × Unit Price reconciles with Amount. (Try the screenshot case too: 1,000 units billed `$3.00` → Unit Price `$0.003`, not `$0.00`.)
- [ ] Instantiate `anthropic_style` → `c35_sonnet_input` stored as `0.0003` cents/token; 1,000,000 input tokens bill `300`¢, not `$3,000,000`.

## FLOW B3: Idempotency

- [ ] Run billing twice in same period → no duplicate invoice. Logs `invoice already exists for billing period (idempotent skip)`.
- [ ] **Multi-add proration in same period (ADR-030 cross-flow audit, migration 0101)**: pick a clock-pinned active sub. AddItem with plan A — succeeds, proration invoice DEMO-NNNN created. AddItem with plan B at the same simulated instant — also succeeds, distinct proration invoice DEMO-NNNN+1 created with the same `billing_period_start/end` as the first proration invoice but different `source_subscription_item_id`. `idx_invoices_billing_idempotency` correctly exempts both (predicate `WHERE source_plan_changed_at IS NULL`); `idx_invoices_proration_dedup` correctly distinguishes them by item id.
- [ ] **AddItem atomicity (ADR-030 atomic-proration follow-through, 2026-05-29)**: simulate a proration write failure (e.g. temporarily kill the database after `BeginTx` succeeds but before the invoice insert — easiest reproduction: temporarily `REVOKE INSERT ON invoices FROM velox_app` — the **runtime** role the app connects as (`cmd/velox` swaps `DATABASE_URL`→`velox_app`); revoking from `velox` is a no-op because it's a superuser and bypasses table privileges — run AddItem on a clock-pinned active sub **whose current-period prebill is already settled** (an unpaid in_advance prebill triggers the B7.8 `unpaid_invoice_blocks_change` 409 *before* the proration insert — settle or void it first), then `GRANT INSERT ON invoices TO velox_app`). Expected (atomic path, `h.db` wired — production default): a generic **500 `internal_error`** AND `SELECT * FROM subscription_items WHERE subscription_id='<sub>' AND plan_id='<new plan>'` returns **ZERO rows** — the item add + proration roll back together. NOTE: `proration_failed` is the **non-atomic fallback** code ("item add succeeded but proration generation failed — item is on the subscription"), used only when the atomic coordinator isn't wired; there the item STAYS on the sub, so it is NOT the atomic-rollback outcome. (Verified 2026-06-20: live atomic path → 500 `internal_error` + zero orphan.)
- [ ] **UpdateItem + RemoveItem atomicity (same ADR-030 follow-through)**: same shape, different mutations. UpdateItem quantity-INCREASE with simulated proration-write failure (`REVOKE INSERT ON invoices FROM velox_app`) → generic **500 `internal_error`** + item.quantity rolled back to the pre-change value. **Cross-interval plan swap is now atomic too (ADR-056)** — the plan write + watermark advance + new-period invoice commit in one tx; a simulated failure on the new-period bill rolls back the plan write AND the watermark (see FLOW B21).
- [ ] **Downgrade/removal clawback is atomic + recoverable (ADR-057, migration 0121)**: a downgrade / qty-decrease / RemoveItem on a PAID source issues a tax-reversing **clawback credit note**, created as a **DRAFT inside the item-change tx** (`issue_pending`) and issued post-commit. **Atomicity:** `REVOKE INSERT ON credit_notes FROM velox_app` (the clawback writes `credit_notes`, NOT `customer_credit_ledger`), RemoveItem a paid-source item → the item is **still present** (the draft-create failure rolls the delete back), not deleted-with-no-credit; `GRANT` back, retry → succeeds. **Recovery (Issue-never-ran):** if instead the *post-commit* `Issue()` never runs (kill the process right after commit), the draft stays `status='draft' AND issue_pending=true` and `RetryPendingClawbackIssue` (scheduler tick) re-issues it on the next tick. **Known gap (not recovered):** a *partial* issue — `Issue()` already flipped the row to `status='issued'` but a later side-effect failed — is invisible to the reconciler's `status='draft'` scan; it surfaces only via a loud `ERROR` log (`grep "issue failed post-commit"`) and is manual to reconcile (tracked follow-up). (Pre-fix the clawback was created+issued post-commit fire-and-forget: any failure removed the item but left the customer un-credited with only a "reconcile manually" log.)
- [ ] **RemoveItem soft-delete (migration 0102, 2026-05-29)**: pick an item that has at least one proration invoice or credit-ledger entry pointing at it (e.g. an item added mid-cycle with proration emitted). DELETE the item via `/v1/subscriptions/{id}/items/{itemID}` → 200 (NOT 500 with the FK-violation error). `GET /v1/subscriptions/{id}` shows the item gone from `sub.Items`. `psql -c "SELECT id, deleted_at FROM subscription_items WHERE id='<the id>'"` returns one row with `deleted_at IS NOT NULL`. Re-adding the same `plan_id` to the same sub succeeds (the partial unique index `WHERE deleted_at IS NULL` allows it).

## FLOW B4: Auto-charge retry

- [ ] Decline-on-charge card → invoice has `auto_charge_pending=true`, `payment_status=pending`.
- [ ] Update card → next scheduler tick → `payment_status=succeeded`, `auto_charge_pending=false`.

## FLOW B5: Idempotency-Key header

- [ ] POST with `Idempotency-Key: test-123` → 201.
- [ ] Same body + key → same response, 1 row.
- [ ] Same key + different body → 409.

## FLOW B6: Subscription lifecycle

- [ ] Trial 7 days → no charge during trial; status flips to active AT `trial_end_at` (Phase 0.5 / cron, ADR-037); first invoice fires at activation for in_advance items or at the post-trial cycle close for in_arrears. Full coverage in FLOW TC6.
- [ ] Pause button on a `status=active` sub → opens **Pause collection** confirm dialog (the hard-pause radio option was removed in PR-6). Click through → cycle keeps drafting invoices, auto-charge is suppressed, no dunning fires on the resulting drafts. Resume collection → next cycle bills normally; drafts stay drafts unless operator finalizes them manually.
- [ ] Pause Collection confirm dialog description includes the bolded line **"On resume, the full current period bills — paused days are not pro-rated. Issue a credit grant after resuming if you want to offset them."** (truth-in-labelling fix shipped 2026-05-18; pause_collection is about charging, not about cycle-skip — full month bills on resume).
- [ ] Cancel from `status=trialing` works (PR-1.5 — Stripe parity) — `trial_end_at` is preserved across cancel for historical reporting, `canceled_at` stamps in simulated time on clock-pinned subs (PR-1).
- [ ] Cancel on `in_arrears` plan → confirm dialog → status canceled, no future billing, no credit grant.
- [ ] Cancel on `in_advance` plan mid-period → confirm dialog → status canceled AND a credit grant lands on the customer's balance for the unused portion of the already-billed period. Description: `Cancel proration — unused portion of <sub_code> base fee (period <start> to <end>, canceled <date>)`. See B17 for the full flow.
- [ ] Cancel on `in_advance` plan AT or AFTER `current_period_end` → no proration credit (period was used in full).

## FLOW B7: Plan change + proration

- [ ] In_arrears sub upgrade immediately → no immediate proration invoice/credit; cycle close emits per-segment lines (FLOW B20).
- [ ] In_arrears sub downgrade immediately → no immediate credit grant; cycle close emits per-segment lines.
- [ ] In_advance sub upgrade immediately + source invoice paid → immediate proration invoice for the delta lands in customer's invoices, with `auto_charge_pending=true`. It **auto-collects** like a cycle invoice — a customer with a saved card is charged on the next scheduler tick (wall-clock subs) or the next clock **Advance** (clock-pinned subs); it does NOT sit at `pending` forever waiting for a manual Collect Payment. (2026-06-11 fix — proration invoices used to finalize with `auto_charge_pending=false` and never charge.)
- [ ] **Upgrade invoices as TWO lines (B7.7, ADR-048 Phase C, 2026-06-06):** the upgrade proration invoice shows a **negative** credit line *"Unused time on Starter (after &lt;date&gt;)"* and a **positive** charge line *"Remaining time on Pro (after &lt;date&gt;)"* — NOT one net line. Amount due equals the prior single-line net. For the 18/30 Starter→Pro example: credit **−$12.00** (`2000×18÷30`) + charge **+$30.00** (`5000×18÷30`) = **$18.00** net. On a taxed sub each line carries its own tax (the credit's is the negative reversed slice); the two per-line taxes sum to the invoice tax and the dashboard/PDF totals are unchanged. Item-add still shows a single net line.
- [ ] **Quantity increases as TWO lines (2026-06-12):** raise a $10.00/seat item from 1 → 3 seats at exactly half period. The proration invoice shows *"Unused time on 1 × Seat (after &lt;date&gt;)"* **−$5.00** (qty 1, unit −$5.00) and *"Remaining time on 3 × Seat (after &lt;date&gt;)"* **+$15.00** (qty 3, unit **$5.00** — the true prorated per-seat rate), net **$10.00**. On BOTH lines qty × unit price = amount exactly.
- [ ] **The plan-change proration invoice carries the "Simulated" badge on a test clock (2026-06-09):** on a clock-pinned in_advance sub, advance the clock and upgrade the plan immediately. The resulting proration invoice — detail header (next to the status pill) and its Invoices-list row — shows the amber **Simulated** badge, exactly like the sibling cycle invoice on the same sub (`invoices.is_simulated = true` in DB).
- [ ] **Exact integer day-ratio amount (ADR-042)**: on a clock-pinned in_advance sub whose 30-day period (Jun 1 → Jul 1) base invoice is paid, advance the clock to Jun 13 (18 of 30 days remain), then immediately upgrade Starter ($20.00) → Pro ($50.00). The proration invoice's amount due is **exactly $18.00** — `(5000−2000)×18 ÷ 30 = 1800`¢, banker's-rounded, no float drift (rendered as the −$12.00 / +$30.00 two-line pair, B7.7). With Pro at $50.01 the net is **$18.01** (`3001×18 ÷ 30 = 1800.6`, rounds up); downgrading Pro → Starter at 18/30 yields a **−$18.00** credit (not an invoice).
- [ ] **Stub period prorates against the FULL cycle, not the partial period (2026-06-06).** Sign a customer up **mid-month** so the first period is a stub (e.g. start Apr 17 → period Apr 17–May 1, a 14-day stub of a 30-day monthly cycle); pay the day-1 invoice (it shows "prorated 14/30 days"). Advance ~1 day in (13 of the stub's days remain) and immediately upgrade Starter ($20) → Pro ($50). The proration invoice is **$13.00** — `(5000−2000)×13 ÷ 30`, the full 30-day cycle — **not** $27.86 (`×13/14`, the stub length). Regression for the DEMO-012094 over-charge: the upgrade denominator must match the day-1 stub's `/30`.
- [ ] **In_advance + source invoice UNPAID → charge blocked, credit adjusts the open invoice (B7.8, ADR-050, 2026-06-08).** On a clock-pinned in_advance sub whose current-period prebill is finalized but **unpaid** (no PM / declined): an immediate **upgrade** (or add-item / qty-increase) is **rejected with 409** (`unpaid_invoice_blocks_change` — "settle or void the outstanding invoice first") and the item is left unchanged (no second receivable stacked). An immediate **downgrade** (or removal / qty-decrease) **proceeds** and issues a tax-reversing **adjustment credit note** against the unpaid prebill, dropping its `amount_due` by the unused gross (capped at amount due) — the change response shows `proration.type=adjustment`, and the customer credit balance is **unchanged** (no refundable credit; nothing was funded). Contrast a **paid** source: upgrade invoices the delta / downgrade credits the balance (B7.6/B7.7). Mirrors the unpaid-cancel relief (TC8b).
- [ ] **Downgrade on a TAXED paid prebill reverses proportional tax (B7.6, ADR-048, 2026-06-06):** repeat the 18/30 downgrade Pro → Starter on a `stripe_tax`/`manual` tenant whose Pro prebill was taxed (net $50 + 10% = $55 paid). The clawback is issued as a **credit note** against the paid source invoice — the customer's balance is credited the **gross** unused (net $18.00 grossed up by the source invoice's `Total/Subtotal` ratio = **$19.80**), and the credit note carries the proportional reversed tax ($1.80; `stripe_tax` files a reversal against the source `tax_transaction`). The change response shows `proration.type=credit` with `amount_cents` = the gross. Same shape for a **quantity decrease** and an **item removal**. Zero-tax prebills are unchanged (gross == net, still via the credit note).
- [ ] Scheduled plan change (`immediate=false`) → no immediate artifact; engine emits closing invoice under OUTGOING plan at period boundary (FLOW B20).
- [ ] Plan change across `base_bill_timing` rejected with 422 (`bill_timing change is not supported on plan-swap`) — both immediate and scheduled. Velox rejects this for billing-safety — an in-place advance↔arrears swap mid-cycle creates prepay/postpay overlap or refund-then-recharge edge cases. Zuora rejects it the same way; Stripe ALLOWS it in-place (bills the new price forward, offloading the overlap to the operator). Operator path: cancel + recreate.
- [ ] Immediate same-cadence cross-interval plan-swap (yearly → monthly or monthly → yearly, both in_advance OR both in_arrears) accepted — see FLOW B21.
- [ ] Plan billing-fields immutability (ADR-034): live-sub plans reject `PATCH` to `base_amount_cents` / `base_bill_timing` / `meter_ids` with 422; `name` / `description` / `tax_code` / `status` mutate cleanly.
- [ ] **Plan billing-fields immutability (ADR-034)**: with at least one live sub on a plan, `PATCH /v1/plans/{id}` with a different `base_amount_cents`, `base_bill_timing`, or `meter_ids` → **422** with message naming the blocked field(s) + live-sub count + "Create a new plan instead." Display-only fields (`name`, `description`, `tax_code`, `status`) STILL mutate cleanly on the same call. On a plan with zero live subs, all fields are mutable (covers typo correction at plan creation). Canceled / archived subs do NOT count as live for the guard.

## FLOW B8: Usage caps

- [ ] `usage_cap_units=5000`, `overage_action=block`, ingest 8000 → billed 5000.
- [ ] Switch to `overage_action=charge`, ingest 8000 → billed 8000.
- [ ] **Fractional cap-scaled quantity keeps its exact decimal on the line (2026-06-06)**: when a multi-meter cap scales a meter to a fractional quantity (e.g. 1.5 units), the usage line's `quantity_decimal` is the exact `1.5` (`GET /v1/invoices/{id}` and the PDF show `1.5`, not truncated `1`); `amount_cents` is unchanged.

## FLOW B9: Customer price overrides

- [ ] POST /v1/price-overrides → that customer's invoice uses override price.
- [ ] Other customers → default rule price.

## FLOW B10: Manual tax + customer tax status

Manual provider applies one flat tenant rate to every customer regardless of country (the old `tax_home_country` / cross-border zero-rating model was dropped — ADR-038). Exemption is driven solely by the customer's `tax_status` (`standard` / `exempt` / `reverse_charge`). Rate precision is covered by B2. <!-- currency-ok: documents the dropped tax_home_country model -->

- [ ] Settings → set tax rate `18` + tax name `IGST` (`tenant_settings.tax_rate=18.0000`; no `tax_rate_bp` / `tax_home_country` columns exist). <!-- currency-ok: states the columns were removed -->
- [ ] Any `standard` customer, any country: $100 → `tax_amount_cents=1800`, `tax_name=IGST`, PDF tax line `IGST (18%)` (decimal `%.4g`).
- [ ] Customer `tax_status=exempt` → $0 tax, invoice `tax_reason='customer_exempt'`, PDF carries the customer-exemption legend.
- [ ] Customer `tax_status=reverse_charge` (India B2B): $0 tax; PDF carries supplier GSTIN under the company line + "Tax payable on reverse charge basis: YES".
- [ ] EU `reverse_charge` customer → $0 tax, PDF retains the EU reverse-charge wording.
- [ ] **Exemption inputs are enforced (2026-06-14):** PUT a billing profile with `tax_status=exempt` and no `tax_exempt_reason` → **400** (`tax_exempt_reason` required). `tax_status=reverse_charge` with no `tax_id` → **400** (buyer `tax_id` required). Both save once the field is supplied. (Direct-API guard; the dashboard already blocks these.)
- [ ] **Country is validated to ISO alpha-2 (2026-06-14):** PUT a billing profile with `country="USA"` → **400** (must be alpha-2). `country=" us "` saves and stores as `US`. (Direct-API guard; the dashboard country picker already sends a valid code.)
- [ ] **`tax_provider=none` still renders the legend:** on a `none` tenant, a `reverse_charge` (or `exempt`) customer's invoice is $0 AND carries the reverse-charge / exemption legend (not a bare $0 with no notice).
- [ ] **Domestic reverse charge is flagged, not silently zero-rated (2026-06-15, ADR-052):** with tenant `company_country=DE`, set a customer in `DE` to `tax_status=reverse_charge` and generate an invoice → invoice is still $0 (override honored) BUT the API log carries a WARN `tax: domestic reverse charge — buyer is in the seller's registration country …`. A customer in `FR` (cross-border) logs nothing. Edit billing profile UI: selecting **Reverse charge** shows the help "prefer Standard + a Tax ID … never applies to a buyer in your own country"; selecting **Standard** shows "add their Tax ID below — reverse charge is applied automatically (cross-border)".
- [ ] Stripe-Tax path: `taxability_reason=not_collecting` round-trips → line item `tax_reason='not_collecting'`, badge in dashboard.

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
- [ ] Replay same idempotency_key → 409 Conflict (`invalid_request_error`), no duplicate (the duplicate-key error propagates; there's no fetch-original-on-replay).
- [ ] Rule with `dimension_match={"token_type":"input"}` claims only input events; `{"token_type":"cache_read"}` claims only cache-read events. Token roles are DISJOINT (ADR-044), so each `{model, token_type}` matches exactly one rule — no priority tie-break needed (the old boolean `cached` + priority-wins model is gone).
- [ ] Aggregations sum / count / max / last_during_period / last_ever all bill correctly. Switching aggregation between cycles re-bills next cycle without affecting past invoices.
- [ ] `cmd/velox-bench` sustains 50k events/sec on local Postgres.

## FLOW B14: Billing thresholds

- [ ] PATCH sub `billing_thresholds:[{subscription_item_id, usage_gte:10000}]` (per-item, keyed on `subscription_item_id` — not `meter_id`). Ingest 9999 → no early finalize. Ingest 1 more → invoice auto-finalized within 1 tick, `billing_reason="threshold"`. New events start a new period.
- [ ] PATCH `{amount_gte:50000}` → cross $500 → same shape.
- [ ] Cross threshold + immediately `POST /v1/billing/run` → idempotent skip.
- [ ] Subscription detail "Spend Thresholds" card: empty state with Set button. Edit dialog has subtotal cap, reset_billing_cycle checkbox, per-item rows. Save shows `$1,000.00` (from cents) and `≥10000.5 units`. Clear thresholds → flips to empty.
- [ ] **Threshold invoice on a test clock carries the Simulated badge (2026-06-12):** pin a sub with an amount threshold to a test clock, advance until the cap crosses → the threshold invoice shows the **Simulated** badge (is_simulated=true), same as sibling cycle invoices on the clock.
- [ ] **`reset_billing_cycle=false` cycle close bills only the residual (2026-06-13):** set `{amount_gte, reset_billing_cycle:false}`, cross the cap mid-cycle (threshold invoice fires), keep ingesting, then advance to period end → the cycle invoice has NO base-fee line and its usage line's period starts at the threshold invoice's `billing_period_end` (only post-fire usage). Sum of the two invoices == what one un-thresholded invoice would have been.
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
  - Base line: $99, `billing_period_start/end = next period`, sub-line "Covers <next period>" (date range only — no "(in advance)" parenthetical)
  - Usage line: $10 (1,000 × $0.01), `billing_period_start/end = elapsed period` (matches invoice header — sub-line suppressed)
  - Single invoice carries both — no separate invoice for the upcoming base.
- [ ] Tax applies to both lines; per-line `tax_amount_cents` populated.
- [ ] Auto-charge fires once for the combined total.

## FLOW B16b: token usage billed on immediate cancel (ADR-044 cancel path, 2026-06-12)

- [ ] Setup: sub on a pure-usage plan with the multi-dim `tokens` meter (per-`{model, token_type}` pricing rules, e.g. the claude-3.5-sonnet recipe — meter has NO direct rating-rule binding). Ingest input + output token usage mid-period.
- [ ] Cancel immediately → a final invoice IS emitted with `billing_reason=subscription_cancel`, one usage line per claimed rule (`… - canceled mid-period`), priced at the recipe's decimal rates.
- [ ] Each usage line carries `quantity_decimal`; line amounts match what the same usage would bill at cycle close.

## FLOW B17: `in_advance` cancel proration credit

- [ ] Setup: customer with PM, sub on `pro-advance` ($49/mo), day-1 invoice **paid** (B15).
- [ ] Cancel mid-period (e.g. day 15 of a 30-day period) → status `canceled`.
- [ ] Customer Detail → Credits tab → new ledger entry:
  - `entry_type = grant`
  - `amount_cents=2450` (≈ $24.50 — half a $49 cycle, 15/30 days)
  - Description: `Cancel proration — unused portion of <sub_code> base fee (period <start> to <end>, canceled <today>)`
- [ ] Cancel at or after `current_period_end` → no proration credit (period was used in full).
- [ ] Cancel on a pure `in_arrears` sub → no proration credit (nothing was prebilled).
- [ ] Mixed sub (one in_advance item + one in_arrears item) → credit covers only the in_advance item's unused portion.
- [ ] Future invoices on this customer auto-apply the credit (C1 behavior).
- [ ] **Cash back to card instead of balance credit (B2B default is balance credit):** cancel grants a *balance* credit, not a card refund. To return cash, issue a credit note on the paid source invoice with **Refund to card** (the CN refund channel) → Stripe refund processed, customer balance NOT credited. This is the deliberate two-step: cancel credits the balance; refunding cash is a separate operator action.
- [ ] **Source invoice unpaid → invoice settled, not credited (#22, ADR-031 amendment):** repeat the setup but DON'T pay the day-1 invoice (`payment_status='pending'`). Cancel mid-period → status `canceled`, **NO credit ledger entry** (no cash was funded), and the unpaid invoice is settled down to the consumed portion: partially-consumed → an **adjustment credit note reduces `amount_due`** (log `cancel: reduced unpaid prebill to consumed portion`); cancel before any consumption → invoice **voided** (log `cancel: voided fully-unused unpaid prebill`). It does NOT ride dunning for the full amount. Full coverage in FLOW TC8b.
- [ ] **Plans > ~$36 base** (regression check): cancel a $59 in_advance sub mid-period (e.g., day 7 of 30-day cycle). Credit grant MUST be non-zero — `5900 × 23 / 30 = 4523 cents = $45.23`.

## FLOW B17b: upgrade then cancel — credit fans across both funding invoices (2026-06-15)

- [ ] Setup: in_advance $100/mo sub, day-1 invoice paid → upgrade to $200/mo mid-period (immediate, with proration) → second proration invoice created and paid → cancel immediately (~23 of 30 days unused).
- [ ] Customer Detail → Credit Balance increases by the FULL unused prepayment (≈ `$200 × 23/30 = $153.33`), NOT $0 and NOT clamped to the $100 day-1 invoice.
- [ ] Credit Notes page shows **two** credit notes for the cancel — one against the $100 base invoice, one against the upgrade proration invoice — each within its own invoice's total; their sum equals the balance credit.
- [ ] On a taxed sub, each credit note reverses its own invoice's proportional tax (`↳ Tax reversed`).
- [ ] Server log shows `cancel proration credit issued … funding_invoices=2` and NO `slog.Error "customer not credited"`.

## FLOW B17c: downgrade after upgrade — clawback reverses the upgrade invoice (LIFO) (2026-06-15)

- [ ] Setup: in_advance sub, day-1 invoice paid → upgrade mid-period (second proration invoice, paid) → then **downgrade** the plan.
- [ ] The downgrade clawback issues its credit note against the **upgrade** (most-recent) invoice, not the day-1 base invoice — reversing that invoice's own tax; the base plan you're keeping is untouched.
- [ ] If the clawback exceeds the upgrade invoice's remaining room, it spills onto the base invoice (a second credit note); it never silently drops or loud-fails on a single invoice's cap.
- [ ] **Downgrade then cancel** (no double-credit): after the downgrade above, cancel. The cancel credit accounts for the headroom the downgrade already consumed (server log `funding_invoices=…`), and total credited across both events never exceeds what was paid.
- [ ] **Quantity decrease** (3→1 seats) on a sub funded by two invoices → the clawback splits **proportionally** across funding invoices (not LIFO), each reversing its own tax.

## FLOW B18: Meter Detail page

- [ ] Default rule card renders the latest version of the linked rating rule (edit rule → version badge bumps).
- [ ] Add dimension-matched rule: k=v rows, priority, rating-rule select → save → table refetches in priority order.
- [ ] Dimension value coercion: `true/false` → bool, numeric strings → number, else string.
- [ ] Per-row delete: typed `delete` confirm; already-finalized invoices unaffected.

## FLOW B19: Cancel-flow billing artifacts (PR-9 + PR-10)

- [ ] **Mid-period immediate cancel, `in_arrears` plan (PR-10):** sub `in_arrears` $100/mo created Nov 1, customer logs 50 usage events Nov 1–15, operator clicks Cancel Nov 15 (mid-period). Result: final invoice with `billing_reason='subscription_cancel'`, `billing_period_start=Nov 1`, `billing_period_end=Nov 15`, lines = prorated base for the elapsed Nov 1–15 = 14 days (`$100 × 14/30 ≈ $46.67`) + usage line (50 × $1 = $50). Total $96.67.
- [ ] **Mid-period immediate cancel, `in_advance` plan (PR-10):** sub `in_advance` $100/mo, day-1 invoice paid (B15), 50 usage events Nov 1–15, Cancel Nov 15. Result: TWO artifacts — (a) final invoice `billing_reason='subscription_cancel'` with usage line only (no base — already paid), total $50; (b) credit grant for the unused 16 of 30 days (`$100 × 16/30 ≈ $53.33`) (B17 unchanged). Independent: invoice doesn't pre-apply the credit.
- [ ] **Clean cancel at-or-after period_end:** Cancel Nov 30 with current_period_end=Dec 1 → BillFinalOnImmediateCancel no-op. The cycle close already billed (or will bill) the period; no second final invoice fires. Credit grant also no-op for in_advance (clean cancel, period used in full).
- [ ] **Scheduled cancel at period_end on `in_advance` (PR-9):** sub `in_advance` $100/mo, operator `schedule-cancel at_period_end=true` mid-Nov. At Dec 1 cycle close: cycle-close invoice contains **NO upcoming-period base line** ($100 NOT charged for Dec 1–Jan 1 that won't be used). Usage line for Nov 1–Dec 1 still bills normally. Then scheduled cancel fires; sub.status=canceled.
- [ ] **Scheduled cancel at period_end on `in_arrears`:** same setup with in_arrears plan. Cycle-close invoice for Nov 1–Dec 1 has full base ($100) + usage (correct — customer consumed the just-elapsed period). Cancel fires after. No overcharge — in_arrears was never affected by PR-9.

## FLOW B20: Segment-aware billing at cycle close (Lago / Orb shape)

- [ ] **Mid-period plan swap (in_arrears, same interval):** sub on Plan A ($30/mo in_arrears), day 15 of March operator UpdateItem to Plan B ($60/mo in_arrears, immediate=true). At April 1 cycle close: invoice has **two base-fee lines** — `Plan A - base fee (qty 1, prorated 14/31 days)` ≈ $13.55 + `Plan B - base fee (qty 1, prorated 17/31 days)` ≈ $32.90. Total ≈ $46.45 (vs. pre-segment $60 or pre-defer $85).
- [ ] **Mid-period plan swap, different meter sets:** Plan A has meter X only ($0.05/unit), Plan B has meter Y only ($0.10/unit). Swap day 15. Ingest 100 units of X in [Mar 1, Mar 15) and 50 units of Y in [Mar 15, Apr 1). Cycle close invoice has TWO usage lines: `Meter X (unit)` with `billing_period_start=Mar 1, billing_period_end=Mar 15`, $5.00; `Meter Y (unit)` with `billing_period_start=Mar 15, billing_period_end=Apr 1`, $5.00. Total usage $10.
- [ ] **No mid-period changes (no-regress):** sub with no UpdateItem / AddItem / RemoveItem during the cycle → cycle-close invoice has exactly one base-fee line at the current plan, billing_period equals the full period.
- [ ] **Scheduled plan-swap at boundary:** schedule UpdateItem with `immediate=false` from Plan A ($30) to Plan B ($60). At cycle close: invoice has **one** base-fee line at Plan A's $30 (not $60). New plan B takes effect for the next cycle.
- [ ] **Mid-period item add:** sub with one item, AddItem on day 15. Cycle close: first item's full-period line + added item's segment line from day 15 to period_end. Added item's `billing_period_start` on the line equals add-time, NOT period_start.
- [ ] **Mid-period item remove (in_arrears):** sub with one item-A + one item-B, RemoveItem on B day 15. Cycle close: item-A full-period line + item-B segment line from period_start to day 15. No credit grant emitted (in_arrears removed item was never prepaid).
- [ ] **Immediate cancel after mid-period plan swap:** UpdateItem Plan A → Plan B day 10, Cancel day 20. `BillFinalOnImmediateCancel` invoice has TWO segment base lines: Plan A for [day 1, day 10) + Plan B for [day 10, day 20). Plus usage lines per (meter, segment).

## FLOW B21: Immediate same-cadence cross-interval plan-swap (yearly ↔ monthly)

Velox accepts `immediate=true` plan-swaps that change the billing interval as long as bill_timing matches on both sides (in_advance↔in_advance or in_arrears↔in_arrears). Same-cadence cross-interval swaps are industry-standard (Stripe / Lago / Orb ship them). Cross-cadence (in_advance↔in_arrears) is rejected by Velox for billing-safety (prepay/postpay overlap); Zuora rejects it the same way, while Stripe ALLOWS it in-place (bills new forward). Operator path is cancel + recreate.

- [ ] **In_advance yearly → monthly downgrade (same cadence, cross interval):** clock-pinned sub on `pro-yearly-adv` ($1200/yr in_advance), day-1 invoice paid. On day 90 of the year, `UpdateItem(new_plan_id=pro-monthly-adv, immediate=true)`. Three artifacts appear within the same tick:
  1. Credit ledger entry: `Plan-swap refund — unused portion of <code> base fee (period <start> to <end>, swapped <today>)`, amount = `$1200 × (365 − 90)/365` = `$1200 × 275/365 ≈ $904.11` (275 days unused of 365).
  2. Subscription's `current_period_start` = today; `current_period_end` = `NextBillingPeriodEnd(today, billing_time, monthly)` (anniversary: today + 1 month; calendar: first-of-next-month).
  3. New invoice for the new in_advance period at the monthly $100 base (stub-prorated if calendar snap shortens it).
- [ ] **In_advance monthly → yearly upgrade (same cadence, cross interval):** sub on `pro-monthly-adv` ($100/mo in_advance), day-1 invoice paid. On day 15 of a 30-day cycle, swap to `pro-yearly-adv` ($1200/yr). Refund credit `$100 × 15/30 = $50`; period jumps to (today, today + 1 year); new $1200 invoice.
- [ ] **In_arrears yearly → monthly (same cadence, cross interval):** sub on `pro-yearly-arr` ($1200/yr in_arrears). On day 90 swap to `pro-monthly-arr` ($100/mo in_arrears, immediate=true). No immediate invoice or credit. `current_period_end` truncated to today; `next_billing_at = today`. On next scheduler tick / test-clock Advance, closing invoice fires under OLD yearly plan at `$1200 × 90/365 ≈ $295.89`, then a new period (today, today + 1 month) opens under the new monthly plan.
- [ ] **In_arrears monthly → yearly (same cadence, cross interval):** symmetric. Closing invoice on next tick at OLD monthly rate × days-elapsed proration; new yearly period opens.
- [ ] **Cross-cadence REJECTED (both directions, both immediate and scheduled):** swap from any in_advance plan to any in_arrears plan (or vice versa) → 422 (`bill_timing change is not supported on plan-swap (current X, new Y); cancel the subscription and start a new one with the target plan`). Velox rejects cross-cadence for billing-safety (matches Zuora's hard rejection); Stripe permits in-place cadence changes but offloads the prepay/postpay overlap to the operator. Lago — the closest model to Velox (per-plan `pay_in_advance`) — documents same-cadence transitions only.
- [ ] **Paid-check gate (NEW in_advance OR cross-cadence with OLD in_advance):** swap on an in_advance sub whose source invoice is `payment_status='pending'`. No credit grant; server log `plan-swap refund: source in_advance invoice not paid; skipping credit grant`. The plan swap + period jump/truncate still happen; operator can manually issue a credit grant from the dashboard.
- [ ] **Same-interval same-cadence swap (no regression):** swap monthly $29 → monthly $49 immediately (both in_arrears). Existing segment-aware behavior — no credit grant, no period jump, no immediate invoice. Cycle close emits per-segment lines (FLOW B20).
- [ ] **Atomic — no silent revenue drop on a failed new-period bill (ADR-056):** in_advance cross-interval swap (e.g. yearly → monthly) while forcing the new-period invoice insert to fail (temporarily revoke INSERT on `invoices` for the velox role, run the immediate swap, restore the grant). Expected: the API call **fails loudly** (500), and the swap **fully rolls back** — `SELECT plan_id, current_billing_period_start, current_billing_period_end, next_billing_at FROM subscriptions/items` show the **pre-swap** plan + period (the watermark did NOT advance, and no orphaned new-period invoice exists). Pre-ADR-056 the watermark advanced first and the failure was swallowed with a false "scheduler catchup will retry" log, permanently dropping the new period's base. The OLD-period refund + the new invoice's tax-commit/auto-charge run only after the tx commits.

---

## Pricing Recipes

## FLOW R1: List + preview

- [ ] `GET /v1/recipes` → 3 entries (anthropic_style, openai_style, replicate_style) — all AI-native after the Phase 2 wedge-alignment trim.
- [ ] `POST /v1/recipes/{key}/preview` → projected products/prices/meters/dunning/webhooks (no DB writes). No `audit_log` row is written (read-only preview, not a "Created recipe").
- [ ] Unknown key → 404.

## FLOW R2: Instantiate

- [ ] `POST /v1/recipes/anthropic_style/instantiate {livemode:false}` → 201 with all created IDs. DB now has products + prices + meters + dunning policy + webhook endpoint.
- [ ] Pricing rules carry `dimension_match` JSONB.
- [ ] Repeat for all 3 recipes — each completes <500ms. (Instantiate writes no audit-log entry; created resources carry `created_by=<key_id>`.)

## FLOW R3: Per-tenant idempotency

- [ ] Instantiate same recipe twice → second call 409 `recipe already instantiated`.
- [ ] Different tenant, same recipe → 201.
- [ ] `DELETE /v1/recipes/instances/{id}` removes the instance row only — products/prices/meters/webhook/dunning PERSIST (no cascade; see R5).

## FLOW R4: Atomic rollback

- [ ] Inject mid-instantiate failure (e.g. invalid webhook URL) → 422; zero rows created.
- [ ] No `recipe_instances` row.

## FLOW R5: Dashboard UI

- [ ] `/recipes` → 3 cards (anthropic_style, openai_style, replicate_style). Preview opens side panel; Instantiate dialog names side-effects and redirects to `/products` on confirm.
- [ ] Uninstall from the Installed card → `recipe_instances` row drops; plans/meters/etc. stay (no cascade). Re-install without renaming originals → 422 name collision; re-install after archiving originals → succeeds.

---

## Invoices

## FLOW I1: Multiple meters

- [ ] Plan with **$29 base** + API ($0.01/call) + Storage ($0.10/GB). Ingest 2000 calls + 50 GB → invoice has 3 line items: base $29 + API $20 (2000 × $0.01) + storage $5 (50 × $0.10).

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
- [ ] **Coalesced rows (ADR-020)**: a paid invoice shows ONE "Invoice paid · $29.00" row, NOT a separate "Payment succeeded" row beneath it.
- [ ] A voided invoice with a previously-pending PI shows ONE "Invoice voided" row, NOT a duplicate "Payment canceled" row.
- [ ] A dunning-recovered invoice shows "Invoice paid · after 3 retry attempts" — no separate "Dunning resolved" row.
- [ ] **Failure rows fold inside-out**: each failed charge collapses to ONE row carrying the dunning attempt label ("Automatic retry scheduled" or "Payment retry #N attempted"), the PI id, the amount, the decline reason, and a `Customer notified by email` sub-line. No separate Stripe `payment_intent.payment_failed` row at the same instant; no separate "Payment-failed email sent" row beneath.
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
- [ ] **payment_processing stale (ADR-049 Phase 4)**: a REAL (non-simulated) invoice left `payment_status=processing` for more than ~6h → the in-flight banner escalates Info → **amber Warning**, message points the operator at Stripe (does NOT promise auto-resolution). A clock-pinned (simulated) invoice stays **Info** no matter how "old" its sim-time is (the age is wall-clock, guarded on `!is_simulated`).

### Info
- [ ] **payment_processing (fresh)**: muted banner, **no actions**, copy says Velox confirms it automatically (true via the synchronous inline settle / reconciler backstop — ADR-049 Phases 2–3).
- [ ] **payment_unconfirmed**: muted banner, **no actions** — copy says Velox resolves it on the next reconcile. The previously-greyed-out "Check provider" button is gone (it had no endpoint; on-demand re-check deferred per ADR-049).
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
- [ ] Backoff respected: the 1st retry is ~5min ahead (±10% jitter), the 5th ~12h ahead (schedule `[5min, 15min, 1h, 4h, 12h, …]`). Sub-5-min ticks don't double-process the row.
- [ ] **Tax-commit reconciler recovers a lost transaction id (2026-06-14):** on a finalized `stripe_tax` invoice that has a `tax_transaction_id`, simulate the commit-succeeded-but-persist-failed orphan: `psql -c "UPDATE invoices SET tax_transaction_id=NULL WHERE id='vlx_inv_xxx'"` (the Stripe transaction still exists upstream). Within one scheduler tick, `slog` logs `tax commit recoveries … recovered=1` and `psql` shows `tax_transaction_id` **repopulated with the SAME `tx_*`** — the idempotency key returned the original transaction, NOT a duplicate (check the Stripe Tax dashboard: still one transaction for that invoice). Now the invoice can be voided with a proper reversal. A `manual`-provider invoice (no `tax_calculation_id`) is never picked up.

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
- [ ] Operator emails (password-reset, member-invite) intentionally plain text — no HTML chrome.

## FLOW I7: Zero-amount invoice

- [ ] Plan `base_amount_cents=0`, no meters → either no invoice or $0 auto-paid (no Stripe charge).

## FLOW I8: Currency consistency

- [ ] Tenant default USD → switch to EUR → new invoices EUR, existing unchanged.
- [ ] Customer with `billing_profile.currency=GBP` → invoices GBP regardless of tenant default.

## FLOW SUB7: Mid-period change outcome on the timeline + invoice (2026-06-14)

- [ ] On a clock-pinned sub, do each mid-period change and check the subscription Activity feed shows the financial outcome, not just the intent: plan upgrade → "Plan changed · … · Proration invoice $X"; downgrade on a PAID prebill → "… · Credit $X"; downgrade on an UNPAID prebill → "… · Open invoice adjusted $X"; quantity increase → "Quantity changed · … · Proration invoice $X"; item add → "Item added · … · Proration invoice $X"; item remove → "Item removed · <plan> · Credit $X". Non-USD tenant shows the right currency symbol, never a hardcoded $.
- [ ] Open the UNPAID invoice that a downgrade adjusted: the credit-note row shows the reason ("Plan downgrade") AND "↳ Tax reversed $X (Stripe Tax)" — same disclosure a paid invoice's post-payment refund shows. Pre-fix the unpaid row was a bare "Credit CN-NNNN -$X".
- [ ] Credit Notes page: an adjustment CN (no refund/credit/out-of-band) shows "applied to invoice" in the channel column (not "—"), and the reason renders as "Plan downgrade" not "subscription_downgrade".

## FLOW I9: Credit note on void

- [ ] Void invoice → issue CN → error "cannot create credit notes for voided invoices". CN not created.

## FLOW I9b: Credit note PDF totals reconcile

- [ ] On a taxed paid invoice (e.g. $100 net + 10% = $110), create + issue a full CN (one line, qty 1 × $110.00 gross). Download the CN PDF: line amount **$110.00**, totals rows read **"Total excluding tax" $100.00**, tax row **$10.00**, **"Credit Total" $110.00** — line amounts sum to Credit Total; no row claims to be a sum-of-lines "Subtotal" that doesn't match.
- [ ] CN numbers are sequential per tenant (CN-…-0001, -0002). A failed number allocation FAILS the Create loudly; no CN with a timestamp-shaped number (CN-YYYYMM-…) is ever created.

## FLOW I10: Hosted invoice page

- [ ] Draft invoice has no `public_token`. Finalize → token minted (`vlx_pinv_` + 64 hex).
- [ ] Detail page: **Copy Link** button. **Rotate** typed-confirm dialog (type `ROTATE`). Buttons hidden on draft.

### Public render (open in incognito)
- [ ] Loads without login. Header: tenant logo + company_name + support_url. Optional 3px accent bar.
- [ ] Invoice meta: number (mono), amount due (large tabular), due date.
- [ ] Bill-to + From columns. Line-items table with tabular numerals.
- [ ] Totals: subtotal, optional discount, optional tax with rate, reverse-charge **or tax-exempt** notice if applicable (`Tax-exempt — <reason>` for an exempt customer; previously dropped on the hosted page), total, amount paid, **Amount due** bold.
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

## FLOW I11: `create_preview`

- [ ] `POST /v1/invoices/create_preview {subscription_id}` → invoice shape with `id=null`, no DB row.
- [ ] No `audit_log` row from a preview: open a customer detail page (the upcoming-invoice card fires `create_preview` on load), then open `/audit-log` → **no** new "Created invoice" row.
- [ ] Plan-change confirmation dialog renders preview before commit.
- [ ] Cost-dashboard projection populated when engine returns a value.
- [ ] **`in_advance` preview** (ADR-031): for a sub on an `in_advance` plan, preview's `billing_period_start/end` is the **upcoming** period (matches what the cycle invoice will stamp). Base line description carries the "in advance for upcoming period" suffix. Usage line totals match the elapsed period (per the engine's stamping). Totals identical to in_arrears preview — only the period labels differ.

## FLOW I12: One-off invoice composer

- [ ] Customer detail → "New invoice" → composer shows Currency + Payment terms (Net 30 default) + line editor.
- [ ] Enter three lines at `3333` / `3333` / `3334` cents → Subtotal ticks to $100.00; Tax row reads "Calculated at finalize".
- [ ] Save draft → exactly ONE `POST /v1/invoices` (no follow-up `add-line-item` calls); row appears with `status=draft`, `subscription_id=null`, `billing_reason=manual`, `tax_amount_cents=0`.
- [ ] Tenant Tax = manual 7.25%, Finalize → `tax_amount_cents=725`, `SUM(line tax)=725` (residual on last line), `total_amount_cents=10725`, `due_at = issued_at + 30d`.

## FLOW I13: Timeline completeness

- [ ] Tax-deferred invoice that auto-finalizes via tax retry → `invoice.finalized` lands in the webhook event stream.
- [ ] Issue a credit note on a paid invoice → the invoice Activity shows a dated "Credit note issued / Refund issued — $X" row (CN number + reason as subtext).
- [ ] Mark an invoice uncollectible (operator menu) → Activity shows "Marked uncollectible — written off as bad debt" with the timestamp; the page STOPS polling (network tab quiet).
- [ ] Record an offline payment → Activity row reads "Payment recorded (offline)", not a card payment.
- [ ] Plan-change with immediate proration → the subscription Activity row reads "Plan changed · <old> → <new> · Immediate · Proration invoice $X". A scheduled change shows a second row "Scheduled plan change applied" at the boundary; a threshold crossing shows "Spending threshold crossed · Invoice <num> issued early — $X".
- [ ] Scheduled-cancel sub: period bar reads Period Start → Period End → **Cancels** (no "Next Billing"); paused-collection sub shows **Collection resumes <date>**.

---

## Dunning

## FLOW D1: Retry cycle + escalation

- [ ] Decline card → run billing → dunning run created. Page shows stat cards (Active, Escalated, Recovered, At Risk $) + tab filters with counts.
- [ ] Sidebar Dunning badge shows count.
- [ ] Run state Active, "No retries yet", `next_action_at` scheduled.
- [ ] Backdate `next_action_at` → next tick increments attempt count.
- [ ] After max retries → state Escalated.
- [ ] **Card-less invoice enters dunning and reaches a terminal (2026-06-23, ADR-060)**: a finalized `auto_charge_pending` invoice for a customer with **no saved card** gets a dunning run created on the next scheduler tick (no charge attempted) and escalates through grace + retries to the policy `final_action` (e.g. `pause`) — instead of being retried forever with no terminal. Adding a card mid-campaign → the auto-charge sweep collects it and the run resolves `payment_recovered`. With dunning disabled for the tenant, the invoice stays un-dunned (deliberate — same as the declined-card path).

## FLOW D2: Resolution

- [ ] "Payment recovered" → invoice marked **paid** (`paid_at` stamped; clock-pinned invoices land in sim-time), run closed.
- [ ] "Void invoice" (resolution `manually_resolved`) → invoice **voided** (`status='voided'`); any applied credits **reversed** to the customer's balance; the Stripe PaymentIntent **canceled**.
- [ ] The void option reads **"Void invoice"** with destructive (red) styling and copy steering offline payments to "Payment recovered" — NOT a benign "Manually resolved / offline payment" label (which would trap an operator into voiding an invoice they actually collected).

## FLOW D4: Self-service payment update

- [ ] Trigger payment failure → email/log carries `http://localhost:5173/update-payment?token=vlx_pt_…`.
- [ ] Open in incognito → page loads without login, shows customer + invoice + amount, "Secured by Stripe".
- [ ] Click Update → Stripe Checkout setup → new card → redirect → the `setup_intent.succeeded` webhook records the card. The **customer detail page now shows it** under "Payment method" (brand + last 4), and the previously-unpaid invoice auto-charges on the next sweep (or clock advance for a clock-pinned customer). The card persists **even though** the hosted Checkout puts `velox_customer_id` only on the session, not the SetupIntent — the webhook resolves the customer from the SetupIntent's `customer` field (2026-06-14).
- [ ] First-time customer (no Stripe Customer yet — the usual no-saved-card case this email targets): clicking Update creates the Stripe Customer on demand and opens Checkout, NOT a "customer does not have a Stripe payment setup" error.
- [ ] A failed attempt does NOT spend the link: if the Update click errors before Checkout opens (e.g. Stripe momentarily unreachable), re-opening the same link still works — only a successful Checkout open consumes the token.
- [ ] Re-open same URL after a successful use → "Link expired or invalid". Random token → same. No token → "No payment update token provided".

---

## Credits & Credit Notes

## FLOW C1: Credits lifecycle

- [ ] Grant $50 expires 30d → balance $50, ledger Expires column populated.
- [ ] Run billing → applied, amount_due reduced, Stripe charged remainder. Ledger entry "Applied to invoice <number>".
- [ ] Grant $500 + $79 invoice → fully credited, amount_due $0, balance $421, Stripe NOT charged.
- [ ] Deduct $20 → confirmation → balance reduced, ledger entry.

## FLOW C2: Credit notes

- [ ] **In-flight payment blocks an amount-reducing credit note (2026-06-22)**: `psql -c "UPDATE invoices SET payment_status='processing' WHERE id='<a finalized, unpaid invoice>'"` → POST a credit note for it → **409** "cannot credit-note an invoice whose payment is in flight — settle or cancel the payment first". Reset to `pending` → the same credit note succeeds. (Operator gate; the automated clawback defers instead — see the Part-B flow below.)
- [ ] **In-flight payment blocks Void and Mark-uncollectible (ADR-059)**: with a finalized invoice forced to `payment_status='processing'` (psql, as above) → POST `/v1/invoices/{id}/void` → **409** "a charge is in flight … wait for it to settle or cancel it before voiding"; POST `/v1/invoices/{id}/mark-uncollectible` → **409** "… before marking uncollectible". Reset to `failed` (or `pending`) → both succeed. (`unknown` behaves like `processing`; `RecordOfflinePayment` is blocked the same way.)
- [ ] **Automated clawback DEFERS on an in-flight source, then settles correctly (ADR-059, Part B)**: on a clock-pinned in_advance sub whose paid prebill is forced to `payment_status='processing'` (psql), downgrade mid-cycle. A clawback credit note row is created **`status='draft'`, `issue_pending=true`** and the prebill's `amount_due` is **unchanged** (not reduced) — the reconciler scan skips it while the source is in flight. Then set the source `payment_status='succeeded'` (psql) and wait one scheduler tick (or trigger the clawback reconciler): the draft flips to **`issued`** and the unused share lands as a **customer balance credit** (paid branch). Repeat with the source set to `failed` instead → the draft issues down the **reduce** branch (`amount_due` drops to the consumed portion). Repeat with the source **voided** before settle → the draft is **voided** (orphan guard), no second tax reversal.
- [ ] Unpaid invoice → Issue credit "Billing error" $20 → no allocation inputs shown → Issue → amount_due reduced.
- [ ] Paid invoice ($100, fully card-paid) → enter $40 → defaults to Credit balance $40, Refund 0, Out-of-band 0 → Issue → customer balance +$40.
- [ ] Same invoice → enter $30 + type $30 in Refund to card → Credit auto-fills to $0; Allocated $30 / $30 ✓ → Issue → Stripe refund processed; CN row label "refund"; refund_status=succeeded.
- [ ] Mixed-paid invoice ($82.60 = $62.60 card + $20 credits) → enter $80 → type $62.60 in Refund to card → Credit auto-fills to $17.40 → Issue → Stripe refund $62.60 + credit grant $17.40; CN row label "refund + credit"; balance increases by $17.40.
- [ ] Same mixed invoice → enter $80, type $70 in Refund → inline error "Refund cannot exceed $62.60 paid via card"; Save disabled.
- [ ] Three-channel split: $100 CN → $40 Refund + $30 Credit + $30 Out-of-band → Allocated $100 / $100 ✓ → Issue → all three persisted; CN row label "refund + credit + out of band".
- [ ] Sum mismatch: $50 CN with Refund $20 + Credit $20 + OOB $0 = $40 ≠ $50 → "Allocated $40 / $50 ✗" red; Save disabled.
- [ ] CN > amount_due (unpaid) or > total_amount (paid) → error.
- [ ] CN page: stat cards (Total Credited, Refunded, Applied to Balance, Issued); list rows show channel breakdown ("refund" / "credit" / "refund + credit" / etc.); CSV export has separate Refund/Credit/Out-of-band columns.

## FLOW C2b: Credits ledger readability

- [ ] Customer ledger shows 5 columns (Date · Type · Description · Amount · Balance) with Amount/Balance fully visible at a 1280px window — nothing clipped at the right edge.
- [ ] An "Applied to invoice DEMO-NNNN" row's description IS the invoice link; a grant with expiry shows "Expires <date>" as subtext; an expiry row reads "Grant expired" (machine id only as small mono subtext).

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
- [ ] **Test-mode replay stays test-mode (2026-06-13):** in **test** mode, replay a webhook event → the new delivery row appears in the test-mode Events view and `SELECT livemode FROM webhook_deliveries WHERE id='<new>'` is `false`.
- [ ] Multi-retry event → "Diff" tab shows payload diff between attempts.
- [ ] Stop Redis or dispatcher → readiness degraded; UI loads but stops streaming.
- [ ] **Failed delivery walks the full retry ladder (2026-06-13):** point an endpoint at a URL that always 5xxs, trigger an event → the delivery row stays `pending` across **five** scheduled retries with backoffs 1m → 5m → 30m → 2h → **24h** (`next_retry_at` steps match), and only flips to `failed` after the attempt past the 24h slot (`attempt_count=6`).

---

## Customers

## FLOW CU1: Settings + billing profile

- [ ] Settings: company name change → "Saved" indicator. Navigating with unsaved changes prompts.
- [ ] **Settings changes are audited field-by-field (2026-06-12)**: change e.g. the default currency + Net terms, save, open `/audit-log` → one "Updated Settings" row whose expanded metadata shows `changed` with each field's `from`/`to` (e.g. `default_currency: USD → EUR`) — including a flip of the fail-closed audit policy itself. A no-op save (no fields changed) adds no field-level row. Actor = the signed-in operator, not "System".
- [ ] Currency change → new invoices use it; existing unchanged.
- [ ] Edit billing profile (address, tax ID) → PDF reflects update.
- [ ] Edit billing profile when customer has `stripe_customer_id` set → Stripe Dashboard → Customer shows the updated legal_name / phone / address / tax_exempt immediately (Phase 1 Velox→Stripe sync, best-effort, fires on every customer/profile update). <!-- currency-ok: Stripe Customer object's own tax_exempt field -->
- [ ] Create a brand-new customer with email + display_name + billing profile → first PM action (operator send-setup-email / copy setup-link) lazily creates the Stripe Customer pre-populated with email, name, address, and tax_exempt status — Stripe Dashboard shows a fully-populated row, NOT a blank one with only `velox_*` metadata. <!-- currency-ok: Stripe's own tax_exempt field -->
- [ ] Set billing profile tax_id (e.g. `eu_vat` + `DE123456789`) → Stripe Dashboard → Customer → Tax IDs tab shows the entry (Phase 2 reconcile). Change tax_id value → old entry gone, new entry present. Clear tax_id → Tax IDs tab empty. Brand-new customer with tax_id pre-filled in profile → first PM action creates the Stripe Customer with the tax_id already in the Tax IDs tab (no follow-up update call needed).
- [ ] Draft invoice held >24h, then click Finalize → operator sees `tax calculation expired (age 24h0m, max 23h0m) — retry tax to refresh, then finalize` (Phase 2 expiry guard). Click Retry tax → tax recomputes → Finalize succeeds, Stripe Tax dashboard shows a `tx_*` transaction. Without the guard, finalize previously left the invoice with `tax_calculation_id` populated but no `tax_transaction_id`.
- [ ] **Tax-retry flush on profile update.** Customer with a draft invoice stuck on `tax_error_code = customer_data_invalid` (e.g. US customer missing `postal_code`). Edit billing profile → fill the missing field → Save. Without per-invoice clicking: invoice's `tax_status` flips to `succeeded` (or back to `failed` with a different code if still wrong), and `slog | grep "billing profile flush retried tax errors"` shows `processed >= 1`. Other stuck-tax codes (e.g. `provider_outage`) are NOT replayed by the flush — only `customer_data_invalid`.

## FLOW CU2: Operator customer-portal API

- [ ] `GET /v1/customer-portal/{customer_id}/overview` → active subs, recent invoices, credit balance.
- [ ] `/subscriptions`, `/invoices` scoped to that customer.

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
- [ ] After running a dunning catchup that exhausts an N-retry policy: the card shows N+1 rows for the run's invoice — 1 "Payment failed" + (N-1) "Payment retry — action required" + 1 "Retries exhausted". Each row's recipient matches `customers.email` (the single canonical recipient since migration 0100); each carries a wall-clock send time (real-time SMTP dispatch instant, NOT the simulated cycle date).
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
- [ ] **Dashboard actions are attributed to the operator, not "System" (2026-06-12)**: any action taken from the signed-in dashboard records `actor_type=user` with the operator's user id (Actor column shows the user, not "System"). Only genuine background work (engine auto-cancel, scheduler) records `actor_type=system`. API-key (Bearer) callers still record `actor_type=api_key` with the key name.
- [ ] **Append-only is TRUNCATE-proof (migration 0115)**: `TRUNCATE audit_log` (even as the app role / with `app.bypass_rls='on'`) fails with `audit_log is append-only; TRUNCATE is not permitted` — matching the existing UPDATE/DELETE block. Rows remain intact.
- [ ] **No customer PII in audit metadata (2026-06-12)**: change a customer's email, upsert a billing profile with a tax ID, send an invoice, send a setup link → the audit rows' expanded Details show `email_changed`/`tax_id_set` flags and ids — never the raw email address or tax ID (append-only log + GDPR erasure). The email outbox keeps the actual delivery addresses.
- [ ] **Operator rows show the operator's name (2026-06-12)**: dashboard-driven rows render the user's display name or email in the Actor column (never the raw `user` enum or a bare `vlx_usr_…` id).
- [ ] **Humanized rows (2026-06-12)**: the action badge reads "Subscription Item Updated", not `subscription.item_updated`; metadata labels case acronyms correctly ("IP Address", not "Ip Address"); structured metadata values (e.g. the settings `changed` map) render as JSON, not `[object Object]`; item add/remove/update rows on a subscription carry the sub's code as the label; expanded Resource ID has a copy button.
- [ ] **Cursor pagination (2026-06-12)**: the list pages with Previous/Next (no numbered jumps); footer reads "Page N · M total". Paging deep does not slow down (seek pagination — only page 1 computes the total). Filters reset to page 1.
- [ ] Destructive rows have red left border. Expand → metadata + "View" link.
- [ ] Filters: resource type, action, date range. Export CSV → all entries.
- [ ] **Export honors the tenant timezone (2026-06-12)**: on a non-UTC tenant (e.g. IST), set a date-range filter so a row sits within an hour of a day boundary; the exported CSV contains exactly the rows shown on screen for that range — no edge row silently dropped from (or added to) the export.
- [ ] **Export includes a Details column (2026-06-12)**: the exported CSV has a final **Details** column carrying the row's metadata JSON (e.g. `{"action":"marked_uncollectible"}`, prorated amounts, old/new plan); empty for rows with no metadata.
- [ ] **Entity pages link to their audit history (2026-06-12)**: Customer, Invoice, and Subscription detail pages each show an **"Audit log"** button in the header; clicking it opens `/audit-log` pre-filtered to that record (resource type + id), showing only that entity's events.
- [ ] **Date filter accepts both formats**: `?date_from=2026-01-01&date_to=2026-12-31` (bare YYYY-MM-DD) and `?date_from=2026-01-01T00:00:00Z` (RFC3339) both work. Invalid input (`?date_from=garbage`) → 400 with field-level error. Same shared parser (`internal/api/timefilter`) as the export + usage endpoints.

## FLOW P2A: Audit log — customer-initiated + Tier 2 coverage

Verifies the 2026-05-26 audit sweep wired every state-changing flow into `audit_log` and the AuditLog page renders the new resource types correctly.

- [ ] Engine auto-fires scheduled cancellation (advance the test clock past cycle close) → AuditLog row: "Canceled <sub>" with meta `canceled_by=schedule`, actor "System".
- [ ] **Engine-finalized invoices are audited (2026-06-12)**: advance a test clock past a cycle close (or create a sub with an in_advance plan) → AuditLog shows a **"Finalized INV-NNN"** row for the engine-generated invoice, actor "System", meta `triggered_by=subscription_cycle|subscription_create|…`, with the test-clock chip/sim subline on clock-pinned subs.
- [ ] **Trial auto-expiry is audited (2026-06-12)**: let a trial lapse via clock advance (or the wall-clock scheduler) → "Trial ended" row on the subscription with `triggered_by=schedule`, matching the operator End-Trial row.
- [ ] Operator marks invoice uncollectible → "Marked INV-NNN uncollectible" — **exactly one row**.
- [ ] Operator records offline payment → "Recorded offline payment on INV-NNN".
- [ ] Operator clicks **Collect payment** on a finalized invoice → "Collected payment on INV-NNN" (amber/medium severity, `action='collect'`), NOT "Created INV-NNN".
- [ ] Operator clicks **Send** → "Emailed INV-NNN" (`action='send'`; metadata carries the invoice + customer ids, NOT the recipient address — PII stays out of the append-only log; the email outbox is the delivery record).
- [ ] Operator **Refund** → "Refunded INV-NNN" (red/high severity).
- [ ] Operator rotates the hosted-invoice link → "Rotated hosted-invoice link for INV-NNN". None of these render as a green "Created" row.
- [ ] Operator edits customer (display_name / email / dunning policy) → "Updated <name>".
- [ ] Operator upserts billing profile (tax status / address / tax ID) → "Updated billing profile for <name>".
- [ ] Customer adds card via Stripe Checkout → "Added Visa ····4242" (resource_type payment_method, action create, actor "<Customer Name>").
- [ ] Operator promotes a non-default card to default → "Set Visa ····4242 as default".
- [ ] Operator detaches a card → "Removed Visa ····4242".
- [ ] Operator creates / revokes / rotates an API key → corresponding rows. Raw key value never appears in metadata.
- [ ] Operator creates / updates / deletes a dunning policy + sets default + resolves a run → matching rows. Resolve row carries `meta.resolution=payment_recovered` etc.
- [ ] Operator creates / updates a plan, creates a meter / rating rule / price override → matching rows.
- [ ] Operator creates / advances / retries / deletes a test clock → matching rows with `frozen_time` snapshot in metadata.
- [ ] Operator connects / disconnects Stripe / sets webhook secret → matching rows. Secret value never in metadata.
- [ ] Operator creates / deletes / rotates a webhook endpoint + replays an event → matching rows. Secret value never in metadata; replay row links to original via `meta.replay_of`.

## FLOW P2B: Operator-side payment-method management

- [ ] CustomerDetail → Payment Methods card lists every attached PM with brand/last4/expiry + Default badge on the primary.
- [ ] No card capture form on the dashboard (PCI: card data must go through Stripe-hosted iframe only).
- [ ] "Add Card" → dialog opens with the customer's email pre-filled (read-only), an optional note field, primary "Send email" button, and a secondary "Copy link" path under an "or" divider.
- [ ] Send email path: typing a note (≤2000 chars) + Send email → 202 from `/v1/customers/{id}/payment-methods/send-setup-email` → toast "Setup link sent to {address}" → Mailpit at http://localhost:8025 shows a branded "Add a payment method" email with the note rendered above the CTA → CTA opens a Stripe Checkout session → completing it adds a new PM and the panel refreshes.
- [ ] Empty note → email body uses the generic fallback intro ("Please add a payment method on file so we can process your billing…").
- [ ] Customer with no email on file → Send email button disabled with tooltip "Add an email on the customer record first"; Copy link path still works.
- [ ] Copy link path: clicking "Copy link" mints the session, auto-copies the URL, and displays the URL in the dialog with a Copy button for re-copy → paste into incognito tab → flow completes the same as the email path.
- [ ] Closing the dialog mid-flow discards any minted URL (operator can mint another fresh URL next time).
- [ ] Non-default card → "Set as default" promotes it; the badge moves; previous default loses its badge.
- [ ] **Auto-charge hits the explicitly-set default, not the newest card (ADR-053)** — attach two cards (`4242…4242`, then `5555…4444`), "Set as default" the FIRST/older one, then auto-charge an invoice (finalize a cycle or manual invoice). The paid invoice's payment-card sub-line shows the **default** card's brand/last4 (visa 4242), NOT the most-recently-added card — Velox names the exact card; Stripe never picks.
- [ ] "Remove" on a non-default card → confirm → card detaches in Stripe + disappears locally; default unchanged.
- [ ] **Remove the DEFAULT card with others present (ADR-053)** — with ≥2 cards, "Remove" the one holding the Default badge → confirm → it disappears and the **newest remaining** card gains the Default badge (never a no-default state). The promoted card also becomes the default on the customer's Stripe dashboard.
- [ ] "Remove" on the last card → confirm dialog still works → list becomes empty.
- [ ] "Remove" disabled (with tooltip) when only one card remains AND it's default — operator must add another card first.
- [ ] Archived customer → all PM action buttons hidden (parity with other archived-customer UI guards).
- [ ] AuditLog page renders the send-email action as "Updated {customer name}" with `meta.action=setup_link_sent`, `meta.to=<address>`, actor = the operator's API key name.
- [ ] Cooldown: clicking "Send email" twice in <60s → second call returns 429 with `Retry-After` header + toast "a setup link was sent to this customer recently — wait before sending again". Cooldown is a strict 60s window per customer; the next send succeeds only after it expires.
- [ ] InvoiceAttention banner (invoice in attention state, e.g. `update_payment_method` action) → clicking "Update Payment Method" opens the SAME dialog as CustomerDetail's Add Card → recipient email pre-filled, note pre-filled with invoice context ("We couldn't process payment for invoice INV-NNN ($X.XX). Please add a payment method using the secure link below."), email path lands a branded "Action required — update payment for invoice INV-NNN" subject in Mailpit; copy-link path mints a Stripe Checkout URL the operator can paste into Slack/SMS.
- [ ] Engine no-PM-at-finalize email (invoice finalizes for a customer with no PM on file) → branded "Action required — update payment for invoice X" email lands in Mailpit + AuditLog shows a `meta.action=setup_link_sent`, `meta.trigger=finalize_no_pm`, `meta.invoice_id=<id>` row with actor "System" (engine-fired).
- [ ] Legacy endpoints removed: `curl -X POST .../v1/payment-portal/{id}/update-payment-method` returns 404 (route deleted in the unified-paths cleanup).

## FLOW P3: Usage summary

- [ ] `GET /v1/usage-summary/{customer_id}?from=…&to=…` → per-meter aggregated totals matching ingestion.

## FLOW P4: Empty billing cycle

- [ ] No subs due → trigger billing → "0 invoice(s) generated", clean exit, dashboard unchanged.

## FLOW P5: Health checks

- [ ] `/health` → 200 `{"status":"ok"}`. `/health/ready` → 200 with database, scheduler ok.
- [ ] Stop Postgres → `/health/ready` → 503 `degraded` with `database: error:…`. `/health` still 200.
- [ ] Kill scheduler goroutine or wait past 2× interval → readiness shows scheduler degraded.

## FLOW P6: Tax deferral metrics

- [ ] `curl -H "Authorization: Bearer $METRICS_TOKEN" /metrics | grep velox_tax_outcome_total` → counter registered (the legacy `velox_tax_fallback_total` was renamed when the zero-tax fallback was cut — ADR-041; outcome is now `deferred`). <!-- currency-ok: documents the metric rename -->
- [ ] Reasons increment correctly: `velox_tax_outcome_total{outcome="deferred",reason=...}` for `no_country` (customer missing country), `no_client_for_mode` (Stripe not connected for the active livemode), `api_error` (invalid Stripe key).
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

## FLOW U11: Operator search + list filters (2026-06-12)

Setup: ≥26 customers so at least one lands on page 2 (FLOW S1 tenant + a quick create loop works).

- [ ] Customers page: search a page-2 customer by email fragment → row appears; `Showing 1–1 of 1`.
- [ ] Customers page: search `zzz-no-match` → "No customers match" empty state with its own Clear filters button; search input still visible.
- [ ] Invoices page: search a full invoice number (e.g. `INV-2026-0003`) → that invoice only.
- [ ] Invoices page: From/To date pickers filter across pages (pick a range excluding today → today's invoices gone, total shrinks).
- [ ] Invoices page: **Past due** tab → only finalized invoices with `due_at` in the past and payment not succeeded/processing.
- [ ] Customer detail → Outstanding card link → Invoices opens with a dismissible `customer: <name>` chip and only that customer's invoices; × clears it.
- [ ] Customer detail → Sent emails → click an invoice number → Invoices opens pre-searched to that number.
- [ ] ⌘K: type a page-2 customer's email → customer appears with email in the subtitle; Enter navigates to its detail page.
- [ ] ⌘K: paste an invoice number → invoice appears; works for invoices beyond the 50 most recent.
- [ ] Subscriptions page: search by code fragment → matches across pages.
- [ ] Refresh any filtered list URL → filters (search/status/dates/page) restore from the URL.
- [ ] Customer detail → External ID row has a copy icon; Subscription detail → Customer row has a copy icon (copies the raw customer id).

## FLOW U12: Dashboard consistency sweep

- [ ] Browser tab reads "Invoices · Velox" on the list, "INV-…-NNNN · Velox" on an invoice; two different tabs are distinguishable.
- [ ] An invoice ≥ $1,000 renders with thousands separators everywhere ($10,000.00, not $10000.00).
- [ ] On /customers/:id the sidebar still highlights Customers; on /plans/:id it highlights Pricing.
- [ ] Dunning policies → Delete asks via an in-app dialog (no native browser confirm). Webhooks → Rotate Secret shows "Rotating…" and can't be double-clicked.
- [ ] Webhook live tail shows customer names, not raw vlx_cus ids (unknown ids show shortened, full id on hover).
- [ ] ⌘K → type a plan name → Enter lands on the plan's detail page.

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

- [ ] `/invoice/:token` (FLOW I10), `/update-payment` (FLOW D4), `/payment-method-added`, `/login` all load without auth. (The old `/checkout/success`/`/checkout/setup`/`/checkout/status` routes were removed in the unified-PM-paths cleanup.)

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
- [ ] Wait 10s, 20 more → ~16 allowed (limit is 100/min = 1.67/sec, so 10s refills ≈ 16.7 tokens).
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
- [ ] `SELECT legal_name, phone, tax_id FROM customer_billing_profiles …` → all 3 prefixed `enc:`. (email column dropped in migration 0100 — recipient is `customers.email` only.)
- [ ] Pre-encryption rows still read correctly (no `enc:` prefix → returned as-is).

## FLOW X7: Stripe Tax

- [ ] `PUT /v1/feature-flags/billing.stripe_tax {enabled:true}`. Customer with full address → invoice tax calculated by Stripe; `tax_name` shows jurisdiction; per-line tax populated.
- [ ] Invalid Stripe key → invoice is **deferred** to `tax_status=pending` (NOT charged $0 — the zero-tax fallback was removed in ADR-041); the TaxRetrier reconciles it later. Counter `velox_tax_outcome_total{outcome="deferred",reason="api_error"}` +1.
- [ ] Customer `tax_status=exempt` → $0 tax regardless.
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

- [ ] POST /v1/usage-events/batch with 1000 events → `{ingested:1000, errors:[], total:1000}`.
- [ ] Include duplicate idempotency keys → duplicates rejected.
- [ ] Run billing → aggregated correctly.

## FLOW X14: Self-host (Compose)

- [ ] `docker compose up -d postgres redis mailpit` → 3 sidecars healthy.
- [ ] `make bootstrap` + `make dev` + `cd web-v2 && npm run dev`. `/health` and `/health/ready` 200.
- [ ] `RUN_MIGRATIONS_ON_BOOT=true` applies all migrations idempotently (default is `false` — set it explicitly for local dev).
- [ ] Mail catches at `localhost:8025`.

## FLOW X15: LiteLLM integration (ADR-033)

The wedge integration. Validates the adapter accepts LiteLLM's `StandardLoggingPayload`, resolves customer + meters, and dedupes replays.

- [ ] Create a customer with `external_id="cus_litellm_test"`.
- [ ] Instantiate the `anthropic_style` recipe (creates one `tokens` meter + per-`{model, token_type}` rules — ADR-044).
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
  → `{"accepted":2,"skipped":0,"errors":[]}`. `GET /v1/usage-events?customer_id=<internal cus_ id>` shows TWO events on meter `tokens` — `token_type=input` qty 1200 + `token_type=output` qty 350 — both with `dimensions.model="claude-3-5-sonnet-20241022"` + `dimensions.provider="anthropic"`. (List filter is the internal `customer_id`, not `external_customer_id`.)
- [ ] Idempotent replay: same POST again → `accepted=0` (events already exist; the `(tenant, customer, meter, idempotency_key)` UNIQUE caught the replay). No duplicates in the event list.
- [ ] Missing `user`: payload with `"user":""` → response has `errors[]` populated, batch otherwise OK. **NOT 5xx.**
- [ ] Unknown customer: `"user":"cus_nonexistent"` → same partial-failure shape: `errors[].error` says `customer "cus_nonexistent" not found (set user=...)`.
- [ ] Non-token-bearing call: `"call_type":"image_generation"` → `accepted=0, skipped=1`. No events emitted.
- [ ] Zero-token completion (error / empty response): `"usage":{"prompt_tokens":0,"completion_tokens":0}` → `accepted=0, skipped=1`.
- [ ] Batch shape: POST `{"events":[<payload1>,<payload2>,...]}` → each payload mapped independently. Per-row failures don't fail the batch.
- [ ] Bare array shape: POST `[<payload1>,<payload2>]` → same handling as `events:[...]`.
- [ ] Embedding call: `"call_type":"embedding","usage":{"prompt_tokens":500,"completion_tokens":0}` → ONE event (meter `tokens`, `token_type=input`), `accepted=1`.
- [ ] Dimension promotion: `"metadata":{"team_id":"team_eng","request_tags":["prod"],"x_other":"ignored"}` → emitted events have `dimensions.team_id="team_eng"` and `dimensions.request_tags=["prod"]`; `x_other` stays in the event's `metadata.litellm_metadata.x_other` (NOT promoted to dimensions for pricing dispatch).
- [ ] Cost surfacing: `cost_breakdown:{input_cost:0.012,output_cost:0.045,total_cost:0.057}` → input event's `metadata.velox.litellm_cost_usd=0.012`, output event's `metadata.velox.litellm_cost_usd=0.045`. Velox billing math is unaffected — pricing rules drive the invoice amount; LiteLLM's cost is audit-only.
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
- These defer the invoice to `tax_status=pending` (the TaxRetrier reconciles) and bump `velox_tax_outcome_total{outcome="deferred",reason=...}` — they do NOT charge $0 (zero-tax fallback removed, ADR-041).
- Missing customer address → `{reason="no_country"}`.
- Tenant in disconnected mode → `{reason="no_client_for_mode"}`.
- Invalid key → `{reason="api_error"}`.

## Rate limit not triggering
- Redis not connected → `redis-cli ping`, check `REDIS_URL`.
- Sequential curl too slow — use parallel.
- Public endpoints (`/health`, `/metrics`, `/v1/bootstrap`) intentionally unrestricted.

## PII not encrypted
- `VELOX_ENCRYPTION_KEY` not set when row created → backward-compat plaintext (FLOW X5).
- Wrong field — only customer display_name/email + billing profile legal_name/phone/tax_id are encrypted.

## Webhook signature fails
- Wrong `whsec_…` after `stripe listen` restart (CLI rotates per run).
- Clock skew >5 min → rejected (FLOW W1).
- Wrong webhook URL — must be `/v1/webhooks/stripe/<vlx_spc_…>`.