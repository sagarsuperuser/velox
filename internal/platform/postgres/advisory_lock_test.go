package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestAdvisoryLock_Exclusive verifies that a second caller cannot acquire the
// same key while the first holds it — mirrors the "two replicas ticking
// together, only one runs" guarantee the billing scheduler depends on.
func TestAdvisoryLock_Exclusive(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const key int64 = 99_888_001

	first, ok, err := db.TryAdvisoryLock(ctx, key)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !ok {
		t.Fatal("first acquire should have succeeded on a fresh key")
	}

	_, ok2, err2 := db.TryAdvisoryLock(ctx, key)
	if err2 != nil {
		t.Fatalf("second acquire (infra err unexpected): %v", err2)
	}
	if ok2 {
		t.Fatal("second acquire should have returned !ok while first holds key")
	}

	first.Release()

	// Post-release the key must be free again — proves Release actually calls
	// pg_advisory_unlock and returns the connection so the next try can
	// reacquire on a fresh session.
	third, ok3, err := db.TryAdvisoryLock(ctx, key)
	if err != nil {
		t.Fatalf("post-release acquire: %v", err)
	}
	if !ok3 {
		t.Fatal("key should be free after Release")
	}
	third.Release()
}

// TestAdvisoryLock_DifferentKeys confirms locks are keyed — two different
// keys must both be holdable simultaneously (billing leader and outbox leader
// should not block each other).
func TestAdvisoryLock_DifferentKeys(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a, ok, err := db.TryAdvisoryLock(ctx, 99_888_101)
	if err != nil || !ok {
		t.Fatalf("key A acquire: err=%v ok=%v", err, ok)
	}
	defer a.Release()

	b, ok, err := db.TryAdvisoryLock(ctx, 99_888_102)
	if err != nil || !ok {
		t.Fatalf("key B acquire should succeed even while A is held: err=%v ok=%v", err, ok)
	}
	defer b.Release()
}

// TestAdvisoryLock_ReleaseIdempotent proves Release is safe to call twice —
// real callers may defer Release inside a loop or inside nested error paths,
// and a double-release must not panic or leak a connection.
func TestAdvisoryLock_ReleaseIdempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const key int64 = 99_888_201

	lock, ok, err := db.TryAdvisoryLock(ctx, key)
	if err != nil || !ok {
		t.Fatalf("acquire: err=%v ok=%v", err, ok)
	}
	lock.Release()
	lock.Release() // second call must be a silent no-op
}
