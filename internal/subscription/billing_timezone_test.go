package subscription

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// TestBillingTimezone_SnapshotAtCreate: a subscription snapshots the tenant
// timezone at creation (ADR-074), the peer of billing_anchor_day.
func TestBillingTimezone_SnapshotAtCreate(t *testing.T) {
	ctx := context.Background()
	svc := NewService(newMemStore(), nil)
	svc.SetSettingsReader(fakeSettings{tz: "Asia/Kolkata"})

	sub, err := svc.Create(ctx, "t1", CreateInput{
		Code: "tz-1", DisplayName: "TZ", CustomerID: "cus_1",
		Items:    []CreateItemInput{{PlanID: "pln_1"}},
		StartNow: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sub.BillingTimezone != "Asia/Kolkata" {
		t.Errorf("BillingTimezone: got %q, want Asia/Kolkata (snapshot at create)", sub.BillingTimezone)
	}

	// Unset tenant TZ snapshots the concrete "UTC", not empty — a concrete
	// immutable anchor.
	svcUTC := NewService(newMemStore(), nil)
	svcUTC.SetSettingsReader(fakeSettings{tz: ""})
	subUTC, err := svcUTC.Create(ctx, "t1", CreateInput{
		Code: "tz-utc", DisplayName: "TZ", CustomerID: "cus_1",
		Items:    []CreateItemInput{{PlanID: "pln_1"}},
		StartNow: true,
	})
	if err != nil {
		t.Fatalf("create utc: %v", err)
	}
	if subUTC.BillingTimezone != "UTC" {
		t.Errorf("unset-tenant sub BillingTimezone: got %q, want UTC", subUTC.BillingTimezone)
	}
}

// TestBillingTimezone_ImmutableUnderTenantChange is THE fix: a subscription's
// billing calendar timezone is read from its SNAPSHOT, so changing the tenant
// timezone afterward does NOT re-anchor a running subscription's date-math.
//
// Mutation-verify: revert subscriptionLocation to `s.tenantLocation(...)`
// (ignore sub.BillingTimezone) — the "still Kolkata despite NY settings"
// assertion fails.
func TestBillingTimezone_ImmutableUnderTenantChange(t *testing.T) {
	ctx := context.Background()

	// A sub created under Asia/Kolkata (UTC+5:30).
	sub := domain.Subscription{
		TenantID: "t1", BillingTimezone: "Asia/Kolkata",
		BillingTime: domain.BillingTimeAnniversary,
	}

	// The operator has SINCE changed the tenant timezone to America/New_York.
	svc := NewService(newMemStore(), nil)
	svc.SetSettingsReader(fakeSettings{tz: "America/New_York"})

	loc := svc.subscriptionLocation(ctx, sub)
	if loc.String() != "Asia/Kolkata" {
		t.Fatalf("subscriptionLocation: got %q, want Asia/Kolkata — the sub must keep its snapshot, not adopt the changed tenant TZ", loc.String())
	}

	// And the timezone genuinely changes the calendar math, so this isn't a
	// no-op: the next anniversary boundary computed in Kolkata differs from
	// the one the (now-current) New_York tenant TZ would produce.
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	nyLoc, _ := time.LoadLocation("America/New_York")
	kolkata := domain.NextBillingPeriodEnd(periodEnd, domain.BillingTimeAnniversary, domain.BillingMonthly, loc, 1)
	newyork := domain.NextBillingPeriodEnd(periodEnd, domain.BillingTimeAnniversary, domain.BillingMonthly, nyLoc, 1)
	if kolkata.Equal(newyork) {
		t.Errorf("Kolkata and New_York boundaries are equal (%v) — the test can't prove the snapshot matters", kolkata)
	}

	// Empty snapshot (legacy/unset row) falls back to the LIVE tenant TZ —
	// preserving pre-migration behavior exactly.
	legacy := domain.Subscription{TenantID: "t1", BillingTimezone: ""}
	if got := svc.subscriptionLocation(ctx, legacy).String(); got != "America/New_York" {
		t.Errorf("legacy empty-TZ sub: got %q, want fallback to live tenant America/New_York", got)
	}
}
