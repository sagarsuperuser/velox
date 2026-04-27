# SOC 2 Trust Services Criteria — Velox Control Mapping

Companion to [audit-log-retention.md](../ops/audit-log-retention.md) and
[encryption-at-rest.md](../ops/encryption-at-rest.md). The audit-log
guide answers "what evidence do we keep, and for how long?"; the
encryption guide answers "what protects the evidence at rest?". This
guide answers "if an auditor showed up tomorrow, which Velox surface
satisfies which Trust Services Criterion, and where are the honest
gaps?"

Velox is **pre-launch and pre-SOC-2-audit** at the time this doc lands —
no Type 1 walkthrough has happened, no Type 2 review period has begun.
Treat this as audit-prep input, not an attestation. The mapping below
exists so that when a tenant or a future Velox-the-company decides to
go through SOC 2, the engineering work is already named, the gaps are
already enumerated, and the evidence pointers are already
copy-pasteable.

The Trust Services Criteria mapped here are the AICPA's Common
Criteria (CC) plus the relevant additional categories. References to
specific controls use the AICPA 2017 TSC numbering as updated through
the 2022 points-of-focus revision; cross-check against your auditor's
control matrix before signing anything.

## Table of contents

- [Scope and shared-responsibility model](#scope-and-shared-responsibility-model)
- [CC1 — Control Environment](#cc1--control-environment)
- [CC2 — Communication and Information](#cc2--communication-and-information)
- [CC3 — Risk Assessment](#cc3--risk-assessment)
- [CC4 — Monitoring Activities](#cc4--monitoring-activities)
- [CC5 — Control Activities](#cc5--control-activities)
- [CC6 — Logical and Physical Access](#cc6--logical-and-physical-access)
- [CC7 — System Operations](#cc7--system-operations)
- [CC8 — Change Management](#cc8--change-management)
- [CC9 — Risk Mitigation](#cc9--risk-mitigation)
- [Additional categories — A / C / PI / P](#additional-categories--a--c--pi--p)
- [Top gaps to close before a Type 1](#top-gaps-to-close-before-a-type-1)
- [Evidence index](#evidence-index)

---

## Scope and shared-responsibility model

SOC 2 is always scoped to a **system** — the bundle of infrastructure,
software, people, procedures, and data that delivers a defined service.
For Velox, the system has three distinct layers, each with a different
responsibility owner:

| Layer | What it covers | Who owns the controls |
|---|---|---|
| **Velox application** | The Go binary, the Postgres schema, the migration runner, the audit log, the encryption primitives, the dashboard frontend (`web-v2/`), the customer portal, the webhook delivery system, the billing scheduler. | Velox project (open-source maintainers + future Velox-the-company). |
| **Tenant deployment** | The specific install of Velox running in a tenant's infrastructure: the Kubernetes cluster or VM, the managed Postgres, the load balancer, the secrets store, the monitoring stack, the IAM policies. | The tenant operating Velox. |
| **Downstream services** | Stripe (payments), the operator's email provider for magic links, the operator's S3-compatible object store for audit archives, the operator's KMS / Secrets Manager. | Each respective vendor (sub-service organisation in SOC 2 terms). |

When Velox eventually pursues its own SOC 2, the **carve-out method**
applies to Stripe and to the tenant's hosting infrastructure: Velox
attests to the controls Velox owns, names the carved-out sub-service
organisations explicitly, and relies on each vendor's own SOC 2 Type 2
report to cover the rest. The scoping language in your engagement
letter must say so explicitly — auditors don't infer carve-outs.

This document maps controls Velox owns. For tenant-side and
downstream controls, the entries below say so and point at the
relevant operator-side runbook section.

### "Velox" the project vs Velox the eventual company

Velox is open-source. Multiple tenants run their own copy. There is no
single Velox cloud today. SOC 2 is an organisational attestation —
it's about the company that operates a system, not about a software
project. So the right question is **which entity is being audited**:

- A **tenant** running Velox on their own infrastructure is the
  organisation under audit. Their SOC 2 covers their service-delivery
  controls; Velox sits inside their system as a piece of software
  whose source they can read (open-source) and whose security
  properties they have inherited from this project. Their auditor
  will ask them to attest that the deployed version of Velox is
  configured according to its own posture (this doc + the operator
  runbooks). The "Velox-side" controls in the table below become
  **inherited controls** in the tenant's narrative.
- **Velox-the-future-hosted-product** would be the organisation under
  audit if and when a managed Velox cloud is offered. At that point
  this doc rolls into the operator's control narrative directly. The
  open-source code and this control mapping are the same, but the
  scope expands to include incident response, on-call rotation,
  background checks for support engineers, etc. — the human-process
  controls that an open-source project doesn't and can't have.

**This doc focuses on tenant-side use today.** Where a control is
clearly tenant-only (background checks, vendor risk management, etc.),
the row says "Tenant-owned" and points at the operator runbook
sections that exist; where a control has a Velox software surface but
also a tenant-side procedural surface, both are listed.

---

## CC1 — Control Environment

> **What CC1 is about.** The "tone at the top" — the entity's
> commitment to integrity and ethical values, board oversight, the
> formal organisational structure that makes the rest of the controls
> possible. CC1 is overwhelmingly a *human-process* control family.
> Software supports it but does not satisfy it.

### CC1.1 — Demonstrates Commitment to Integrity and Ethical Values

**What it requires.** A documented code of conduct, a way for
employees / contributors to raise concerns, and consequences when
values are violated.

**How Velox addresses it.**
- The repository ships a `CONTRIBUTING.md` that names the project's
  code-style and architecture rules. A `CODE_OF_CONDUCT.md` at the
  repo root references Contributor Covenant 2.1 verbatim with
  `conduct@velox.dev` as the reporting inbox and a separate
  `conduct-escalation@velox.dev` for concerns about the maintainer.
- Pull-request review is the integrity-enforcement surface: every
  change goes through GitHub PR with at least one reviewer. A
  `CODEOWNERS` file at the repo root assigns the maintainer as the
  default reviewer for every path, with comments outlining how the
  file should split per-domain ownership as contributors join.

**Gaps.**
- No documented escalation path for an integrity concern raised by a
  contributor beyond the conduct-escalation inbox.

**Auditor evidence requested.**
- The code of conduct file (when added).
- A sampling of PR review comments demonstrating reviewer engagement.
- For tenants: the tenant's own employee handbook and
  whistleblower-policy documentation. Velox the project does not
  cover this; the tenant's HR controls do.

### CC1.2 — Exercises Oversight Responsibility

**What it requires.** A board / leadership team that supervises
internal-control design.

**How Velox addresses it.**
- Tenant-owned today. Velox-the-project is open-source and has no
  board.
- Future Velox-the-company would document a security/risk committee
  cadence here.

**Gaps.** Wholly tenant-side.

### CC1.3 — Establishes Structure, Authority, and Responsibility

**What it requires.** A documented org structure with clear
responsibility for security and compliance.

**How Velox addresses it.**
- Tenant-owned today. The tenant operating Velox names their own
  CISO / security lead; that role is the contact named to Velox's
  auditors.

**Gaps.** Tenant-side.

### CC1.4 — Demonstrates Commitment to Competence

**What it requires.** Evidence that the people designing and operating
controls have the relevant skills.

**How Velox addresses it.**
- Code-review gate (PR + CI in `.github/workflows/ci.yml`) provides
  some assurance that code changes meet a quality bar before landing.
- The architecture decisions are written down (`docs/adr/`) so the
  project's design rationale is auditable.
- Tenant-owned for staff competency: the tenant's HR controls cover
  hiring, onboarding, and continued education for the engineers
  operating Velox.

**Gaps.** Tenant-side.

### CC1.5 — Enforces Accountability

**What it requires.** Performance reviews, disciplinary processes when
controls fail.

**How Velox addresses it.**
- Tenant-owned. Velox does not have employees.

**Gaps.** Tenant-side.

---

## CC2 — Communication and Information

> **What CC2 is about.** How the entity obtains, generates, and uses
> relevant information; how it communicates internally and with
> external parties (customers, auditors, regulators).

### CC2.1 — Uses Relevant Information

**What it requires.** Quality information drives the operation of
controls.

**How Velox addresses it.**
- Structured logs (`log/slog`) on every request and every background
  job. Log lines carry tenant_id, request_id, key fields for
  correlation.
- Metrics at `GET /metrics` Prometheus format — see the metrics
  inventory in [`docs/ops/runbook.md`](../ops/runbook.md#metrics-inventory).
- Audit log table with the schema documented in
  [`docs/ops/audit-log-retention.md`](../ops/audit-log-retention.md#whats-in-the-audit-log).
  Mutations on customer-affecting resources land here, with append-only
  enforcement at the database trigger level
  (`internal/platform/migrate/sql/0011_audit_append_only.up.sql`).

**Gaps.**
- The application log is JSON but not yet centrally aggregated by
  default — the operator wires it into their SIEM. A reference
  Filebeat / Vector / OpenTelemetry config is a documentation gap to
  close before the first SOC 2 readiness review.

**Auditor evidence requested.**
- The metrics dashboard screenshots (Grafana or whatever the operator
  uses).
- An audit_log row sample for an arbitrary mutating action during the
  review period.

### CC2.2 — Communicates Internally

**What it requires.** Internal personnel receive information about
their control responsibilities.

**How Velox addresses it.**
- Tenant-owned for the operator team's internal comms.
- Velox's contribution: the runbook
  ([`docs/ops/runbook.md`](../ops/runbook.md)) is the
  knowledge-transfer artifact for on-call engineers; the
  per-domain READMEs and the ADRs in `docs/adr/` are the design-rationale
  artifacts.

**Gaps.** Tenant-side procedural.

### CC2.3 — Communicates Externally

**What it requires.** A way for external parties to communicate
relevant information; commitments and responsibilities are made
explicit.

**How Velox addresses it.**
- **Public changelog** at `web-v2/src/pages/Changelog.tsx` (renders at
  the dashboard `/changelog` route) and the engineering-facing
  `CHANGELOG.md` at the repo root. Both are updated on every
  user-visible ship — see the changelog discipline note in the project
  memory and the existing entries documenting Week 10 doc rollouts.
- **GitHub Issues** at <https://github.com/sagarsuperuser/velox/issues>
  is the public bug-and-feature-request surface.
- **Status page**: not yet implemented. The runbook mentions a status
  page in passing (SEV-1 SLA references "status page update within 15
  min") but no status page is wired up. Listed in gaps.
- **Security disclosure**: a `SECURITY.md` at the repo root names
  `security@velox.dev` as the reporting inbox, sets a 5-business-day
  triage commitment + 30-day patch-landing target for high/critical,
  enumerates an explicit in-scope / out-of-scope list (the binary,
  schema, dashboard, deploy artifacts, encryption-at-rest +
  audit-log + RLS guarantees in scope; operator deploy environment +
  third-party services + DoS via traffic flooding out of scope), and
  carries a safe-harbor clause for good-faith research. No PGP key
  is published yet — age is mentioned as the future encrypted path
  once a maintainer key is published at `velox.dev/.well-known/age.txt`.

**Gaps.** Listed inline; bundled into the "top gaps" section at the
end.

**Auditor evidence requested.**
- The changelog (both the markdown file and the rendered TSX page).
- Any contractual commitments to external parties — the tenant's
  Terms of Service / DPA. Velox-the-project doesn't have these; the
  tenant does.

---

## CC3 — Risk Assessment

> **What CC3 is about.** The entity identifies, analyses, and responds
> to risks to the achievement of its objectives. SOC 2 wants to see
> that the entity *thought* about what could go wrong, not just that
> they ship working software.

### CC3.1 — Specifies Suitable Objectives

**What it requires.** Documented service objectives — availability,
correctness, latency, security.

**How Velox addresses it.**
- [`docs/ops/sla-slo.md`](../ops/sla-slo.md) — the SLO definitions
  for HTTP availability, billing-cycle correctness, payment success
  rate, etc.
- [`docs/90-day-plan.md`](../90-day-plan.md) — the project's
  near-term objectives. Closes once Velox-the-company starts
  producing quarterly OKRs.

**Gaps.** None code-level. Tenant adds their own customer-facing
SLAs.

### CC3.2 — Identifies Risks

**What it requires.** A risk register or equivalent — what threats
exist, what's the impact.

**How Velox addresses it.**
- [`docs/migration-safety-findings.md`](../migration-safety-findings.md)
  — operational risk register for the migration runner
  (lock-acquisition risks measured at three load presets, mitigations
  documented).
- [`docs/phase2-hardening-plan.md`](../phase2-hardening-plan.md) — the
  hardening risk inventory.
- ADRs in `docs/adr/` carry "consequences" sections that name the
  trade-offs of each architectural decision.

**Gaps.**
- No formal **threat model** doc (STRIDE / LINDDUN / ATT&CK-mapped).
  The auditor will ask for this.
- No documented annual risk-review cadence.

### CC3.3 — Considers Fraud Potential

**What it requires.** The entity evaluates ways fraud could occur
within its system.

**How Velox addresses it.**
- The audit log is the primary control: every mutating action carries
  an `actor_id`, `ip_address`, `request_id` — see
  [`audit-log-retention.md`](../ops/audit-log-retention.md#whats-in-the-audit-log).
  Append-only enforcement at the trigger level
  (`internal/platform/migrate/sql/0011_audit_append_only.up.sql`)
  means a compromised application path cannot rewrite history.
- Per-tenant fail-closed audit posture
  (`tenant_settings.audit_fail_closed`) lets a regulated tenant
  refuse the request when the audit insert fails — see
  [migration `0010_audit_fail_closed`](../../internal/platform/migrate/sql/0010_audit_fail_closed.up.sql).
- API key types are constrained: publishable keys are read-only by
  permission policy in `internal/auth/permission.go`, so a
  pasted-into-a-public-website publishable key cannot mint
  subscriptions or invoices.

**Gaps.**
- No anomaly detection on audit log volumes today (a sudden spike in
  `gdpr.delete` actions, for example, should page someone — it
  doesn't yet).
- No explicit segregation-of-duties policy beyond "publishable keys
  are read-only" — high-blast-radius write operations are not
  multi-party today.

### CC3.4 — Identifies and Assesses Changes

**What it requires.** Changes to the business and the technology
landscape are evaluated for new risks.

**How Velox addresses it.**
- Each ADR in `docs/adr/` was written precisely for this — see
  ADR-006 ("background scheduler vs message queue") for an example of
  a reasoned change evaluation.
- The 90-day plan is reviewed at end-of-quarter (per the project
  cadence in `docs/90-day-plan.md`).

**Gaps.**
- No documented "every quarter, here's what changed in our risk
  posture" review artefact.

---

## CC4 — Monitoring Activities

> **What CC4 is about.** Ongoing and separate evaluations of whether
> controls are operating effectively. SOC 2 distinguishes between
> *ongoing* (always-on, automated) and *separate* (periodic,
> human-driven) monitoring; both count.

### CC4.1 — Performs Ongoing and Separate Evaluations

**Ongoing (automated).**
- Prometheus metrics at `GET /metrics` —
  [`docs/ops/runbook.md`](../ops/runbook.md#metrics-inventory) lists
  the full surface: HTTP, billing engine, payments, webhooks, dunning,
  credit, audit, background cleanup. Every counter/gauge/histogram is
  scrapeable and graphable.
- Health checks: `GET /health` (liveness) and `GET /health/ready`
  (readiness — DB reachable + scheduler not stalled). See
  [`docs/self-host.md`](../self-host.md#health-checks).
- Audit log per request — every mutating 2xx writes a row via
  `internal/api/middleware/audit.go::AuditLog`.
- CI gate on every PR (`.github/workflows/ci.yml`): unit tests with
  race detector, integration tests with Postgres, `go vet`,
  `go mod verify`, `govulncheck` against the dependency graph.

**Separate (periodic).**
- Quarterly verification of encryption-at-rest using the recipes in
  [`docs/ops/encryption-at-rest.md`](../ops/encryption-at-rest.md#verification-recipes).
- Quarterly restore drill of the Postgres backup —
  [`docs/ops/backup-recovery.md`](../ops/backup-recovery.md).
- Audit log retention review — operator confirms the
  [retention policy](../ops/audit-log-retention.md#compliance-baseline-retention)
  matches the regimes their tenants are subject to.

**Gaps.**
- No central security-event SIEM by default. The application log is
  structured JSON; the tenant wires the aggregator. Reference
  config (Vector / Filebeat / OpenTelemetry collector) is a doc gap.
- No annual penetration test (organisationally, this is a Velox-as-a-
  company future activity once an external interface exists for a
  tester to point at).

**Auditor evidence requested.**
- Metric dashboard screenshots covering the review period.
- The most recent quarterly encryption-verification recipe output.
- The most recent restore-drill log.
- CI run logs from any PR in the review period.

### CC4.2 — Communicates Deficiencies

**What it requires.** Deficiencies are communicated to the parties
responsible for taking corrective action.

**How Velox addresses it.**
- Alerts route to PagerDuty / Slack / email per the alert catalog in
  [`docs/ops/runbook.md`](../ops/runbook.md#alert-catalog). Severities
  are documented (SEV-1 page, SEV-2 ticket, SEV-3 info) with response
  times.
- Post-mortem template at the bottom of the runbook
  ([`runbook.md` → Post-mortem template](../ops/runbook.md#post-mortem-template))
  — saved as `docs/postmortems/YYYY-MM-DD-short-slug.md`, published
  within 5 business days of any SEV-1 or multi-tenant SEV-2.

**Gaps.** None at the Velox software layer. Tenant wires the actual
PagerDuty / Slack endpoints.

---

## CC5 — Control Activities

> **What CC5 is about.** The policies and procedures that mitigate
> identified risks — including selection and development of control
> activities, the use of technology in control activities, and the
> deployment of policies and procedures.

### CC5.1 — Selects and Develops Control Activities

**How Velox addresses it.**
- Per-domain package architecture (`internal/{domain}/`) with zero
  cross-domain imports (ADR-002) means each domain owns its own
  validation, store, and handler controls. Bugs are local; control
  failures are local.
- Store interfaces enforce the contract; in-memory fakes drive unit
  tests; real Postgres drives integration tests.
- Every store method runs inside an RLS-scoped transaction —
  `db.BeginTx(ctx, postgres.TxTenant, tenantID)` (see
  `internal/platform/postgres/postgres.go:45`). The tenant boundary is
  enforced at the database, not just the application.
- `TxBypass` exists for legitimate cross-tenant operations (e.g.
  bootstrap, scheduler) and is grep-auditable —
  `internal/platform/postgres/postgres.go:46`. Reviewers know to
  scrutinise every `TxBypass` site.

### CC5.2 — Selects and Develops General Controls Over Technology

**How Velox addresses it.**
- Branch protection on `main` requires PR review + green CI before
  merge. CI defined at `.github/workflows/ci.yml`: build, race-mode
  unit tests, integration tests with Postgres 16, `go vet`,
  `go mod verify`, `govulncheck`.
- Migration runner enforces ordered, idempotent schema evolution —
  `internal/platform/migrate/migrate.go`. See CC8.1 below.
- Vulnerability scanning via `govulncheck` on every CI run is a
  hard CI gate (the `continue-on-error: true` qualifier was removed
  in the SOC 2 cheap-gap closeout; a stdlib or dependency vulnerability
  with a known fix now blocks merges).

**Gaps.**
- No SAST (static application security testing) tool (e.g. Semgrep,
  CodeQL) wired into CI. Recommended addition.
- No dependency-update bot (Dependabot / Renovate). Recommended.

### CC5.3 — Deploys Policies and Procedures

**How Velox addresses it.**
- Policies live in `docs/`. Procedures live in `docs/ops/`. The
  operator runbook ([`runbook.md`](../ops/runbook.md)) is the
  procedure-of-record for incident response.
- ADRs (`docs/adr/`) document binding architectural decisions —
  changing one means writing a new ADR that supersedes it, not
  silently flipping behaviour.

**Gaps.** Tenant wires their internal policy review cadence.

---

## CC6 — Logical and Physical Access

> **What CC6 is about.** The big one for an API product. CC6 covers
> access provisioning, removal, authentication, authorization,
> encryption, network security, physical security. Most SOC 2
> findings cluster here.

### CC6.1 — Implements Logical Access Security Software, Infrastructure, and Architectures

**What it requires.** Authentication and authorisation mechanisms
restrict access to authorised users only.

**How Velox addresses it.**
- **API key authentication.** Three key types defined in
  `internal/auth/permission.go`:
  - **Platform** (`vlx_platform_<mode>_…`) — tenant management
    operations only.
  - **Secret** (`vlx_secret_<mode>_…`) — full tenant access
    (server-side only).
  - **Publishable** (`vlx_pub_<mode>_…`) — read-only, browser-safe
    (intentionally narrow permission set).
  - **Session** (cookie-based) — dashboard UI sessions, full
    permission set today (will become role-scoped when invites
    ship). See `internal/session/middleware.go`.
- **Key generation** — `internal/auth/service.go::CreateKey`. Raw
  key shown exactly once; storage is `SHA-256(salt || rawKey)` with a
  16-byte per-key salt. Lookup is by key prefix (12 chars) + constant-
  time hash comparison.
- **Permission enforcement** — `auth.Require(perm)` middleware in
  `internal/auth/middleware.go`. Every protected route declares its
  required permission; route registration in `internal/api/router.go`.
- **Tenant isolation** — Postgres Row-Level Security (RLS) with
  `FORCE ROW LEVEL SECURITY` on every tenant-scoped table (66 tables,
  enumerated in `internal/platform/migrate/sql/0001_schema.up.sql`
  starting at line 680). The `velox_app` runtime role is non-superuser
  so the FORCE policies cannot be bypassed at the application layer
  even if it tried — see `deploy/compose/postgres-init.sql` for the
  role split.
- **Livemode separation** — every API key carries an explicit
  livemode flag; `BeginTx` records the mode on the tx session and the
  RLS policy filters rows by mode in addition to tenant_id. See
  `internal/platform/postgres/postgres.go:62-78`. Test-mode and
  live-mode data are mutually invisible at the database layer.
- **Dashboard user authentication** — passwords stored as Argon2id
  PHC strings (m=64MiB, t=3, p=4, 16-byte salt, 32-byte key) per
  OWASP 2024. See `internal/user/password.go`.
- **Sessions** — cookie carries the raw token; DB stores the SHA-256
  hash. A DB snapshot cannot be replayed as a bearer token. See
  `internal/session/token.go:16-31`.
- **Magic links** (customer-portal) — single-use, 15-minute TTL,
  SHA-256 of the random `vlx_cpml_…` token stored. Migration
  `0024_customer_portal_magic_links`.

**Evidence pointers.**
- Permission policy: `internal/auth/permission.go:72-127`.
- API key validation: `internal/auth/service.go::ValidateKey`.
- RLS policies: `internal/platform/migrate/sql/0001_schema.up.sql`
  (search for `FORCE ROW LEVEL SECURITY`).
- RLS isolation tests: `internal/platform/postgres/rls_isolation_test.go`.

**Gaps.**
- No multi-factor authentication on dashboard login today. Tracked
  for the WorkOS / Clerk integration noted in `CLAUDE.md`. Auditor
  will flag this for any tenant whose own commitments require MFA.
- No SSO (SAML / OIDC) — same WorkOS dependency.
- API key scopes are coarse (read/write per resource family). No
  fine-grained scope pinning yet (e.g. "this key can charge but not
  refund").

### CC6.2 — Registers and Authorizes New Internal and External Users

**What it requires.** New users go through a documented
registration / approval flow before access is granted.

**How Velox addresses it.**
- **Tenant-side admin users.** Self-serve via the dashboard signup
  flow; the bootstrap user becomes an "owner" of the tenant. Member
  invitations land via `internal/user/` invite flows (migration
  `0035_member_invitations`).
- **API keys** — only an authenticated, permission-bearing user can
  mint a new key (`POST /v1/api-keys` requires `apikey:write`). Every
  creation and rotation is audit-logged with action `create` /
  `rotate` and `resource_type = api_key`.
- **Customer-portal end users** — magic-link-only, no password. The
  tenant decides who gets a portal session by minting a magic link
  via the API or by the customer requesting one against an existing
  email-bidx-resolvable account.

**Gaps.**
- No approval workflow for new dashboard users (the first user
  becomes owner; subsequent invitees are accepted by the owner; no
  multi-approver gate). Tenant-side hardening if their CC6.2 control
  narrative requires multi-approval.

### CC6.3 — Authorizes, Modifies, or Removes Access

**What it requires.** Access is removed when no longer needed; changes
are tracked.

**How Velox addresses it.**
- **API key revocation** — `DELETE /v1/api-keys/{id}` immediately
  revokes (sets `revoked_at`); `POST /v1/api-keys/{id}/rotate`
  with `expires_in_seconds: 0` is the same effect under a different
  endpoint.
  See [`docs/ops/api-key-rotation.md`](../ops/api-key-rotation.md).
- **Session revocation** — server-side: deleting the row in
  `sessions` invalidates the cookie immediately on the next request.
- **Audit trail** — every revoke / rotate writes a row to
  `audit_log` (action `revoke` / `rotate`, resource_type `api_key`).
  See [`docs/ops/audit-log-retention.md`](../ops/audit-log-retention.md#what-events-are-recorded-today).

**Gaps.**
- No automated dormant-key sweeper. A long-unused API key sits
  forever; the operator has to spot it manually. Recommended:
  `velox_scheduled_cleanup` extension that flags keys with
  `last_used_at` older than a configurable window.
- No central "user X is leaving, revoke their access everywhere"
  workflow — the operator manually revokes per-domain. Tracked.

**Auditor evidence requested.**
- A sample audit_log row for a `rotate` action on `api_key` from the
  review period.
- The dormant-key sweep policy (when implemented).

### CC6.4 — Restricts Physical Access

**What it requires.** Physical access to facilities / hardware /
storage is restricted.

**How Velox addresses it.**
- **Tenant-owned and downstream.** Velox runs on the tenant's
  infrastructure; physical security is the cloud provider's
  responsibility (AWS / GCP / Azure SOC 2 reports cover this) or the
  tenant's own datacentre / office controls. The Velox project owns
  no hardware.

**Auditor evidence requested.** The cloud provider's SOC 2 Type 2
report; the tenant's facility controls.

### CC6.5 — Discontinues Logical and Physical Protections Over Physical Assets

**What it requires.** Decommissioned hardware doesn't leak data.

**How Velox addresses it.**
- Tenant-owned and downstream.

### CC6.6 — Implements Logical Access Security Measures Against Threats from Outside the System

**What it requires.** Boundary protections — TLS, firewalls, DDoS
protection.

**How Velox addresses it.**
- **TLS termination** — operator's responsibility. Velox listens on
  plain HTTP inside the trusted network; the operator's load
  balancer / Cloudflare / certbot terminates TLS. Documented at
  [`docs/self-host.md`](../self-host.md#tls).
- **HSTS** — `Strict-Transport-Security: max-age=31536000;
  includeSubDomains` on every non-local response. See
  `internal/api/middleware/security.go:18`.
- **Other security headers** — `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Cache-Control: no-store`,
  `Referrer-Policy: strict-origin-when-cross-origin`. Same file,
  lines 21-31.
- **Rate limiting** — distributed via Redis-backed GCRA in
  `internal/api/middleware/ratelimit.go`. Per-API-key bucket; falls
  back to per-tenant for session traffic, per-IP for unauthenticated
  flows. Configurable `failClosed` for production
  (`SetFailClosed(true)` returns 429 when Redis is unreachable rather
  than letting the deployment become a DDoS target).
- **CORS** — restrictive by default; the operator's `CORS_ORIGINS` env
  var enumerates allowed origins. `Access-Control-Allow-Credentials:
  true` so session cookies work, which forces a non-wildcard
  `Access-Control-Allow-Origin` per spec. See
  `internal/api/middleware/cors.go`.
- **Webhook signature verification (inbound Stripe)** — HMAC-SHA256
  with a constant-time comparison and a 5-minute timestamp tolerance.
  See `internal/webhook/service.go:444` for the signing primitive.
- **Webhook signature signing (outbound to tenants)** — same HMAC
  primitive with `secret_encrypted` (AES-256-GCM under
  `VELOX_ENCRYPTION_KEY`) and a `secondary_secret_encrypted` for
  rotation grace. See migration `0019_webhook_secret_encrypt` /
  `0049_webhook_secondary_secret`.

**Gaps.**
- No application-layer WAF rules. The expectation is that the
  operator's CDN (Cloudflare / AWS WAF) provides this.
- The `CORS_ORIGINS` default in dev is permissive — production
  config must set this. The runbook calls this out but it's a
  configuration discipline, not a code-enforced gate.

### CC6.7 — Restricts the Transmission, Movement, and Removal of Information

**What it requires.** Data in transit is protected; data at rest is
encrypted.

**How Velox addresses it.**
- **In transit.** TLS at the operator boundary (CC6.6 above).
- **At rest, application layer.** AES-256-GCM on customer email +
  display name, billing-profile legal name + email + phone + tax
  id, outbound webhook signing secrets (primary + secondary), and
  per-tenant Stripe credentials. HMAC-SHA256 blind index on
  `customers.email` for deterministic equality lookup without
  decrypting. Detailed mapping in
  [`docs/ops/encryption-at-rest.md`](../ops/encryption-at-rest.md#whats-encrypted).
- **At rest, storage layer.** Tenant-owned. AWS RDS encryption,
  GCP Cloud SQL CMEK, self-hosted LUKS on the data volume —
  verified per [`docs/ops/encryption-at-rest.md` § Verification
  Recipes step 7](../ops/encryption-at-rest.md#7-postgres-level-disk-encryption-storage-layer).
- **Hashing** for one-way protected assets — Argon2id passwords,
  SHA-256 sessions / API keys / magic-link tokens / payment-update
  tokens. See
  [`docs/ops/encryption-at-rest.md` § What's hashed](../ops/encryption-at-rest.md#whats-hashed-not-encrypted).

**Gaps.**
- **Key rotation is not implemented today** for either
  `VELOX_ENCRYPTION_KEY` or `VELOX_EMAIL_BIDX_KEY`. Documented
  honestly at
  [`encryption-at-rest.md` → Key rotation: NOT IMPLEMENTED](../ops/encryption-at-rest.md#key-rotation-not-implemented-today).
  This is the single largest CC6.7 gap and will be a finding for any
  strict auditor. The proper fix is envelope encryption (DEK/KEK
  split) — same pattern Stripe / AWS KMS / GCP KMS use; tracked as
  long-term work.

**Auditor evidence requested.**
- Output of the verification recipes from `encryption-at-rest.md`
  on the running install.
- The KMS key id (or LUKS verification output) for the storage
  layer.
- The audit_log entries for any `rotate` action on
  `webhook_endpoints` / `api_keys` during the review period.

### CC6.8 — Prevents or Detects and Acts Upon Unauthorized or Malicious Software

**What it requires.** Defenses against malware, malicious code paths,
unauthorised software running on production systems.

**How Velox addresses it.**
- **Dependency scanning** — `govulncheck` is a hard CI gate
  (`.github/workflows/ci.yml`). The `continue-on-error: true`
  qualifier was removed in the SOC 2 cheap-gap closeout; a stdlib or
  module vulnerability with a known fix blocks PR merges.
- **Module integrity** — `go mod verify` in CI catches a tampered
  `go.sum`.
- **Container provenance** — images publish from CI to GHCR
  (`ghcr.io/sagarsuperuser/velox`); operators can pin tag SHAs
  rather than `:latest`.

**Gaps.**
- No image signing (cosign / Sigstore) — recommended.
- No runtime attestation (the K8s admission controller doesn't
  verify image signatures by default).
- No SAST in CI today (Semgrep / CodeQL would close this).

---

## CC7 — System Operations

> **What CC7 is about.** The operational disciplines — system
> monitoring, incident response, backup and recovery, change
> management at the operational level (vs CC8 which is about *change*
> as such).

### CC7.1 — Detects Configuration Changes

**What it requires.** Configuration changes are detected; deviation
from a known-good baseline triggers investigation.

**How Velox addresses it.**
- Schema drift is detected by the migration runner: every binary
  start checks `schema_migrations` against the embedded migration
  set. A deployment running stale schema can't quietly serve traffic
  with the wrong shape — see `internal/platform/migrate/migrate.go`.
- The application logs every config decision at startup
  (`encryption at rest enabled`, `rate limiter: redis configured`,
  etc.) so an operator pulling logs can verify the running posture
  matches the intended one.

**Gaps.**
- No file-integrity-monitoring (FIM) on the binary itself or on the
  Postgres data directory. Tenant-side; commodity tools (auditd,
  AIDE) cover this for self-hosted deployments.

### CC7.2 — Monitors System Components and Detects Anomalies

**Already covered substantially in CC4.1 above.**
- Prometheus metrics surface, alert catalog with SLO-tied alerts,
  audit log table.
- The `VeloxAuditWriteErrors` alert
  ([runbook → Audit & compliance metrics](../ops/runbook.md#audit--compliance))
  fires when audit log INSERT failures spike — a SEV-1 for
  fail-closed tenants because their requests are being rejected.

### CC7.3 — Evaluates Security Events for Impact

**What it requires.** When something fires, someone evaluates whether
it's a security incident vs ordinary noise.

**How Velox addresses it.**
- Severity classification rules — runbook
  [§ Severity definitions](../ops/runbook.md#severity-definitions).
- Triage runbooks per alert — runbook
  [§ Alert catalog](../ops/runbook.md#alert-catalog).
- Post-mortem process — runbook
  [§ Post-mortem template](../ops/runbook.md#post-mortem-template).

### CC7.4 — Responds to Identified Security Incidents

**What it requires.** A documented, exercised incident response plan.

**How Velox addresses it.**
- Incident playbooks at
  [`docs/ops/runbook.md`](../ops/runbook.md#incident-playbooks).
- Communication template at
  [`runbook.md` → Communication](../ops/runbook.md#communication).
- Rollback procedures at
  [`runbook.md` → Rollback procedures](../ops/runbook.md#rollback-procedures).
- Backup & restore drill at
  [`docs/ops/backup-recovery.md`](../ops/backup-recovery.md), with
  RPO=5 min and RTO=1 hour targets.

**Gaps.**
- No quarterly tabletop exercise schedule — operator should add one.
- No customer-notification template for confirmed breaches —
  recommended addition to the runbook before SOC 2 readiness.

### CC7.5 — Identifies and Develops Activities to Recover from Identified Security Incidents

**What it requires.** Recovery procedures exist and are tested.

**How Velox addresses it.**
- WAL-archive + base-backup recipe in
  [`docs/ops/backup-recovery.md`](../ops/backup-recovery.md).
- The Postgres restore drill is described as a quarterly exercise.
- Audit log restore from S3 archive into a side `audit_log_archive`
  table — see
  [`audit-log-retention.md` → Restore process](../ops/audit-log-retention.md#restore-process).

**Auditor evidence requested.**
- Quarterly restore-drill log.
- Sample post-mortem from the review period (if any incident
  occurred — if not, "no SEV-1 incidents during the review period"
  is itself an evidence statement).

---

## CC8 — Change Management

> **What CC8 is about.** Changes to the system are authorised,
> tested, approved, and documented before they reach production.

### CC8.1 — Authorizes, Designs, Develops, Configures, Documents, Tests, Approves, and Implements Changes

**How Velox addresses it.**
- **Authorisation.** Branch-protection on `main` requires PR review;
  no direct pushes.
- **Design.** Larger features have ADRs (`docs/adr/`) or
  design docs in `docs/design-*.md` written and reviewed before
  the implementation PR lands.
- **Development.** Per-domain package architecture means changes
  are bounded — see ADR-002. Cross-domain effects require a narrow
  interface change which forces visible review across the affected
  packages.
- **Configuration.** All runtime configuration via env vars
  (12-factor; see [`secrets-management.md`](../ops/secrets-management.md)).
  Config validation at startup — `internal/config/config.go::validateFatal`
  refuses to start with unsafe combinations
  (e.g., `APP_ENV=production` without `VELOX_ENCRYPTION_KEY`).
- **Documentation.** `CHANGELOG.md` updated on every
  user-visible ship; customer-facing rollups in
  `web-v2/src/pages/Changelog.tsx`. The project's "changelog
  discipline" is documented memory.
- **Testing.** Unit tests + integration tests, race detector enabled
  in CI. Every domain has both an in-memory store and a real-Postgres
  variant. RLS-isolation tests in
  `internal/platform/postgres/rls_isolation_test.go` lock down the
  tenant-isolation invariant directly.
- **Approval.** PR review, status checks, and CI green required for
  merge.
- **Implementation.** Zero-downtime migration runner with explicit
  no-transaction support for `CREATE INDEX CONCURRENTLY` —
  `internal/platform/migrate/migrate.go`. Migration safety findings
  documented at
  [`docs/migration-safety-findings.md`](../migration-safety-findings.md).

**Schema drift detection.** Migration runner is the source of truth.
The embedded migration set (`internal/platform/migrate/sql/*.sql`)
plus the `schema_migrations` table together define the expected
schema for a given binary version. Drift between deployed binary and
deployed schema causes a startup error.

**Migration rollback.** Every `*.up.sql` has a paired `*.down.sql`.
The migration roundtrip test
(`internal/platform/migrate/roundtrip_test.go`) exercises full up →
down → up across the embedded set on every CI run, so a
non-reversible migration cannot land.

**Gaps.**
- No formal CAB (Change Advisory Board) process — for an
  open-source project this is over-engineering, but Velox-the-future-
  company would want one for production-impacting changes. PR review
  is the closest analogue today.
- No automated rollback on canary failure (the operator deploys are
  blue/green or rolling per their own deploy tooling — Velox doesn't
  prescribe).

**Auditor evidence requested.**
- A sample PR from the review period showing review + CI green.
- The CI workflow definition (`.github/workflows/ci.yml`).
- The migration set + the `schema_migrations` rows in production.

---

## CC9 — Risk Mitigation

> **What CC9 is about.** Vendor management, business continuity,
> insurance — risks the entity has chosen to mitigate by transferring
> rather than absorbing.

### CC9.1 — Identifies, Selects, and Develops Risk Mitigation Activities

**How Velox addresses it.**
- The most significant inherited risk is **Stripe**. Velox's
  PaymentIntent-only pattern (ADR-001) means Stripe holds the card,
  Stripe owns PCI compliance scope for cardholder data. The
  trade-off: Stripe being unavailable is the same as Velox being
  unable to charge. Mitigation: circuit breaker on outbound Stripe
  calls (`velox_stripe_breaker_state` metric in the runbook); per-
  invoice retries via the dunning loop; retry-with-backoff on
  transient failures.
- **DDoS mitigation** is partly transferred to the operator's CDN.
  Velox's rate limiter is the second line of defense.
- **Backup risk** transferred to the operator's S3 / Glacier with
  the lifecycle policy described in audit-log-retention.

### CC9.2 — Assesses and Manages Risks Associated with Vendors and Business Partners

**Vendor inventory.**

| Vendor | Velox dependency | Risk | Mitigation |
|---|---|---|---|
| **Stripe** | Card processing, PaymentIntent confirmation, webhook source | Stripe outage means no charges; Stripe API change could break Velox; PCI scope flow-through. | PaymentIntent-only pattern (Velox holds tokens, never PANs); circuit breaker; per-tenant credentials in `stripe_provider_credentials` so an issue with one tenant's Stripe account doesn't cascade. Stripe's own SOC 2 Type 2 report is the carve-out reliance. |
| **PostgreSQL** | Primary datastore | DB outage = full outage. | Streaming replica + PITR per backup-recovery.md. Operator-side: pick a managed Postgres with a vendor SLA, or self-host with a tested HA setup. |
| **Redis** | Distributed rate limiting | Outage degrades rate limiting; default is fail-open in dev, recommended fail-closed in prod (DDoS protection). | Rate limiter `SetFailClosed(true)` for production; capacity planning documented. |
| **GHCR** (GitHub Container Registry) | Image distribution | Outage blocks new deploys but not running services. | Operators pin image SHAs locally; can mirror to a private registry. |
| **External Secrets Operator + KMS / Vault / AWS Secrets Manager** | Env-var delivery | Compromise = secret exposure; outage = pod restart fails. | Standard 12-factor pattern with operator-owned RBAC. |
| **WorkOS / Clerk** (planned) | User auth (UI login) | Future dependency for SSO / MFA. | Decision documented in `CLAUDE.md`; not yet integrated. |

**Sub-service-organisation reliance.** When Velox does its own SOC 2,
Stripe and the underlying cloud provider are sub-service organisations
under the **carve-out method**. Both maintain their own SOC 2 Type 2
reports; Velox-the-company would obtain those, review the user-control
considerations (UCCs), and document the controls Velox implements to
fulfil its end of the UCCs.

**Gaps.**
- No formal vendor risk-review cadence today. Annual review of each
  vendor's SOC 2 / DPA / breach history is the SOC 2 baseline.
  Tenant-side procedural addition.
- No DPIA (Data Protection Impact Assessment) for processing in
  the GDPR sense — Velox aggregates personal data; the tenant is the
  controller and owes the DPIA. Velox provides the technical
  measures that go into it (pseudonymisation via blind index,
  encryption-at-rest, audit log).

**Auditor evidence requested.**
- Stripe's most recent SOC 2 Type 2 report.
- The cloud provider's most recent SOC 2 Type 2 report.
- The vendor risk-review log (when implemented).

---

## Additional categories — A / C / PI / P

The Trust Services Criteria include four optional categories beyond
the Common Criteria. SOC 2 reports name which categories are in scope;
Velox's posture below is "Common Criteria + Confidentiality" as the
baseline, with Availability and Processing Integrity being natural
fits given the workload, and Privacy applicable for tenants whose
contracts require it.

### Availability (A series)

> The system is available for operation and use as committed.

- **A1.1 — Maintains, monitors, and evaluates current processing
  capacity.** Capacity-planning doc at
  [`docs/ops/capacity-planning.md`](../ops/capacity-planning.md).
  Sizing baselines in [`self-host.md` → Sizing](../self-host.md#sizing).
- **A1.2 — Recovery procedures.** Backup + restore at
  [`backup-recovery.md`](../ops/backup-recovery.md). RPO=5 min,
  RTO=1 hour.
- **A1.3 — Tests recovery plan procedures.** Quarterly restore drill;
  the operator's responsibility to keep the cadence.

### Confidentiality (C series)

> The system protects information designated as confidential.

- **C1.1 — Identifies and maintains confidential information to meet
  the entity's objectives.** Data classification implicit in the
  "what's encrypted / hashed / plaintext on purpose" tables of
  [`encryption-at-rest.md`](../ops/encryption-at-rest.md).
  An explicit data-classification doc is a small follow-up — listed
  in gaps.
- **C1.2 — Disposes of confidential information.** GDPR right-to-
  erasure flow at `internal/customer/gdpr.go` writes a
  `gdpr.delete` audit row and anonymises the customer's PII fields.
  See migration `0050_customer_email_status` for the email tombstone.
  Audit-log archive lifecycle (Glacier → expiration at 7 years)
  documented at
  [`audit-log-retention.md` → Archival to S3](../ops/audit-log-retention.md#archival-to-s3).

### Processing Integrity (PI series)

> System processing is complete, valid, accurate, timely, and
> authorised to meet the entity's objectives.

- **PI1.1 — Inputs.** Validation middleware
  (`internal/api/middleware/validate.go`); per-domain handler
  validation with `errs.Required` / `errs.Invalid`.
- **PI1.2 — Processing.** Idempotency keys
  (`internal/api/middleware/idempotency.go` + migration
  `0004_idempotency_fingerprint`); proration dedup (migration
  `0005_proration_dedup`); subscription "active uniqueness"
  invariant (migration `0013_subscriptions_active_unique`); inbound
  webhook event replay protection (migration
  `0058_webhook_event_replay`).
- **PI1.3 — Outputs.** Outbound webhooks signed with HMAC-SHA256;
  customer-portal links stable across rotations via
  `vlx_pinv_<32-hex>` public tokens.
- **PI1.4 — Authorised processing.** Permission-gated routes (CC6.1
  above). Test-mode and live-mode are isolated at the database
  layer; an export of test-mode data cannot accidentally be sent to
  a live-mode tenant.
- **PI1.5 — Stored authorised inputs.** Audit log + invoice
  finalisation events (`finalize`, `void`, `refund`) provide the
  end-to-end paper trail that an auditor walks: API request → audit
  row → invoice ledger → Stripe PaymentIntent.

### Privacy (P series)

> Personal information is collected, used, retained, disclosed, and
> disposed in conformity with the entity's commitments and system
> requirements.

The Privacy category is in scope for any tenant whose contracts
require it (GDPR, CCPA, HIPAA flow-through). Velox's privacy posture:

- **Notice.** Tenant-owned (their privacy policy).
- **Choice and consent.** Tenant-owned.
- **Collection.** Velox collects only what the tenant's API calls
  carry. The schema documents the columns; the encryption tables in
  `encryption-at-rest.md` document the protection.
- **Use, retention, and disposal.** Audit log retention by regime in
  `audit-log-retention.md`; GDPR right-to-erasure at
  `internal/customer/gdpr.go`.
- **Access.** GDPR right-of-access export shipped end-to-end
  (`internal/customer/gdpr.go::ExportCustomerData`).
- **Disclosure to third parties.** Stripe is the only sub-processor
  (PaymentIntent). The tenant lists Stripe in their DPA's
  sub-processor schedule.
- **Security for privacy.** Encryption at rest + audit log per
  CC6 above.
- **Quality.** Customer can update their own profile via the
  customer portal; the tenant can update via the API.
- **Monitoring and enforcement.** Audit log surfaces every
  privacy-relevant action (`gdpr.delete`, `gdpr.export`). The
  operator wires the alerting.

---

## Top gaps to close before a Type 1

The honest list, ranked by audit-impact:

1. **Key rotation tooling for `VELOX_ENCRYPTION_KEY` /
   `VELOX_EMAIL_BIDX_KEY`.** Single largest CC6.7 gap. Documented
   honestly in `encryption-at-rest.md` but not implemented. The
   proper fix is envelope encryption (DEK/KEK split). Tracked.
2. ~~**`SECURITY.md` + responsible-disclosure email.**~~ **Closed**
   in the SOC 2 cheap-gap closeout — `SECURITY.md` at repo root with
   `security@velox.dev`, 30-day patch SLA for high/critical, explicit
   in/out-of-scope, safe-harbor clause.
3. **MFA on dashboard login.** Tracked under the WorkOS / Clerk
   integration. CC6.1 gap; auditor flag for any tenant whose
   commitments require MFA.
4. ~~**`govulncheck` made blocking in CI.**~~ **Closed** — the
   `continue-on-error: true` qualifier was removed; `govulncheck` is
   now a hard CI gate. CI uses Go 1.25.9 (latest patch) and reports
   no vulnerabilities at time of merge.
5. **SAST in CI** (Semgrep / CodeQL). CC6.8 gap. Adds dependency-
   scanning's static-analysis sibling.
6. ~~**`CODE_OF_CONDUCT.md`**~~ **Closed** — references Contributor
   Covenant 2.1 verbatim with `conduct@velox.dev` and a separate
   `conduct-escalation@velox.dev` for concerns about the maintainer.
7. ~~**`CODEOWNERS`**~~ **Closed** — added at repo root with the
   maintainer as default owner; comments outline per-domain
   ownership splits as contributors join.
8. **Status page** for external availability comms (CC2.3 gap).
   Recommend Statuspage.io or atlassian's open-source equivalent
   wired to the same metrics that drive PagerDuty.
9. **Image signing** (cosign / Sigstore) on the GHCR push. CC6.8
   gap; small CI addition.
10. **Threat model document** (STRIDE or LINDDUN). CC3.2 gap;
    auditor will ask.
11. **Customer-notification template** for confirmed breaches in
    runbook.md. CC7.4 polish.
12. **Quarterly tabletop exercise schedule.** CC7.4 procedural.
13. **Vendor risk-review log.** CC9.2 procedural; tenant-side.
14. **Annual penetration test** (a Velox-as-a-company activity, not
    open-source-project).
15. **Dormant-key sweeper.** CC6.3 quality-of-life.
16. **Anomaly detection on audit log volumes.** CC3.3.
17. **Fine-grained API key scopes** (e.g. "charge but not refund").
    CC6.1 polish.

Items 1-9 are the priority list before pursuing a Type 1 walkthrough.
Items 10-17 are tracked but not blocking.

---

## Evidence index

A flat list of evidence pointers an auditor will request, with the
file path or runbook section that produces each.

| Evidence | Where | Notes |
|---|---|---|
| Audit log row sample | `audit_log` table; query recipes in [`audit-log-retention.md`](../ops/audit-log-retention.md#querying-the-audit-log) | Filter by review period, sample 25-50 rows. |
| Audit log immutability proof | `internal/platform/migrate/sql/0011_audit_append_only.up.sql` | The `audit_log_immutable_trg` trigger definition. |
| RLS policies | `internal/platform/migrate/sql/0001_schema.up.sql` (search `FORCE ROW LEVEL SECURITY`) | 66 tables enforced. |
| RLS test coverage | `internal/platform/postgres/rls_isolation_test.go` | Cross-tenant read/write attempts; both blocked. |
| API key hashing | `internal/auth/service.go::CreateKey` | SHA-256 with 16-byte per-key salt. |
| Password hashing | `internal/user/password.go` | Argon2id, OWASP 2024 parameters. |
| Encryption-at-rest verification | [`encryption-at-rest.md` → Verification recipes](../ops/encryption-at-rest.md#verification-recipes) | Run quarterly, attach output. |
| Storage-layer encryption | AWS / GCP / LUKS — see [`encryption-at-rest.md` § 7](../ops/encryption-at-rest.md#7-postgres-level-disk-encryption-storage-layer) | Operator-side attestation. |
| Backup + restore drill | [`docs/ops/backup-recovery.md`](../ops/backup-recovery.md) | RPO=5min, RTO=1h. Quarterly drill log. |
| CI configuration | `.github/workflows/ci.yml` | Branch-protection screenshot from GitHub. |
| Migration runner | `internal/platform/migrate/migrate.go` | Roundtrip test in `roundtrip_test.go`. |
| Migration safety findings | [`docs/migration-safety-findings.md`](../migration-safety-findings.md) | Lock-acquisition measurements + mitigations. |
| Architectural decisions | `docs/adr/001` through `006` | Reasoning for each major choice. |
| Runbook & alerts | [`docs/ops/runbook.md`](../ops/runbook.md) | Severity definitions, alert catalog, playbooks. |
| SLO definitions | [`docs/ops/sla-slo.md`](../ops/sla-slo.md) | Targets the alerts measure against. |
| Incident response | [`runbook.md` → Incident playbooks](../ops/runbook.md#incident-playbooks) | Tested via tabletop or real incident. |
| Post-mortem template | [`runbook.md` → Post-mortem template](../ops/runbook.md#post-mortem-template) | Saved as `docs/postmortems/YYYY-MM-DD-slug.md`. |
| Public changelog | `web-v2/src/pages/Changelog.tsx` + `CHANGELOG.md` | External communications evidence. |
| GDPR export | `internal/customer/gdpr.go::ExportCustomerData` | Article 15/20 right-of-access flow. |
| GDPR deletion | `internal/customer/gdpr.go` (audit row `gdpr.delete`) | Article 17 right-to-erasure flow. |
| Vendor list | This doc, CC9.2 above | Plus each vendor's SOC 2 report. |
| Configuration validation | `internal/config/config.go::validateFatal` | Refuses to start with unsafe combinations. |
| Webhook signature verification | `internal/webhook/service.go:444` | HMAC-SHA256, constant-time compare. |
| Permission policy | `internal/auth/permission.go:72-127` | Per-key-type permission map. |
| Rate limiter | `internal/api/middleware/ratelimit.go` | Redis-backed GCRA, fail-closed-capable. |
| Security headers | `internal/api/middleware/security.go` | HSTS, CSP-equivalent set. |

---

## Related docs

- [`audit-log-retention.md`](../ops/audit-log-retention.md) — the
  evidence-retention companion. CC4.1 / CC7.2 / CC7.5 lean on this
  heavily.
- [`encryption-at-rest.md`](../ops/encryption-at-rest.md) — the
  data-protection companion. CC6.7 leans on this almost entirely;
  the "Compliance mapping" subsection there is the predecessor of
  this whole doc.
- [`secrets-management.md`](../ops/secrets-management.md) — the env
  var delivery story. CC6.1 / CC6.7 cite this for the operator's
  secrets-store choices.
- [`runbook.md`](../ops/runbook.md) — operational runbook. CC4 / CC7
  cite this throughout.
- [`backup-recovery.md`](../ops/backup-recovery.md) — backup and
  recovery. CC7.5 / A1.2 cite this.
- [`sla-slo.md`](../ops/sla-slo.md) — SLO definitions that the alert
  catalog measures against.
- [`api-key-rotation.md`](../ops/api-key-rotation.md) — paired with
  CC6.3 (access removal) and CC6.7 (key lifecycle).
- [`docs/adr/`](../adr/) — architectural decision record. CC3.4 /
  CC8.1 cite the ADRs as the change-rationale evidence.
- `docs/compliance/gdpr-data-export.md` — Week 10 doc landing
  alongside this one. CC6.7 / Privacy category lean on the export
  flow described there.
