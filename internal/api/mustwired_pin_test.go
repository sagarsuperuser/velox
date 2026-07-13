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

	// Service/adapter receivers of SetAuditLogger(auditLogger). Handler
	// receivers end in "H" by router convention and are exempt (see doc).
	// Handler receivers of the LEGACY post-hoc Logger (invoiceH, subH, ...)
	// are exempt — they hold the concrete *audit.Logger and fail at compile
	// time. In-tx emitters are NOT exempt regardless of naming:
	// publicPaymentH is one and must appear in a (guarded) MustWired call.
	legacyHandlerExempt := map[string]bool{
		"invoiceH": true, "subH": true, "creditNoteH": true, "customerH": true,
		"settingsH": true, "authH": true, "dunningH": true, "pricingH": true,
		"tenantStripeH": true, "webhookOutH": true, "testClockH": true,
		"membersH": true, "dashboardAuthH": true, "paymentMethodsH": true,
	}
	for _, m := range regexp.MustCompile(`(\w+)\.SetAuditLogger\(auditLogger\)`).FindAllStringSubmatch(text, -1) {
		recv := m[1]
		if legacyHandlerExempt[recv] {
			continue
		}
		if !wired[recv] {
			t.Errorf("%s.SetAuditLogger is wired in router.go but %s is missing from audit.MustWired(...) — add it so a dropped wiring line fails at boot", recv, recv)
		}
	}
}
