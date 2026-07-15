package arch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The residual own-tx audit writers, and why each one is still allowed.
//
// ADR-090 made audit emission ride the BUSINESS transaction (`LogInTx`): the row
// and the change it describes commit together or not at all. Handlers are fully
// migrated. The engine is NOT — these call sites still use the own-tx `Log`, which
// writes the row in a SEPARATE transaction AFTER the business change has already
// committed. If that write fails, the mutation stands and its evidence does not.
// That is the `row_lost` outcome on velox_audit_write_errors_total, and nothing
// retries it.
//
// Every one of these discards the error (`_ =`), which is correct and is also the
// point: post-commit, there is nothing the caller can do. The only real fix is to
// stop writing post-commit, and that means threading the business tx down to the
// emission — a money-path change per domain, under the playbook gates, which is
// what ADR-090 says each domain does in its own PR.
//
// So this gate does the one thing that is both cheap and load-bearing today: it
// makes the residual set EXPLICIT and SHRINKING. Adding a new own-tx background
// writer fails CI. Migrating one to LogInTx fails CI until it is deleted from this
// map. The map is the migration backlog, and it can only go down.
//
// Keyed by action, counted — not by file:line, which rots on the first edit above
// the call, and not by file alone, which would let a new call hide inside a file
// that is already listed. (That exact miss shipped once in this arc's own MarkSkip
// gate, which was file-keyed and therefore blind to new calls in declared files.)
// MIGRATED (no longer here): "finalize". As of 2026-07-15 every engine finalize
// path emits its audit row IN the invoice-create transaction
// (CreateInvoiceWithLineItemsAudited on the own-tx paths; LogInTx on the
// coordinator tx for the cross-interval swap, the atomic cancel, and the threshold
// reset) — shared fate, ADR-090. It was the highest-value row and the first domain
// migrated off the post-commit Log; the six textual finalize sites (cycle close,
// subscription create, both cancel paths, both swap paths, threshold) are covered
// by the shared-fate + sim-axis-parity tests. This is the shrinking set doing its
// job: one entry removed, proven, gone.
var residualOwnTxAuditWriters = map[string]int{
	// Engine-driven subscription lifecycle. Cancel appears twice: end-of-period cancel
	// on the billing run, and the cancel-at-period-end sweep.
	"cancel":                              2,
	"update":                              1,
	"subscription.pending_change_applied": 1,

	// Usage-threshold scanning. Neither mutates money directly — they record that a
	// threshold was crossed or deliberately deferred — so these are the least urgent
	// to migrate, and the safest to leave behind if the others move first.
	"subscription.threshold_crossed":  1,
	"subscription.threshold_deferred": 1,
}

// Packages whose audit emission runs OUTSIDE a request, on the scheduler. These are
// the ones that must eventually ride their business tx.
var backgroundPkgs = []string{"../billing", "../dunning", "../webhook", "../stripe"}

var ownTxLogCall = regexp.MustCompile(`auditLogger\.Log\(\s*ctx\s*,[^,]+,\s*(?:"([^"]+)"|domain\.(\w+))`)

// TestBackgroundAuditWriters_AreAnExplicitShrinkingSet fails when a background
// package grows a NEW post-commit audit writer, or when a declared one is migrated
// but left in the list.
func TestBackgroundAuditWriters_AreAnExplicitShrinkingSet(t *testing.T) {
	found := map[string]int{}

	for _, pkg := range backgroundPkgs {
		entries, err := os.ReadDir(pkg)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", pkg, err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join(pkg, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			for _, m := range ownTxLogCall.FindAllStringSubmatch(stripLineComments(string(src)), -1) {
				action := m[1]
				if action == "" {
					// domain.AuditActionFinalize -> "finalize"
					action = strings.ToLower(strings.TrimPrefix(m[2], "AuditAction"))
				}
				found[action]++
			}
		}
	}

	for action, n := range found {
		want, declared := residualOwnTxAuditWriters[action]
		if !declared {
			t.Errorf(`NEW post-commit audit writer: %q (%d call(s)) in a background package.

ADR-090 requires background writers to emit on their BUSINESS transaction (LogInTx),
so the row and the mutation share fate. An own-tx Log() here writes the row AFTER the
change has committed — if it fails, the mutation stands with no evidence, and nothing
retries it.

Use LogInTx with the tx that performs the change. If that is genuinely impossible,
add %q to residualOwnTxAuditWriters with the reason, and understand that you are
signing up for a permanently lossy audit row.`, action, n, action)
			continue
		}
		if n != want {
			t.Errorf("own-tx audit writer %q: found %d call(s), declared %d.\n"+
				"If you ADDED one, see the message above. If you MIGRATED one to LogInTx, "+
				"lower the count (or delete the entry) — the residual set is the migration "+
				"backlog and is only allowed to shrink.", action, n, want)
		}
	}

	for action, want := range residualOwnTxAuditWriters {
		if _, ok := found[action]; !ok {
			t.Errorf("declared own-tx audit writer %q (%d) no longer exists.\n"+
				"If it was migrated to LogInTx: delete it from residualOwnTxAuditWriters — "+
				"leaving it makes the backlog overstate the remaining loss.", action, want)
		}
	}
}

// stripLineComments removes // comments so a call quoted in prose (this arc has a
// lot of prose) cannot be mistaken for a real one.
func stripLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
