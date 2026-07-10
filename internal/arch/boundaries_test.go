// Package arch holds architecture-invariant tests that keep CLAUDE.md's
// stated boundaries from silently eroding (a repeat of the doc-drift the
// 2026-07-07 doc-truth audit found: CLAUDE.md claimed "zero cross-domain
// imports" while several domains imported peers).
package arch

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// allowedCrossDomainImports pins every peer-domain import edge that
// exists in prod code today. The invariant it enforces is NOT "no
// cross-domain imports" (false — see below) but "no NEW cross-domain
// coupling sneaks in unreviewed." A new edge fails this test until an
// author adds it here, which forces a conscious decision.
//
// The edges fall into three sanctioned classes:
//   - cross-cutting infra any domain may use: auth (ctx/permissions),
//     audit (operator logging), session (dashboard auth).
//   - shared value types / DTOs / validation helpers: chiefly tax.*
//     (ReversalRequest, ValidateTaxID), subscription.ListFilter.
//   - the billing coordinator: billing orchestrates credit/payment/
//     subscription/tax via narrow consumer-defined interfaces (the one
//     package allowed to reach several peers by design).
//
// What is forbidden and must never appear here: a business domain
// importing another domain's concrete *Service or *Store. If a genuinely
// new value-type edge is needed, add it with a one-word note; if you're
// about to add a concrete-service edge, wire it through the composition
// root (internal/api) instead.
var allowedCrossDomainImports = map[string][]string{
	"analytics":      {"auth"},
	"audit":          {"auth"},
	"billing":        {"audit", "auth", "credit", "payment", "subscription", "tax"},
	"credit":         {"audit", "auth"},
	"creditnote":     {"audit", "auth", "tax"},
	"customer":       {"audit", "auth", "tax"},
	"dashmembers":    {"audit", "auth", "session"},
	"dunning":        {"auth"},
	"hostedinvoice":  {"invoice"},
	"invoice":        {"auth", "tax"},
	"payment":        {"auth", "tax", "tenantstripe"},
	"paymentmethods": {"auth", "payment"},
	"pricing":        {"auth"},
	"recipe":         {"audit", "auth"},
	"session":        {"auth"},
	"subscription":   {"audit", "auth", "tax"}, // tax = shared value types/classification (ADR-052 class; used for deferral-fact stamping on proration invoices)
	"tenant":         {"auth"},
	"tenantstripe":   {"auth"},
	"testclock":      {"auth"},
	"usage":          {"auth", "subscription"},
	"user":           {"audit", "auth", "session"},
	"webhook":        {"auth"},
}

// domainDirs are the packages under internal/ that are DOMAINS (own a
// slice of the business). Non-domain packages are excluded as importers:
// api is the composition root (wires everything by design); the rest are
// infra/shared kernels that any domain may depend on.
var nonDomainDirs = map[string]bool{
	"api": true, "arch": true, "config": true, "domain": true, "errs": true,
	"integrations": true, "pdffonts": true, "platform": true, "testutil": true,
	"version": true,
}

func internalDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	// .../internal/arch/boundaries_test.go -> .../internal
	return filepath.Dir(filepath.Dir(thisFile))
}

// domainImports returns the set of peer-DOMAIN packages imported by prod
// (non-test) Go files in internal/<dom>/.
func domainImports(t *testing.T, internal, dom string, isDomain func(string) bool) []string {
	t.Helper()
	dir := filepath.Join(internal, dom)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			const pfx = "github.com/sagarsuperuser/velox/internal/"
			if !strings.HasPrefix(path, pfx) {
				continue
			}
			pkg := strings.SplitN(strings.TrimPrefix(path, pfx), "/", 2)[0]
			if pkg != dom && isDomain(pkg) {
				seen[pkg] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func TestNoUnreviewedCrossDomainImports(t *testing.T) {
	internal := internalDir(t)
	entries, err := os.ReadDir(internal)
	if err != nil {
		t.Fatalf("read internal/: %v", err)
	}
	isDomain := func(pkg string) bool { return !nonDomainDirs[pkg] }

	// Collect the actual edge set and diff against the allowlist.
	for _, e := range entries {
		if !e.IsDir() || nonDomainDirs[e.Name()] {
			continue
		}
		dom := e.Name()
		allowed := map[string]bool{}
		for _, p := range allowedCrossDomainImports[dom] {
			allowed[p] = true
		}
		for _, peer := range domainImports(t, internal, dom, isDomain) {
			if !allowed[peer] {
				t.Errorf("NEW cross-domain import: internal/%s imports internal/%s, not in the allowlist.\n"+
					"  If this is an intentional value-type/coordinator import, add %q to allowedCrossDomainImports[%q] with a note.\n"+
					"  If it's a concrete *Service/*Store dependency, wire it through the composition root (internal/api) instead.",
					dom, peer, peer, dom)
			}
		}
	}
}

// TestAllowlistHasNoStaleEntries keeps the allowlist honest — an entry
// for an edge that no longer exists is stale documentation that should
// be removed (mirrors the rlsFenceAllowlist stale-entry check).
func TestAllowlistHasNoStaleEntries(t *testing.T) {
	internal := internalDir(t)
	isDomain := func(pkg string) bool { return !nonDomainDirs[pkg] }
	for dom, peers := range allowedCrossDomainImports {
		actual := map[string]bool{}
		for _, p := range domainImports(t, internal, dom, isDomain) {
			actual[p] = true
		}
		for _, p := range peers {
			if !actual[p] {
				t.Errorf("stale allowlist entry: allowedCrossDomainImports[%q] lists %q, but internal/%s no longer imports internal/%s — remove it.", dom, p, dom, p)
			}
		}
	}
}
