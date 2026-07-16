package user_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/user"
)

const testThrottlePepper = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestPostgresLoginGuard_ThrottlesAttackerNotVictim is the non-weaponizable
// proof: a single source hammering one account gets throttled, but the account's
// REAL OWNER — arriving from a different IP-prefix — is never throttled. This is
// exactly what the removed users.locked_until lockout got wrong (a bare-account
// lock any known email could trip). Real Postgres because the whole point is the
// authoritative single-store counter.
func TestPostgresLoginGuard_ThrottlesAttackerNotVictim(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	blinder, err := crypto.NewBlinder(testThrottlePepper)
	if err != nil {
		t.Fatalf("blinder: %v", err)
	}
	g := user.NewPostgresLoginGuard(db, blinder)

	const account = "victim-attacker-split@example.test"
	const attackerIP = "198.51.100.7"
	const victimIP = "203.0.113.20" // a genuinely different /24 and /64

	if g.Check(ctx, account, attackerIP).Deny {
		t.Fatal("throttled before any failure was recorded")
	}

	// The attacker hammers the account from ONE IP, up to the threshold.
	for i := 0; i < user.LoginThrottleThreshold; i++ {
		g.Record(ctx, account, attackerIP, false)
	}

	if !g.Check(ctx, account, attackerIP).Deny {
		t.Error("attacker IP not throttled after threshold failures against the account")
	}
	// The load-bearing assertion: the real owner from a different IP is untouched.
	if g.Check(ctx, account, victimIP).Deny {
		t.Error("victim IP throttled — the throttle is weaponizable (behaving like a bare-account lock)")
	}

	// A success from the attacker's own source clears its counter (it proved it
	// holds the credential).
	g.Record(ctx, account, attackerIP, true)
	if g.Check(ctx, account, attackerIP).Deny {
		t.Error("counter not cleared after a successful login from the same source")
	}
}

// TestPostgresLoginGuard_StoresNoPlaintextEmail pins that the throttle key is a
// hash, never the plaintext email — closing the audit-#485-class leak the old
// Redis key (velox:login_fail:<PLAINTEXT-EMAIL>) had.
func TestPostgresLoginGuard_StoresNoPlaintextEmail(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	g := user.NewPostgresLoginGuard(db, crypto.NewNoopBlinder()) // even the dev sha256 fallback must not store plaintext

	const account = "leaky-canary@example.test"
	g.Record(ctx, account, "198.51.100.9", false)

	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer postgres.Rollback(tx)

	var found bool
	rows, err := tx.QueryContext(ctx, `SELECT subject FROM login_throttle`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if strings.Contains(subject, account) {
			t.Errorf("login_throttle.subject contains the plaintext email %q: %q", account, subject)
		}
		if strings.Contains(subject, "leaky-canary") {
			t.Errorf("subject leaks the local-part of the email: %q", subject)
		}
		found = true
	}
	if !found {
		t.Error("no throttle row was written — the test proved nothing")
	}
}
