# ADR-094: Login brute-force protection — Postgres-authoritative, non-weaponizable, extensible

Date: 2026-07-17
Status: Accepted (pragmatic-lean first cut; full design documented, rest deferred)
Relates: ADR-011 (homegrown email+password auth — the login this protects), ADR-014 (SSO stays homegrown/embedded, no SaaS auth vendor — same self-host constraint), ADR-093 (CSRF gate — sibling pre-auth hardening)

## Context

The dashboard operator login (email+password → httpOnly cookie, ADR-011) shipped
with a brute-force lockout: a per-email failed-login counter in Redis, an
in-process fallback behind a circuit breaker, and a hard `users.locked_until`
lock after 5 misses (velox-ops #21). Walking it end-to-end surfaced three
problems worth fixing at the root rather than patching:

1. **It fragments.** The count lives in *two* stores (Redis + the in-process
   fallback) that never merge. During a Redis flap, some attempts land in Redis
   and some in memory, loosening the effective threshold to ~2× — the exact
   distributed-counter footgun the two-store design invites.
2. **It is weaponizable.** A hard lock keyed on a *bare account* means anyone who
   knows an operator's email can lock them out on demand (5 wrong guesses → 15
   minutes of denial). This is the on-demand DoS lever OWASP explicitly cautions
   against; account lockout is a known anti-pattern.
3. **It leaks PII.** The Redis key was `velox:login_fail:<PLAINTEXT-EMAIL>` —
   operator emails in a store visible via `KEYS`/`SCAN` and RDB/AOF snapshots,
   the same class the audit #485 arc spent effort purging.

A design panel (5 independent architectures, each adversarially red-teamed, then
scored and synthesized) produced the target design below. This ADR records that
target in full so it is predetermined, then ships a **pragmatic-lean first cut**
and defers the rest with explicit triggers.

## The target design (documented now, built incrementally)

The load-bearing insight from the panel, which every proposal reached
independently: **a failed-attempt counter only catches attackers who *guess*.**
Credential stuffing and password spray often submit the *correct* (breached)
password and succeed on the first try — no counter ever ticks. So the throttle is
the cheap outer supplement; the controls that actually stop the dominant threats
are **mandatory MFA/step-up** and a **login-time breached-password check**
(HIBP k-anonymity, or a shipped local Pwned-Passwords filter for air-gapped
operators — consistent with ADR-014's self-host constraint).

Architecture (defense in depth, from cheap/outer to real/inner):

- **Postgres is the single durable AUTHORITY** for both the counter-floor and the
  escalation decision. Login already hard-depends on Postgres (it reads
  `password_hash`), so making the throttle authoritative here adds **no new
  SPOF** and there is no second store to diverge from — the fragmentation class
  is gone by construction.
- **Optional accelerators strictly ABOVE the authority**, governed by two
  invariants so every degradation fails *safe*: (I1) accelerators write through
  to the Postgres floor and, on degradation, *seed from* it rather than starting
  a fresh parallel count; (I2) **ratchet-monotonicity** — an accelerator may only
  make a decision *more* restrictive; only a full authenticated success relaxes
  it. Under these, an asymmetric partition can at most *over*-restrict slightly,
  never the unbounded *loosening* of today's dual-count.
- **The counter is a sensor, not a gate.** It feeds a **graduated ladder**
  (tarpit → CAPTCHA/PoW → post-password step-up/MFA), never a hard account lock.
  The only hard block attaches to the **attacker dimension** — `(IP-prefix ×
  account)` — so a party who knows only an email can, at worst, force the owner
  through one self-clearing challenge they always pass. `users.locked_until`
  survives only as a rare operator/extreme-confidence backstop, never auto-fired.
- **Multi-dimension keying:** per-`(IP-prefix × account)` (the hard-block dim),
  per-account and per-IP-prefix sensors, a sharded global rollup, and an HLL
  fan-in (distinct IP-prefixes per account) — the last two **alert-only**, never
  fed back into a per-request decision (that would be a global-friction DoS
  lever). IPs are keyed on prefixes (IPv4 /24, IPv6 /64); a bare IPv6 address is
  2⁶⁴ fresh sources. Every subject is `HMAC(pepper, …)`, never plaintext.
- **Fail direction, per component:** Postgres shares fate with auth (down = login
  down anyway, correct). Redis accelerator: fail-*open* for latency, fail-*closed*
  for security (fall through to the Postgres floor; never drop the check, never
  wholesale-block on a blip; emit a degraded metric — loud, not silent).
- **Self-host tiers, one code path:** full (Redis + N instances), single
  instance, and multi-instance-no-Redis all work because the authority is always
  Postgres; aggregate-only dimensions degrade to a boot-WARN + a metric, never a
  silent no-op.

The panel's rejected alternatives (kept so the reasoning survives): a blocking
`pg_advisory_xact_lock` on the write path (connection-pool self-DoS); an exact
sliding-window-*log* COUNT (quadratic under the flood it measures); Redis as the
authority or a hard multi-instance dependency (wrong shape for a self-hostable
product); a single multi-key `EVALSHA` (Redis Cluster CROSSSLOT); RedisBloom
CMS/TopK (unloadable on managed/self-host Redis); hard account lockout as the
primary response; distinct `429`/`account_locked` responses (enumeration oracle);
ASN/shared-IP as a hard-block dimension (collateral DoS); and treating hashcash
PoW as a strong control (a botnet out-computes a phone).

## Decision — what ships now (the pragmatic-lean first cut)

1. **Remove the weaponizable, fragmenting lockout entirely.** Delete
   `internal/user/lockout.go` (the Redis + in-memory + circuit-breaker
   `FallbackFailureCounter`), the `FailureCounter` interface, `SetFailureCounter`,
   and `Service.RecordFailedAttempt`. The `users.locked_until` column and
   `Store.Lock` survive as a dormant **manual/operator backstop** — no longer
   auto-fired by failure velocity.
2. **Introduce the `LoginGuard` seam** (`internal/user/loginguard.go`): a
   `Check(email, ip) → Decision` (pre-bcrypt) + `Record(email, ip, success)`
   interface. This is where the graduated ladder plugs in later without touching
   callers. `NoopLoginGuard` is the default (relies on the /v1/auth IP limiter).
3. **Ship one lean, non-weaponizable, non-throwaway layer behind it** — the
   panel's "L2 pre-bcrypt load-shed on the attacker dimension": `PostgresLoginGuard`,
   a fixed-window failed-login counter keyed on `HMAC(pepper, ip_prefix|account)`
   (migration 0152, `login_throttle`). Over the threshold, it short-circuits to
   the generic 401 **before** bcrypt. Keyed on `(IP-prefix × account)`, so it
   throttles a single source hammering one account but **cannot lock the owner
   out** (they arrive from a different prefix). Pepper reuses `VELOX_ENCRYPTION_KEY`
   (a dedicated key is a documented future hardening), falling back to plain
   SHA-256 in local dev with no key.
4. **Fix the two confirmed bugs by construction:** the plaintext-email key is
   gone with the Redis counter; the timing oracle (the `locked_until` check ran
   *before* bcrypt) is fixed by moving the backstop check *after* `VerifyPassword`
   in `Service.Authenticate`, so a locked account's response timing can't be
   distinguished.

The default threshold/window (10 fails per source-account per 15 min) are a lean
starting point, tunable; a shadow-mode labeling pass on real traffic sets the
real knobs before they matter.

## Deferred (documented, triggered)

Each is additive and does not require reworking the above:

- **MFA / step-up (TOTP homegrown, per ADR-014) and login-time breached-password**
  — the controls that actually stop distributed stuffing. *These are the highest
  security value and should lead the next phase.* Trigger: any real user traffic /
  a DP security review (ADR-011/014 named "a DP demands MFA/SSO").
- **The graduated ladder** (tarpit → CAPTCHA/PoW → step-up) behind the existing
  `Decision` seam — replaces the current allow/deny with softer rungs.
- **Aggregate credential-stuffing detection** (per-account fan-in HLL, per-IP
  spread, global-rate EWMA) as **alert-only** observability + runbooks.
- **The optional Redis accelerator** under the I1/I2 invariants, if the
  failed-login path ever becomes hot (it essentially never does for operator
  login).
- **A dedicated throttle pepper key** (separate from `VELOX_ENCRYPTION_KEY`), and
  **wiring `PostgresLoginGuard.SweepExpired` into the scheduler** (the table's
  only growth vector; negligible at operator-login volume, so deferred).

## Consequences

- **Removes a real DoS lever and a PII leak**, and eliminates the fragmentation
  class — a net security improvement even before the deferred controls land.
- **Honest reduction to be aware of:** single-account brute force from a
  *distributed* source (many IP-prefixes) is not throttled by the `(IP-prefix ×
  account)` dimension alone — that is what MFA + breached-password (deferred)
  stop, and no throttle can. The per-IP `/v1/auth` limiter remains the volumetric
  floor. This is stated, not hidden; do not present the throttle as adequate
  standalone protection until MFA lands.
- MANUAL_TEST FLOW A1's account-lockout steps are replaced by the throttle's
  observable behavior in the same PR.

### Known residuals (adversarially reviewed, tracked against the deferred ladder)

Inherent to a lean IP-prefix throttle — the reason the graduated ladder + MFA
above are the *real* fix, not this cut:

- **TRUST_PROXY precondition.** The non-weaponizability rests on the client IP
  being the real client's. Behind a proxy with `TRUST_PROXY` unset, every peer is
  the proxy → all clients collapse to one prefix → the throttle degrades toward a
  bare-account lock. Mitigated by a production WARN at boot; the real fix is
  correct deployment. **Do not run behind a proxy without `TRUST_PROXY`.**
- **Colocated-attacker collateral.** An attacker SHARING the owner's IP-prefix
  (same office NAT, CGNAT/mobile pool, VPN exit, cloud region) can hold the owner
  out ~15 min per 10 requests on the shared `(prefix × account)` subject. Narrower
  than the old any-email lock (needs colocation) but real; the deferred
  tarpit/CAPTCHA rung (a challenge the owner passes, not a block) is the fix.
- **IPv6 /64 granularity.** RIRs route /56–/48 to one customer, so a routed /56
  yields 256 /64s = 256× the guess budget against one email. `/64` is a tunable
  default; a coarser mask trades collateral for budget.
- **Concurrency burst (Check→Record TOCTOU).** Check and Record are separate
  statements, so a simultaneous burst can overshoot the threshold before an
  increment is observed — the throttle bounds steady rate, not a single burst.
  bcrypt cost-12 is the per-attempt floor; a pre-bcrypt concurrency cap is deferred.
- **Retention sweep not wired.** `SweepExpired` exists but is a deferred
  follow-up; at operator-login volume growth is negligible and stale rows are
  inert (ignored by the window predicate).
