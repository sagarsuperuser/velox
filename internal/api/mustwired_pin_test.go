package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestMustWired_CoversEveryAuditedComponent pins the audit.MustWired list in
// router.go against the SetAuditLogger wiring cluster: every component that
// gets an in-tx audit emitter via <name>.SetAuditLogger(auditLogger) must
// also appear in the audit.MustWired(...) call, so a future audited domain
// can't silently skip the boot-time nil check. Source-text based on purpose:
// the wiring lives in ONE composition-root file, and a text pin is the
// simplest gate that fails when a new SetAuditLogger line lands without the
// matching MustWired entry.
//
// Handler-level SetAuditLogger receivers (post-hoc Log writers, e.g.
// invoiceH) are exempt: MustWired covers services/adapters that emit IN-TX
// (ADR-090), where a nil emitter silently un-audits a domain. Handlers keep
// the *audit.Logger concrete type and fail loudly at compile time instead.
func TestMustWired_CoversEveryAuditedComponent(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	text := string(src)

	mw := regexp.MustCompile(`audit\.MustWired\(([^)]*)\)`).FindStringSubmatch(text)
	if mw == nil {
		t.Fatal("audit.MustWired call not found in router.go")
	}
	wired := map[string]bool{}
	for _, name := range strings.Split(mw[1], ",") {
		wired[strings.TrimSpace(name)] = true
	}

	// Service/adapter receivers of SetAuditLogger(auditLogger). Handler
	// receivers end in "H" by router convention and are exempt (see doc).
	for _, m := range regexp.MustCompile(`(\w+)\.SetAuditLogger\(auditLogger\)`).FindAllStringSubmatch(text, -1) {
		recv := m[1]
		if strings.HasSuffix(recv, "H") {
			continue
		}
		if !wired[recv] {
			t.Errorf("%s.SetAuditLogger is wired in router.go but %s is missing from audit.MustWired(...) — add it so a dropped wiring line fails at boot", recv, recv)
		}
	}
}
