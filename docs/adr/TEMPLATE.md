# ADR-NNN: Short imperative title

**Date:** YYYY-MM-DD
**Status:** Proposed | Accepted | Superseded by ADR-XXX | Deprecated

<!--
Status field uses one of:
- "Proposed" — under discussion, not yet committed to.
- "Accepted" — decision in effect.
- "Accepted (amended YYYY-MM-DD — short reason)" — when the ADR has
  an inline amendment section added later.
- "Superseded by ADR-XXX (one-line reason)" — when a later ADR
  replaces this decision. Keep the file; the history is the point.
- "Deprecated" — no longer in effect but not replaced by a new ADR.

Date is when the ADR was first written. Amendment dates go in the
status line + inline section headers ("## Amendment YYYY-MM-DD").
-->

## Context

What changed in the world, the codebase, or the operator experience
that triggered this decision? Quote concrete signals — a bug report,
a verified cross-platform pattern, a customer ask. Don't invent
abstract justifications.

If the decision is anchored on industry shape ("Stripe parity", "best
practice"), this section MUST quote verified source lines from at
least 2-4 reference platforms. Per
`feedback_verify_stripe_parity_claims`: single-platform pseudo-research
isn't research. Use WebSearch / WebFetch to verify before writing.

## Decision

The decision itself — one or two paragraphs. State what's done, not
why (that's the next section). Be precise: name the affected
interfaces, tables, files, migrations.

## Why this design

Why the chosen approach over alternatives. Tie back to memories
(`feedback_stripe_parity_framing`, `feedback_longterm_fixes`, etc.)
when relevant — the memory is the durable principle; the ADR is the
specific application of it.

## Alternatives considered

For each alternative seriously discussed:
- **A. <Name>** — one-paragraph description, then rejection reason.

Discard fake alternatives ("do nothing", "rewrite everything"). The
list should show that the chosen design was non-obvious.

## Consequences

### Positive
- What gets better, named concretely.

### Risks / open items
- What gets harder, what we're trading off, what's deferred and why.
- Schema migration risks (data loss, downtime).
- Operator-UX surprises (e.g. semantic changes to existing fields).
- Followup work that's NOT in this ADR's scope.

## References

- Related ADRs (cite by number)
- Migration numbers (`migration 00XX`)
- Memory pointers (`feedback_X`, `project_Y`)
- External docs / source lines (markdown links, NOT bare URLs)
