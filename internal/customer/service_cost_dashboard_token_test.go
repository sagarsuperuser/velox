package customer

import (
	"context"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/errs"
)

// TestService_RotateCostDashboardToken_HappyPath asserts the service
// mints a vlx_pcd_… token, persists it to the store, and returns the
// token to the caller. Doesn't depend on postgres — uses the existing
// memoryStore mock so the test stays a fast unit test.
func TestService_RotateCostDashboardToken_HappyPath(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	cust, err := svc.Create(ctx, "tenant1", CreateInput{
		ExternalID:  "cus_rotate_001",
		DisplayName: "Rotator",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	token, err := svc.RotateCostDashboardToken(ctx, "tenant1", cust.ID)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !strings.HasPrefix(token, CostDashboardTokenPrefix) {
		t.Errorf("token prefix: got %q, want prefix %q", token, CostDashboardTokenPrefix)
	}
	// 32 bytes hex = 64 chars, plus the "vlx_pcd_" prefix = 8 chars → 72 total.
	if len(token) != len(CostDashboardTokenPrefix)+64 {
		t.Errorf("token length: got %d, want %d", len(token), len(CostDashboardTokenPrefix)+64)
	}

	// The lookup-by-token path must round-trip the same customer the
	// rotation just minted the token for. Confirms the service writes
	// to the right column and the store mock's lookup honors it.
	got, err := svc.GetByCostDashboardToken(ctx, token)
	if err != nil {
		t.Fatalf("get by token: %v", err)
	}
	if got.ID != cust.ID {
		t.Errorf("token resolved to wrong customer: got %q, want %q", got.ID, cust.ID)
	}
	if got.CostDashboardToken != token {
		t.Errorf("returned customer missing token: got %q, want %q", got.CostDashboardToken, token)
	}
}

// TestService_RotateCostDashboardToken_NotFound covers the canonical
// 404 path: rotating a token for a customer ID that doesn't exist (or
// belongs to another tenant) returns ErrNotFound, never an empty token.
func TestService_RotateCostDashboardToken_NotFound(t *testing.T) {
	svc := NewService(newMemoryStore())
	ctx := context.Background()

	if _, err := svc.RotateCostDashboardToken(ctx, "tenant1", "vlx_cus_nonexistent"); err != errs.ErrNotFound {
		t.Errorf("rotate unknown id: got %v, want ErrNotFound", err)
	}
}

// TestService_RotateCostDashboardToken_ReplacesPrevious is the
// rotation-as-invalidation guarantee. After a rotation, the OLD token
// must no longer resolve any customer. If it did, an operator who
// rotated in response to a leaked URL would still be exposed.
func TestService_RotateCostDashboardToken_ReplacesPrevious(t *testing.T) {
	store := newMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	cust, _ := svc.Create(ctx, "tenant1", CreateInput{
		ExternalID:  "cus_rotate_replace",
		DisplayName: "Replacer",
	})

	tokenOld, err := svc.RotateCostDashboardToken(ctx, "tenant1", cust.ID)
	if err != nil {
		t.Fatalf("first rotate: %v", err)
	}
	tokenNew, err := svc.RotateCostDashboardToken(ctx, "tenant1", cust.ID)
	if err != nil {
		t.Fatalf("second rotate: %v", err)
	}
	if tokenOld == tokenNew {
		t.Fatal("second rotation produced same token (entropy collision should be ~0; bug is more likely)")
	}

	if _, err := svc.GetByCostDashboardToken(ctx, tokenOld); err != errs.ErrNotFound {
		t.Errorf("old token after rotation: got %v, want ErrNotFound", err)
	}
	got, err := svc.GetByCostDashboardToken(ctx, tokenNew)
	if err != nil {
		t.Fatalf("new token lookup: %v", err)
	}
	if got.ID != cust.ID {
		t.Errorf("new token: got customer %q, want %q", got.ID, cust.ID)
	}
}

// TestService_GetByCostDashboardToken_EmptyAndUnknown asserts the
// uniform "lookup miss" surface: empty string and bogus token both
// surface as ErrNotFound, never as a successful empty-customer return.
func TestService_GetByCostDashboardToken_EmptyAndUnknown(t *testing.T) {
	svc := NewService(newMemoryStore())
	ctx := context.Background()

	if _, err := svc.GetByCostDashboardToken(ctx, ""); err != errs.ErrNotFound {
		t.Errorf("empty token: got %v, want ErrNotFound", err)
	}
	if _, err := svc.GetByCostDashboardToken(ctx, "vlx_pcd_unknown"); err != errs.ErrNotFound {
		t.Errorf("unknown token: got %v, want ErrNotFound", err)
	}
}
