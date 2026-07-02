package user

import (
	"testing"
	"time"
)

// P12: the /v1/auth per-IP limiter doesn't stop one caller flooding a
// single victim's inbox with reset emails, each request inside the IP
// budget. The per-address throttle caps sends at 3/address/hour and
// must be response-invisible (the fixed generic 200 is the
// account-enumeration defence).
//
// Mutation-verify: make allow() always return true — the over-cap
// assertions fail.
func TestResetThrottle_PerAddressCap(t *testing.T) {
	th := newResetThrottle(3, time.Hour)
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		if !th.allow("victim@example.test", base.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("send %d blocked below the cap", i+1)
		}
	}
	if th.allow("victim@example.test", base.Add(5*time.Minute)) {
		t.Error("4th send within the window allowed; want throttled")
	}
	// Case/whitespace variants hit the same bucket — the attacker
	// doesn't get 4× the budget by mixing case.
	if th.allow("  VICTIM@example.test ", base.Add(6*time.Minute)) {
		t.Error("case-variant address bypassed the throttle")
	}
	// Other addresses are unaffected.
	if !th.allow("other@example.test", base.Add(6*time.Minute)) {
		t.Error("unrelated address throttled")
	}
	// The window slides: an hour later the address may receive again.
	if !th.allow("victim@example.test", base.Add(time.Hour+7*time.Minute)) {
		t.Error("send after the window elapsed still throttled")
	}
}
