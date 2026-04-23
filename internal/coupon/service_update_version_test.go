package coupon

import (
	"context"
	"errors"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// setupUpdateVersionSvc seeds a coupon in the mock store and returns
// helpers for exercising the optimistic-concurrency path.
func setupUpdateVersionSvc(t *testing.T) (*Service, *mockStore, string) {
	t.Helper()
	store := newMockStore()
	svc := NewService(store)

	cpn, err := svc.Create(context.Background(), "t1", CreateInput{
		Code:      "SAVE10",
		Name:      "10 off",
		Type:      domain.CouponTypeFixedAmount,
		AmountOff: 1000,
		Currency:  "USD",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if cpn.Version != 1 {
		t.Fatalf("initial version: got %d, want 1", cpn.Version)
	}
	return svc, store, cpn.ID
}

func TestUpdate_BumpsVersion(t *testing.T) {
	t.Parallel()
	svc, _, id := setupUpdateVersionSvc(t)

	newName := "10 off — Q2 promo"
	updated, err := svc.Update(context.Background(), "t1", id, UpdateInput{
		Name: &newName,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Version != 2 {
		t.Errorf("version after update: got %d, want 2", updated.Version)
	}

	// Second update bumps again.
	newerName := "10 off — Q2 promo (revised)"
	updated, err = svc.Update(context.Background(), "t1", id, UpdateInput{
		Name: &newerName,
	})
	if err != nil {
		t.Fatalf("second Update: %v", err)
	}
	if updated.Version != 3 {
		t.Errorf("version after second update: got %d, want 3", updated.Version)
	}
}

func TestUpdate_IfMatchMatches(t *testing.T) {
	t.Parallel()
	svc, _, id := setupUpdateVersionSvc(t)

	v := 1
	newName := "matched"
	updated, err := svc.Update(context.Background(), "t1", id, UpdateInput{
		Name:    &newName,
		IfMatch: &v,
	})
	if err != nil {
		t.Fatalf("Update with matching If-Match: %v", err)
	}
	if updated.Version != 2 {
		t.Errorf("version: got %d, want 2", updated.Version)
	}
}

func TestUpdate_IfMatchMismatch_ReturnsPreconditionFailed(t *testing.T) {
	t.Parallel()
	svc, _, id := setupUpdateVersionSvc(t)

	stale := 0 // anything that isn't the current version of 1
	newName := "stale edit"
	_, err := svc.Update(context.Background(), "t1", id, UpdateInput{
		Name:    &newName,
		IfMatch: &stale,
	})
	if err == nil {
		t.Fatal("Update with stale If-Match should error")
	}
	if !errors.Is(err, errs.ErrPreconditionFailed) {
		t.Errorf("error kind: got %v, want ErrPreconditionFailed", err)
	}
}

func TestUpdate_IfMatchNil_BypassesCheck(t *testing.T) {
	t.Parallel()
	// A client that doesn't send If-Match opts out of the concurrency
	// check. This preserves last-writer-wins semantics for scripts and
	// one-shot tooling that don't round-trip ETags.
	svc, _, id := setupUpdateVersionSvc(t)

	// Bump the version behind the scenes to simulate a concurrent edit.
	n := "concurrent writer"
	if _, err := svc.Update(context.Background(), "t1", id, UpdateInput{Name: &n}); err != nil {
		t.Fatalf("concurrent Update: %v", err)
	}

	// Our caller sends no If-Match — the write should still succeed
	// against the newer version.
	ours := "no-etag client"
	updated, err := svc.Update(context.Background(), "t1", id, UpdateInput{Name: &ours})
	if err != nil {
		t.Fatalf("Update without If-Match: %v", err)
	}
	if updated.Version != 3 {
		t.Errorf("version: got %d, want 3", updated.Version)
	}
}

func TestUpdate_IfMatchMismatch_StateUnchanged(t *testing.T) {
	t.Parallel()
	svc, store, id := setupUpdateVersionSvc(t)

	stale := 99
	newName := "should not apply"
	_, _ = svc.Update(context.Background(), "t1", id, UpdateInput{
		Name:    &newName,
		IfMatch: &stale,
	})

	got := store.coupons[id]
	if got.Name != "10 off" {
		t.Errorf("name leaked through on rejected update: %q", got.Name)
	}
	if got.Version != 1 {
		t.Errorf("version bumped despite rejection: %d", got.Version)
	}
}
