package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// TestAuthenticate_BackstopLockCheckedAfterPasswordVerify is the regression lock
// for the login timing oracle.
//
// The old order checked users.locked_until BEFORE VerifyPassword and returned
// early on a locked account — so a locked account answered faster (it skipped
// the ~100ms cost-12 bcrypt), a timing signal for "this is a real, locked
// account". The fix verifies the password FIRST, then the (now rare,
// manual/backstop) lock, so every path spends the same bcrypt.
//
// The observable proof: a locked account with a WRONG password returns
// ErrBadCredentials, not ErrAccountLocked — VerifyPassword ran and failed before
// the lock check was ever reached.
func TestAuthenticate_BackstopLockCheckedAfterPasswordVerify(t *testing.T) {
	future := time.Now().Add(time.Hour)
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-horse-battery-staple"), bcrypt.DefaultCost)
	locked := domain.User{ID: "usr_1", Email: "op@acme.com", PasswordHash: string(hash), LockedUntil: &future}
	store := &fakeUserStore{loginUser: &locked, tenants: []domain.UserTenant{{UserID: "usr_1", TenantID: "ten_acme"}}}
	svc := NewService(store, nil)

	// Locked + WRONG password → ErrBadCredentials (bcrypt ran, failed, and
	// returned BEFORE the lock check). Under the old pre-bcrypt lock check this
	// returned ErrAccountLocked without running bcrypt at all.
	if _, _, err := svc.Authenticate(context.Background(), "op@acme.com", "the-wrong-password"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("locked + wrong password → %v, want ErrBadCredentials (bcrypt must run before the lock check)", err)
	}

	// Locked + CORRECT password → ErrAccountLocked (the backstop is still
	// enforced, just after the constant-time verify).
	if _, _, err := svc.Authenticate(context.Background(), "op@acme.com", "correct-horse-battery-staple"); !errors.Is(err, ErrAccountLocked) {
		t.Errorf("locked + correct password → %v, want ErrAccountLocked", err)
	}
}
