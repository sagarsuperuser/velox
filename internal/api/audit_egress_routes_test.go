package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// walkEgressRoutes enumerates the live GET routes under /v1/exports — the bulk
// data-egress surface. Same mechanism as the mutating walk (chi.Walk over the
// real router), narrowed to the one subtree the egress registry claims.
func walkEgressRoutes(t *testing.T) map[routeKey]bool {
	t.Helper()

	srv := newTestServer()
	found := make(map[routeKey]bool)
	err := chi.Walk(srv.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != http.MethodGet {
			return nil
		}
		pattern := canonicalRoute(route)
		if !strings.HasPrefix(pattern, auditEgressPrefix+"/") {
			return nil
		}
		found[routeKey{Method: method, Pattern: pattern}] = true
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk over the live router: %v", err)
	}
	if len(found) == 0 {
		t.Fatalf("walked 0 export routes under %s — the walk is broken, not the registry", auditEgressPrefix)
	}
	return found
}

// TestAuditEgressRegistry_MatchesLiveExportRoutes is the drift gate for read
// egress. Two-way diff, same contract as the mutating registry: a new export
// route that ships without an audit emission fails CI, and an entry for a route
// that no longer exists fails CI.
//
// Bulk export is the one READ that must leave a trail — it is the act of copying
// the evidence. Do not "fix" a failure here by deleting the entry.
func TestAuditEgressRegistry_MatchesLiveExportRoutes(t *testing.T) {
	walked := walkEgressRoutes(t)

	var undeclared []string
	for k := range walked {
		if _, ok := auditEgressRegistry[k]; !ok {
			undeclared = append(undeclared, fmt.Sprintf("%s %s", k.Method, k.Pattern))
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		t.Errorf(`%d export route(s) are NOT declared in the read-egress registry:

%s

You added a route that hands an operator bulk tenant data. It must emit an
action=export audit row BEFORE it streams a byte, and fail closed if that row
cannot be written (exportsHandler.auditExport) — then declare it in
internal/api/audit_routes.go (auditEgressRegistry).

A row written at stream COMPLETION is not a substitute: killing the connection
mid-stream defeats it, and pages of customer PII leave with nothing recorded.`,
			len(undeclared), "  "+strings.Join(undeclared, "\n  "))
	}

	var stale []string
	for k := range auditEgressRegistry {
		if !walked[k] {
			stale = append(stale, fmt.Sprintf("%s %s", k.Method, k.Pattern))
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf(`%d read-egress registry entr(y/ies) match NO live route:

%s

The route was renamed or removed but its registry entry was not. Delete or
update the entry — a stale entry claims audit coverage for a surface that does
not exist.`, len(stale), "  "+strings.Join(stale, "\n  "))
	}
}

// TestAuditEgressRegistry_EntriesAreWellFormed pins the shape of the egress
// table. Every export is EXPLICIT: there is no honest "exempt" reason for handing
// out a whole table with no record — if a future route needs one, that is a
// deliberate widening and it argues for itself in review, not here.
func TestAuditEgressRegistry_EntriesAreWellFormed(t *testing.T) {
	for k, d := range auditEgressRegistry {
		where := k.Method + " " + k.Pattern

		if k.Method != http.MethodGet {
			t.Errorf("%s: the egress registry declares bulk READ routes; mutating routes belong in auditRouteRegistry", where)
		}
		if got := canonicalRoute(k.Pattern); got != k.Pattern {
			t.Errorf("%s: key is not canonical (canonicalRoute gives %q)", where, got)
		}
		if !strings.HasPrefix(k.Pattern, auditEgressPrefix+"/") {
			t.Errorf("%s: outside the declared egress subtree %s — the arch test's walk would never see it, so the entry would be dead", where, auditEgressPrefix)
		}
		if d.Coverage != auditExplicit {
			t.Errorf("%s: bulk data egress must be explicit — an un-recorded full-table export is the exact gap ADR-090 §7 closes", where)
		}
		if strings.TrimSpace(d.Note) == "" {
			t.Errorf("%s: empty note — an explicit entry must name its emission site", where)
		}
	}
}
