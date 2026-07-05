# Agent Prompting Standards

Reusable prompt fragments and rules for every multi-agent workflow this repo
runs — adversarial design panels, audit finders, verifiers, migration sweeps.
Distilled from Anthropic's Fable 5 and Opus 4.8 prompting guides (2026) plus what
our own panels proved works. (The orchestrator is Opus 4.8; subagents run Fable —
but these fragments are model-general.) Copy the fragments; don't re-derive them
per workflow.

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

**Coverage, not self-filtering, at the FIND stage.** Grounding (above) kills
false *positives*; this kills false *negatives*. A careful model honors "only
report high-severity / be conservative / don't nitpick" faithfully — it
investigates just as deeply, then drops findings below the stated bar, so recall
falls (Opus 4.8 guide). So a finder's job is coverage; a *later* stage filters.
Append:

> Report EVERY issue you find, including low-confidence and low-severity ones.
> Do not filter for importance or confidence here — tag each finding with a
> confidence and a severity, and a separate verify/rank stage will filter.
> Coverage is the goal: better to surface a finding that gets filtered out than
> to silently drop a real bug.

Our panels already have the downstream stage (adversarial-verify + dedup), so
this just moves the filtering there. If a single pass MUST self-filter, set a
CONCRETE bar ("anything that could cause incorrect behavior, a test failure, or a
misleading result; omit only pure style/naming"), never a qualitative "important."

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

If a stage reasons shallowly on a hard task, **raise its effort** rather than
adding "think carefully" band-aids to the prompt — effort is the lever, not a
prompt patch (Opus 4.8 guide).

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
- **Negative-instruction lists.** To shape output (concision, format, tone),
  SHOW one positive example of the desired style; positive examples beat "don't
  do X" lists (both guides).
- **Assuming an instruction generalizes.** Models are literal — they won't
  silently apply a rule from one item to the rest, nor infer a step you didn't
  ask for. State the scope ("every section, not just the first") and enumerate
  the set. The upside of the literalism is precision; pay for it with explicit
  scope.
