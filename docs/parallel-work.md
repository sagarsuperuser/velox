# Parallel Work Setup

> How two Claude Code sessions execute the 90-day plan in parallel without stepping on each other.

## Why

The 90-day plan has parallelizable workstreams. Backend Go work and frontend/content/outreach work touch different files and rarely conflict. Two sessions running in parallel against the plan ~2× velocity if coordinated well. Without coordination, you get merge conflicts and divergent assumptions.

## Two-track split

### Track A — Backend / Engine
- **Owns:** `internal/`, `cmd/`, `internal/platform/migrate/sql/` (migrations), Go test files, `docs/design-*.md`, `docs/adr/*`
- **Branches:** `feat/backend-*`
- **Week 1:** multi-dim meters technical design (`docs/design-multi-dim-meters.md`)
- **Week 2:** multi-dim meters implementation (schema migration, store, service, handler, tests, benchmark)
- **Week 3:** pricing recipes API + recipe registry
- **Week 5–6:** `create_preview` engine, billing thresholds, billing alerts (server-side)
- **Week 9–11:** self-host Helm chart, migration-from-Stripe importer

### Track B — Frontend / Content / Outreach
- **Owns:** `web-v2/`, `README.md`, `docs/blog/*`, `docs/marketing/*`, outreach docs
- **Branches:** `feat/frontend-*`, `docs/content-*`
- **Week 1:** README rewrite, AI-billing blog post, outreach list (50 candidates)
- **Week 2–3:** recipe-picker UI (against Track A's design doc), quickstart wizard scaffold
- **Week 4:** wizard polish, demo recording
- **Week 5:** cost dashboard React component (against Track A's `create_preview` API contract)
- **Week 6:** live event stream UI, plan-migration preview UI
- **Week 7:** bulk operations UI, one-off invoice composer

## File-ownership boundaries

| Path / file | Track A | Track B |
|---|---|---|
| `internal/**/*.go` | ✅ | ❌ |
| `cmd/**/*.go` | ✅ | ❌ |
| `internal/platform/migrate/sql/*.sql` | ✅ | ❌ |
| `docs/adr/*.md`, `docs/design-*.md` | ✅ | ❌ |
| `web-v2/src/**` | ❌ | ✅ |
| `web-v2/package.json`, lockfiles | ❌ | ✅ |
| `README.md` | shared | shared |
| `CHANGELOG.md`, `web-v2/src/pages/Changelog.tsx` | append-only | append-only |
| `docs/positioning.md`, `docs/90-day-plan.md` | append-only | append-only |
| `docs/blog/*`, `docs/marketing/*` | ❌ | ✅ |
| `docs/parallel-handoff.md` | append-only | append-only |
| `docs/parallel-work.md` (this file) | shared | shared |

**Append-only rule** for shared docs: each track adds its own entries; never edits the other's entries within the same week. End-of-week rebase resolves any drift.

## Coordination protocols

### 1. Design-first / RFC pattern

For any backend work whose API the frontend consumes, **Track A writes a design doc in `docs/design-*.md` BEFORE implementing.** The frontend treats the design doc as the API contract — can stub/mock/scaffold against it while Track A implements.

Track A maintains the design doc as the source of truth. If implementation diverges from the doc, **the doc updates first** in the same commit.

This is the standard RFC pattern (Rust, Go, Python, Tailscale, Stripe internal). It's how senior engineering teams ship cross-cutting features without thrash.

### 2. Daily handoff log

`docs/parallel-handoff.md` is a per-day append-only log. Every session ends its turn by appending an entry. The next session reads recent entries before starting work.

Format:

```
## 2026-04-25 (Sat)

### Track A
- Shipped: docs/design-multi-dim-meters.md (RFC v1, status: draft)
- API decisions firmed up: dimensions are free-form JSONB, max 16 keys/event
- Blocking Track B on: nothing
- Track B can start: building recipe-picker UI against design doc

### Track B
- Shipped: README.md rewrite, outreach list (15 of 50), blog post outline
- Blocking Track A on: nothing
```

### 3. Migration numbering rule

**Only Track A creates migrations.** Per memory rule (`feedback_migration_numbering.md`): pick the next number from `origin/main` at PR-open time, not from the local branch. If both tracks need a schema change, Track B files an issue or design-doc proposal; Track A picks it up.

### 4. Git worktrees for parallel sessions

Each session runs in its own git worktree so file changes don't collide:

```bash
# From repo root:
git worktree add ../velox-track-a feat/backend-week1
git worktree add ../velox-track-b feat/frontend-week1

# Each Claude Code session opens its own worktree as cwd.
```

End of day: each track rebases on `main`, pushes, opens a PR. Once green, merge to `main`. Worktrees stay; branches roll forward.

### 5. Conflict resolution

If a merge conflict surfaces:

- **File-ownership winner takes priority** — Track A wins on `internal/`, Track B wins on `web-v2/src/`.
- **Shared docs** — take both edits; if structurally divergent, pause and ask the human.
- **Neither owns the file** (rare) — pause and ask the human.

Never resolve a conflict by silently picking one side without checking ownership. That's how subtle bugs slip in.

### 6. CHANGELOG + Changelog.tsx discipline

Per memory (`feedback_changelog_discipline.md`), every user-visible ship updates BOTH:
- `CHANGELOG.md` (Keep a Changelog format)
- `web-v2/src/pages/Changelog.tsx` (Linear-style curated rollups)

Track A owns CHANGELOG.md entries for backend changes; Track B owns Changelog.tsx entries (since it touches `web-v2/`). For features that span both tracks, the track that ships the user-visible surface owns the Changelog.tsx update.

## Kickoff prompt for Track B (paste into the second Claude Code session)

```
I'm continuing parallel work on the Velox 90-day plan from a separate session.
This is Track B (frontend / content / outreach).

Read these in order:
1. docs/positioning.md — strategic positioning (AI-native + self-host wedge)
2. docs/90-day-plan.md — week-by-week execution plan
3. docs/parallel-work.md — workstream split between Track A (backend, the
   other session) and Track B (this session)
4. docs/parallel-handoff.md — latest handoff log; read entries from the past
   2-3 days

Week 1 deliverables for Track B:
- Rewrite README.md to lead with the AI-native + self-host wedge (current
  README probably leads with generic billing-engine framing)
- Draft a long-form blog post: "Why Stripe Billing's Meter API doesn't fit
  AI workloads" — save as docs/blog/2026-04-stripe-meter-api-ai-workloads.md
- Build an outreach list of 50 candidate design partners (AI inference,
  vector DB, dev infra) as docs/marketing/outreach-list.md — name, company,
  URL, role, why-they-fit

Conventions:
- I do NOT touch internal/, internal/platform/migrate/sql/, or cmd/ — that's
  Track A's lane
- I append my work to docs/parallel-handoff.md at end of every turn so
  Track A knows what shipped and what's blocked
- Schema/migration changes go through Track A (file an issue or design doc
  proposal if I need one for a UI feature)
- Industry-grade: design before code (RFC-first), no half-finished
  implementations, follow existing repo conventions
- Read the user's auto-memory in ~/.claude/.../memory/ — it has the user's
  preferences and decisions; respect them

Start by:
1. Reading the four docs above
2. Appending a "Track B kickoff" entry to docs/parallel-handoff.md
3. Beginning the README rewrite

Then work through the Week 1 deliverables. Commit each piece individually
(matching the repo's tight commit style — see `git log --oneline -10`).
```

## When NOT to parallelize

Don't parallelize when:
- The next deliverable is a single tightly-coupled feature (e.g. one bug fix touching backend + frontend together) — single session is faster than the coordination overhead
- One track is in a deep design phase and the other has no usable contract yet — let the design land first
- The user wants to closely review every step of one track — splitting attention degrades both

Default: **one session unless there's an obvious split.** Parallelize when the next ~3–5 deliverables genuinely fall into two non-overlapping buckets.
