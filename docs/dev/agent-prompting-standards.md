# Agent Prompting Standards (Claude Fable 5)

Reusable prompt fragments and rules for every multi-agent workflow this repo
runs — adversarial design panels, audit finders, verifiers, migration sweeps.
Distilled from Anthropic's Fable 5 prompting guide (2026) plus what our own
panels proved works. Copy the fragments; don't re-derive them per workflow.

Scope: this is a **tooling** discipline. Money-path methodology stays in
[money-path-robustness-playbook.md](money-path-robustness-playbook.md); this
doc is only about how we *prompt the agents* that execute that methodology.

## 1. Ground every claim (finders, auditors, status reporters)

Append to every finder/audit agent prompt:

> Before reporting, audit each claim against a tool result from this session
> (a file you read, a command you ran). Only report findings you can point to
> evidence for — cite `file:line`. If something is plausible but unverified,
> label it PLAUSIBLE explicitly. Never report a finding you inferred but did
> not check.

Why: Anthropic measured this line nearly eliminating fabricated reports.
For us it cuts false findings *before* the adversarial-verify stage instead
of paying 3 verifier votes to kill them there.

## 2. Give the reason with the request

Every agent prompt opens with intent, not just task:

> You are one lens of an adversarial design panel for Velox (an open-source
> usage-based billing engine). The design under attack fixes [the bug and its
> money consequence]. Your verdict gates whether we build it.

Why: Fable 5 connects the task to relevant context instead of inferring
intent. Our panels already do this — every panel that materially amended a
design (P1b denominator, P2b claim protocol) carried the full why. This makes
it a standard, not a habit.

## 3. Evidence, never reasoning transcripts

Ask agents for **evidence and justification of claims** (`detail`, `why`,
`failure_scenario` fields). Never write "show your thinking", "explain your
reasoning step by step", or "include your chain of thought" in a prompt or
schema — Fable 5 runs a `reasoning_extraction` refusal classifier and such
phrasing can trigger fallbacks mid-workflow.

## 4. Route effort per stage

Workflow `agent()` calls accept `effort`. Default (inherited xhigh under
ultracode) is right for panels, verifiers, and money-path finders — and
wasteful for mechanical stages. Route:

| Stage | Effort |
|---|---|
| Adversarial panel lenses, verify votes, money finders | inherit (xhigh) |
| Broad code/document sweeps, catalog checks | `medium` |
| Formatting, list-building, mechanical transforms | `low` |

## 5. Mid-build spec-conformance verifier (optional gate, L-sized builds)

Panels verify the design *before* code; mutation tests verify behavior
*after*. For builds spanning 4+ commits, add one fresh-context verifier
between implementation and the test matrix:

> Read [the amended design/spec section] and the diff of [branch] against
> [base]. For each numbered protocol item, state whether the implementation
> conforms, citing file:line — or name the divergence. Do not review style;
> review spec conformance only.

Why: fresh-context verification outperforms self-critique (Anthropic), and
spec drift across a long build is exactly the class the P1b panel caught at
design time — this catches it again at build time, cheaply.

## 6. Anti-patterns

- **Over-prescription.** Fable 5 follows brief instructions well; long
  enumerated behavior lists degrade output. One sentence of intent beats ten
  bullet rules. When editing this doc or the playbook, prefer deleting an
  obsolete rule over adding a clarifying one.
- **Blocking on subagents.** Prefer async dispatch (background workflows,
  notification on completion) over waiting inline.
- **Echoing context budgets.** Never tell an agent how many tokens it has
  left; it induces premature wrap-up.
