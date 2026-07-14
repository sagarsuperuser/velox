package arch

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// WHY THESE GATES EXIST
//
// The audit redesign (ADR-089/090) was audited for completeness THREE times. Each
// round found ~30 places where a comment, an ADR paragraph, a registry note or a
// runbook box claimed something the code does not do — and each round's FIXES
// left siblings behind, or introduced fresh lies of their own. The third round
// caught a comment asserting a route is `explicit` when the registry says
// `exempt`, in a commit whose entire purpose was to remove false claims.
//
// The lesson is not "try harder". It is:
//
//	PROSE THAT MAKES A PRECISE, CHECKABLE CLAIM AND IS NEVER CHECKED WILL DRIFT.
//	Either mechanize the check, or do not make the claim.
//
// These are the mechanized checks for the two classes that drifted in every
// single round. They are deliberately cheap and deliberately literal.

// sourceFiles walks internal/ and hands back every non-test .go file.
func sourceFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := internalDir(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(filepath.Dir(root), path)
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("walked 0 source files — the gate is broken, not the code")
	}
	return out
}

// markSkipCallers is the CANONICAL list of paths that declare "this 2xx mutated
// nothing" (audit.MarkSkip). It is duplicated in prose in two places that keep
// going stale — internal/audit/context.go's "Live callers" list and ADR-090 §4 —
// so it is pinned HERE, against the code, and those two now point at this test
// rather than trying to enumerate from memory.
//
// Adding a MarkSkip call is a real decision: it tells the coverage detector that a
// successful mutating request legitimately wrote no audit row. If you add one,
// add it here and say why. If this list is longer than you expected, that is the
// point — a silent MarkSkip is a route quietly opting out of audit coverage.
var markSkipCallers = map[string]string{
	"internal/billing/create_preview_handler.go": "invoice create_preview — read-only dry run",
	"internal/recipe/handler.go":                 "recipe preview — read-only render",
	"internal/recipe/service.go":                 "recipe re-apply that installs nothing",
	"internal/tenant/settings.go":                "settings save that changed no field",
	"internal/usage/handler.go":                  "duplicate usage ingest — idempotency key already exists, no event row written",
	"internal/user/handler.go":                   "stale-cookie logout; unknown-email password reset; THROTTLED password reset (all fixed-200 enumeration defences)",
	"internal/api/adapters.go":                   "hosted-invoice payment-session reuse — a second Pay click returns the same Stripe session, no new claim row",
	"internal/api/middleware/idempotency.go":     "idempotency replay — the cached response of a request whose original DID emit",
	"internal/creditnote/handler.go":             "credit-note issue that defers",
}

// TestMarkSkipCallers_MatchTheCode is the gate on the class that drifted in all
// three audit rounds: a hand-maintained list of "who calls MarkSkip" that nobody
// re-derives when a caller is added.
func TestMarkSkipCallers_MatchTheCode(t *testing.T) {
	call := regexp.MustCompile(`(?:audit\.)?MarkSkip\(`)

	found := map[string]int{}
	for path, src := range sourceFiles(t) {
		// The definition itself, and the package's own doc, are not callers.
		if path == "internal/audit/context.go" {
			continue
		}
		if n := len(call.FindAllString(src, -1)); n > 0 {
			found[path] = n
		}
	}

	var undeclared, stale []string
	for path := range found {
		if _, ok := markSkipCallers[path]; !ok {
			undeclared = append(undeclared, path)
		}
	}
	for path := range markSkipCallers {
		if found[path] == 0 {
			stale = append(stale, path)
		}
	}
	sort.Strings(undeclared)
	sort.Strings(stale)

	if len(undeclared) > 0 {
		t.Errorf(`%d file(s) call audit.MarkSkip but are NOT declared in markSkipCallers:

  %s

MarkSkip tells the coverage detector that a successful MUTATING request legitimately
wrote no audit row. That is a route opting out of audit coverage, and it must be a
decision someone made on purpose — not a line that appeared. Add the file to
markSkipCallers (in this test) with a one-line reason.`,
			len(undeclared), strings.Join(undeclared, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf(`%d entr(y/ies) in markSkipCallers call MarkSkip nowhere:

  %s

The caller was removed but the list was not. A list that names paths which no longer
exist is exactly the kind of confident-and-wrong documentation this gate exists to
prevent.`, len(stale), strings.Join(stale, "\n  "))
	}
}

// deletedThings are things this arc DELETED, paired with the phrasings that assert
// they still exist. Every audit round found comments still describing them in the
// present tense — the catch-all middleware was found in round 1, round 2 AND round
// 3, in five different files, because each pass fixed only the instances it was
// handed.
//
// The check is deliberately literal: it looks for a present-tense assertion, not a
// mere mention. Saying "the catch-all was deleted" or "this used to opt out of the
// catch-all" is correct and stays legal — describing history is how you stop the
// next reader re-deriving a wrong conclusion.
var forbiddenPresentTense = []struct {
	pattern *regexp.Regexp
	why     string
}{
	{
		regexp.MustCompile(`(?i)(the|a) (audit )?middleware'?s? catch-all (still|records|writes|would|so it|alone)`),
		"the catch-all audit middleware was DELETED (ADR-090, #471). It does not record, write, or fall back to anything. If you are describing history, say so explicitly (\"used to\", \"was deleted\").",
	},
	{
		regexp.MustCompile(`which the next PR deletes`),
		"the catch-all was deleted in #471; there is no 'next PR'. A comment that describes a completed deletion as pending tells the reader the opposite of the truth.",
	},
	{
		regexp.MustCompile(`(?i)(Logger\.Log and the AuditLog middleware|both audit write paths \(Logger\.Log and)`),
		"the AuditLog middleware was DELETED (ADR-090, #471). There is exactly ONE post-hoc write path (Logger.Log) plus the in-tx LogInTx.",
	},
	{
		regexp.MustCompile(`order=sim_effective_at|order by simulated time`),
		"there is deliberately NO 'order by simulated time' control (ADR-090 §5): within one clock it is the same order as created_at, and across clocks it interleaves unrelated simulations. It was removed in #474 along with its cursor axis.",
	},
}

// negated marks a sentence that DENIES the deleted thing exists ("there is
// deliberately NO order by simulated time", "the catch-all was deleted"). Those
// are the sentences we WANT — describing history, or the absence of a mechanism,
// is how you stop the next reader re-deriving a wrong conclusion. A gate that
// fires on the correct sentence is a gate somebody deletes, so it must not.
var negated = regexp.MustCompile(`(?i)\b(no|not|never|deleted|removed|gone|used to|no longer|does not|cannot|was the|deliberately|retired|superseded|instead of|rather than)\b`)

// TestNoPresentTenseReferencesToDeletedThings gates the other class that survived
// every round: a comment that still describes machinery the arc removed. An
// out-of-date comment about a DELETED mechanism is worse than no comment — it
// sends the next reader looking for a safety net that is not there.
//
// Matching is per-LINE and negation-aware: the forbidden phrasing only fails when
// the line asserts the thing is live.
func TestNoPresentTenseReferencesToDeletedThings(t *testing.T) {
	for path, src := range sourceFiles(t) {
		// The gate's own table names the phrases it forbids.
		if strings.HasSuffix(path, "audit_prose_gates_test.go") {
			continue
		}
		for i, line := range strings.Split(src, "\n") {
			for _, f := range forbiddenPresentTense {
				m := f.pattern.FindString(line)
				if m == "" || negated.MatchString(line) {
					continue
				}
				t.Errorf("%s:%d: %q\n\n  %s\n", path, i+1, m, f.why)
			}
		}
	}
}

// TestAuditProseGates_AreThemselvesWired is the belt on the gates: a regex that
// matches nothing (because someone renamed the thing it looked for) is a gate that
// has silently stopped gating — the exact failure mode of the audit coverage this
// whole arc was about.
func TestAuditProseGates_AreThemselvesWired(t *testing.T) {
	files := sourceFiles(t)
	call := regexp.MustCompile(`(?:audit\.)?MarkSkip\(`)
	total := 0
	for path, src := range files {
		if path == "internal/audit/context.go" {
			continue
		}
		total += len(call.FindAllString(src, -1))
	}
	if total == 0 {
		t.Fatal("found ZERO MarkSkip calls — either they are all gone (delete this gate) or the pattern stopped matching (fix it). A gate that matches nothing is not a gate.")
	}
	if len(markSkipCallers) == 0 {
		t.Fatal("markSkipCallers is empty")
	}
	fmt.Printf("audit prose gates: %d MarkSkip call sites across %d declared files\n", total, len(markSkipCallers))
}
