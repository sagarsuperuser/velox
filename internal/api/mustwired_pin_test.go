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
// THERE ARE NO EXEMPTIONS. There used to be 14 — every handler-level receiver —
// on the written grounds that "handlers keep the *audit.Logger concrete type and
// fail loudly at compile time instead." BOTH HALVES OF THAT WERE FALSE. MOST of
// those handlers take an INTERFACE (auth.AuditWriter, subscription.auditRecorder,
// invoice.auditWriter, dunning.AuditWriter, …), so a nil emitter does not even
// fail to type-check — and every one of them, INCLUDING the two that do hold the
// concrete *audit.Logger (customerH, creditNoteH), nil-GUARDS its emission and
// silently skips it. A concrete type buys nothing when the field is still nil-able
// and the call site tolerates nil: a missing emitter compiles, boots, serves
// traffic, and writes nothing.
//
// So a forgotten SetAuditLogger line un-audited an entire domain in silence, with
// the route registry still declaring those routes `explicit` and every gate in
// this package green. The API-key lifecycle — mint, revoke, rotate — sat behind
// exactly that hole. If you are about to add an exemption here, you are about to
// reopen it: MustWired is generic (it reflects the audit field out of any struct),
// so there is no kind of component it cannot check.
func TestMustWired_CoversEveryAuditedComponent(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	text := string(src)

	mws := regexp.MustCompile(`audit\.MustWired\(([^)]*)\)`).FindAllStringSubmatch(text, -1)
	if len(mws) == 0 {
		t.Fatal("audit.MustWired call not found in router.go")
	}
	wired := map[string]bool{}
	for _, mw := range mws {
		for _, name := range strings.Split(mw[1], ",") {
			wired[strings.TrimSpace(name)] = true
		}
	}

	// EVERY receiver of SetAuditLogger(auditLogger) — service, adapter or handler.
	for _, m := range regexp.MustCompile(`(\w+)\.SetAuditLogger\(auditLogger\)`).FindAllStringSubmatch(text, -1) {
		recv := m[1]
		if !wired[recv] {
			t.Errorf("%s.SetAuditLogger is wired in router.go but %s is missing from audit.MustWired(...) — add it, so that DROPPING that wiring line fails loudly at boot instead of silently un-auditing %s's routes", recv, recv, recv)
		}
	}
}
