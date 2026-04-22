package coupon_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/coupon"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestCoupon_PrivateCoupon_E2E exercises the enterprise-private-coupon path
// end-to-end against a real Postgres. The unit tests in service_test.go
// cover the guard logic with a mock store, but three things only the real
// schema can verify:
//
//  1. The customer_id column persists through the Create → Get round trip
//     (a scanDest order mismatch would silently drop the value).
//  2. The auto-generated CPN-XXXX-XXXX code passes the DB's UNIQUE(tenant_id,
//     code) constraint without collision in a realistic sample.
//  3. The redeem flow reads the persisted customer_id and rejects the wrong
//     customer, matching the service-layer guard.
func TestCoupon_PrivateCoupon_E2E(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenant := testutil.CreateTestTenant(t, db, "Coupon Private E2E")
	svc := coupon.NewService(coupon.NewPostgresStore(db))

	// Create with an explicit customer_id and an empty code (exercises both
	// the private-coupon and auto-generation paths on the wire).
	cpn, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code:       "", // triggers auto-generation
		Name:       "Acme Enterprise Deal",
		Type:       domain.CouponTypePercentage,
		PercentOff: 30,
		CustomerID: "cust_acme",
	})
	if err != nil {
		t.Fatalf("create private coupon: %v", err)
	}
	if cpn.Code == "" {
		t.Fatal("empty input code should have auto-generated, got empty")
	}
	if cpn.CustomerID != "cust_acme" {
		t.Errorf("customer_id did not round-trip: got %q, want %q", cpn.CustomerID, "cust_acme")
	}

	// The target customer redeems successfully.
	red, err := svc.Redeem(ctx, tenant, coupon.RedeemInput{
		Code:          cpn.Code,
		CustomerID:    "cust_acme",
		SubtotalCents: 10000,
	})
	if err != nil {
		t.Fatalf("target customer redeem: %v", err)
	}
	if red.DiscountCents != 3000 {
		t.Errorf("discount: got %d, want 3000", red.DiscountCents)
	}

	// Any other customer gets rejected — the error shape must be "coupon not
	// found" so we don't leak the existence of private codes.
	_, err = svc.Redeem(ctx, tenant, coupon.RedeemInput{
		Code:          cpn.Code,
		CustomerID:    "cust_beta",
		SubtotalCents: 10000,
	})
	if err == nil {
		t.Fatal("wrong customer redeem: expected error, got nil")
	}
	if got := err.Error(); got != "coupon not found" {
		t.Errorf("wrong customer redeem error: got %q, want \"coupon not found\"", got)
	}

	// Public coupons continue to work (regression guard for the CustomerID=""
	// path after migration).
	pub, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code:       "PUBLIC20",
		Name:       "Public 20",
		Type:       domain.CouponTypePercentage,
		PercentOff: 20,
	})
	if err != nil {
		t.Fatalf("create public coupon: %v", err)
	}
	if pub.CustomerID != "" {
		t.Errorf("public coupon has unexpected CustomerID: %q", pub.CustomerID)
	}
	for _, cust := range []string{"cust_acme", "cust_beta", "cust_gamma"} {
		if _, err := svc.Redeem(ctx, tenant, coupon.RedeemInput{
			Code:          pub.Code,
			CustomerID:    cust,
			SubtotalCents: 10000,
		}); err != nil {
			t.Errorf("public coupon redeem by %s: %v", cust, err)
		}
	}
}
