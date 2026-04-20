package customerportal_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/customerportal"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestMagicLink_FullLoop_E2E walks the complete FEAT-3 P4.5 customer-
// initiated sign-in journey against a real Postgres. The unit tests in
// request_test.go / magiclink_test.go use fakes and therefore miss three
// things only a real DB can exercise:
//
//  1. The blind-index column (email_bidx) actually indexes the row,
//     cross-tenant FindByEmailBlindIndex fans out, and encryption of the
//     email column doesn't break the lookup.
//  2. The single-use marker on customer_portal_magic_links is atomic — a
//     second consume of the same token hits the UPDATE ... WHERE used_at
//     IS NULL predicate, returns ErrNoRows, and surfaces as ErrNotFound.
//  3. The mint-session-from-magic-link path honours the livemode on the
//     magic-link row, so a test-mode link can't mint a live-mode session.
//
// Steps:
//  1. Create two tenants. Create encryptor + blinder with real keys and
//     configure them on a shared customer.PostgresStore.
//  2. Create a customer in each tenant sharing the same email, plus a
//     third customer with a different email in the first tenant.
//  3. FindByEmailBlindIndex for the shared email returns both customers
//     across tenants; for the unique email, one match; for an unknown
//     email, zero matches.
//  4. Run MagicLinkRequestService.RequestByEmail for the shared email
//     and observe 2 captured deliveries — one per (tenant, customer).
//  5. Consume the first delivery's raw token; get back a portal session
//     whose identity matches that tenant/customer. Validate the session
//     token round-trips through Service.Validate.
//  6. Consume the same raw token again — ErrNotFound (single-use).
//  7. Mint a magic link directly, forcibly expire it in the DB, attempt
//     consume — ErrNotFound.
//  8. Normalisation: case- and whitespace-mangled input still matches
//     the canonical row (mirrors what the login form will accept).
func TestMagicLink_FullLoop_E2E(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantA := testutil.CreateTestTenant(t, db, "Magic Link E2E A")
	tenantB := testutil.CreateTestTenant(t, db, "Magic Link E2E B")

	// Real encryptor + blinder with distinct 32-byte keys. Both must be
	// non-zero or the DB row carries plaintext / NULL bidx and skips the
	// path we want to cover.
	enc, err := crypto.NewEncryptor(hexKey(0x11))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	blinder, err := crypto.NewBlinder(hexKey(0x22))
	if err != nil {
		t.Fatalf("NewBlinder: %v", err)
	}

	custStore := customer.NewPostgresStore(db)
	custStore.SetEncryptor(enc)
	custStore.SetBlinder(blinder)

	// -------------------------------------------------------------------
	// 1-2. Seed three customers: shared email × 2 tenants + a unique one.
	// -------------------------------------------------------------------
	sharedEmail := "shared@example.com"
	otherEmail := "solo@example.com"

	custA, err := custStore.Create(ctx, tenantA, domain.Customer{
		ExternalID:  "cus_shared_a",
		DisplayName: "Shared A",
		Email:       sharedEmail,
	})
	if err != nil {
		t.Fatalf("create custA: %v", err)
	}
	custB, err := custStore.Create(ctx, tenantB, domain.Customer{
		ExternalID:  "cus_shared_b",
		DisplayName: "Shared B",
		Email:       sharedEmail,
	})
	if err != nil {
		t.Fatalf("create custB: %v", err)
	}
	custSolo, err := custStore.Create(ctx, tenantA, domain.Customer{
		ExternalID:  "cus_solo",
		DisplayName: "Solo",
		Email:       otherEmail,
	})
	if err != nil {
		t.Fatalf("create custSolo: %v", err)
	}

	// Sanity: the Get round-trip decrypts the email correctly — a broken
	// encrypt/decrypt cycle would silently masquerade as a lookup bug
	// below.
	got, err := custStore.Get(ctx, tenantA, custA.ID)
	if err != nil {
		t.Fatalf("get custA: %v", err)
	}
	if got.Email != sharedEmail {
		t.Fatalf("encrypt/decrypt round-trip broke email: got %q want %q", got.Email, sharedEmail)
	}

	// -------------------------------------------------------------------
	// 3. Blind-index lookup across tenants.
	// -------------------------------------------------------------------
	matches, err := custStore.FindByEmailBlindIndex(ctx, blinder.Blind(sharedEmail), 10)
	if err != nil {
		t.Fatalf("FindByEmailBlindIndex shared: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("shared email should match 2 rows across tenants, got %d: %+v", len(matches), matches)
	}
	seenTenants := map[string]string{}
	for _, m := range matches {
		seenTenants[m.TenantID] = m.CustomerID
	}
	if seenTenants[tenantA] != custA.ID || seenTenants[tenantB] != custB.ID {
		t.Fatalf("match identity mismatch: %+v", matches)
	}

	soloMatches, err := custStore.FindByEmailBlindIndex(ctx, blinder.Blind(otherEmail), 10)
	if err != nil {
		t.Fatalf("FindByEmailBlindIndex solo: %v", err)
	}
	if len(soloMatches) != 1 || soloMatches[0].CustomerID != custSolo.ID {
		t.Fatalf("solo lookup want [%s], got %+v", custSolo.ID, soloMatches)
	}

	nothing, err := custStore.FindByEmailBlindIndex(ctx, blinder.Blind("ghost@example.com"), 10)
	if err != nil {
		t.Fatalf("FindByEmailBlindIndex ghost: %v", err)
	}
	if len(nothing) != 0 {
		t.Fatalf("unknown email should match zero rows, got %d", len(nothing))
	}

	// -------------------------------------------------------------------
	// 4. Wire the full magic-link service stack against the same DB and
	// run a RequestByEmail with a capturing delivery. Two deliveries, one
	// per (tenant, customer) for the shared email.
	// -------------------------------------------------------------------
	portalSvc := customerportal.NewService(customerportal.NewPostgresStore(db))
	magicStore := customerportal.NewPostgresMagicLinkStore(db)
	magicSvc := customerportal.NewMagicLinkService(magicStore, portalSvc)
	delivery := &captureDelivery{}
	requestSvc := customerportal.NewMagicLinkRequestService(
		magicSvc,
		&lookupAdapter{store: custStore},
		blinder,
		delivery,
		nil,
	)

	if err := requestSvc.RequestByEmail(ctx, sharedEmail); err != nil {
		t.Fatalf("RequestByEmail shared: %v", err)
	}
	calls := delivery.snap()
	if len(calls) != 2 {
		t.Fatalf("shared email should produce 2 deliveries, got %d", len(calls))
	}
	byTenant := map[string]captureCall{}
	for _, c := range calls {
		byTenant[c.TenantID] = c
	}
	if byTenant[tenantA].CustomerID != custA.ID || byTenant[tenantB].CustomerID != custB.ID {
		t.Fatalf("delivery identity mismatch: %+v", calls)
	}

	// -------------------------------------------------------------------
	// 5. Consume the first raw token → session. Validate it independently.
	// -------------------------------------------------------------------
	firstToken := byTenant[tenantA].RawToken
	consumed, err := magicSvc.Consume(ctx, firstToken)
	if err != nil {
		t.Fatalf("Consume first token: %v", err)
	}
	if consumed.Session.TenantID != tenantA || consumed.Session.CustomerID != custA.ID {
		t.Fatalf("consumed session identity mismatch: got (%s, %s) want (%s, %s)",
			consumed.Session.TenantID, consumed.Session.CustomerID, tenantA, custA.ID)
	}
	validated, err := portalSvc.Validate(ctx, consumed.RawToken)
	if err != nil {
		t.Fatalf("Validate session: %v", err)
	}
	if validated.CustomerID != custA.ID {
		t.Fatalf("validated session customer_id: want %s, got %s", custA.ID, validated.CustomerID)
	}

	// -------------------------------------------------------------------
	// 6. Second consume of the same token must fail — single-use is the
	// main reason the atomic UPDATE ... WHERE used_at IS NULL lives.
	// -------------------------------------------------------------------
	if _, err := magicSvc.Consume(ctx, firstToken); err == nil {
		t.Fatalf("reusing magic token should fail; second consume succeeded")
	} else if err != errs.ErrNotFound {
		t.Fatalf("reused token should surface ErrNotFound, got %v", err)
	}

	// -------------------------------------------------------------------
	// 7. Expired-link branch: mint a fresh one, backdate expires_at, try
	// to consume. The same 401 / ErrNotFound must apply so the caller
	// can't tell "already used" from "expired".
	// -------------------------------------------------------------------
	expired, err := magicSvc.Mint(ctx, tenantA, custA.ID)
	if err != nil {
		t.Fatalf("Mint for expiry case: %v", err)
	}
	forceExpire(t, db, expired.Link.ID)
	if _, err := magicSvc.Consume(ctx, expired.RawToken); err == nil {
		t.Fatalf("expired token should not consume")
	} else if err != errs.ErrNotFound {
		t.Fatalf("expired token should surface ErrNotFound, got %v", err)
	}

	// -------------------------------------------------------------------
	// 8. Normalised input variant still matches both customers.
	// -------------------------------------------------------------------
	delivery.reset()
	if err := requestSvc.RequestByEmail(ctx, "  Shared@EXAMPLE.com  "); err != nil {
		t.Fatalf("RequestByEmail normalised: %v", err)
	}
	if n := len(delivery.snap()); n != 2 {
		t.Fatalf("normalised input should match 2 rows, got %d deliveries", n)
	}
}

// hexKey builds a 64-char hex string from a single byte — each byte
// repeats to fill 32 bytes. Deterministic across runs.
func hexKey(seed byte) string {
	buf := make([]byte, 32)
	for i := range buf {
		buf[i] = seed
	}
	return hex.EncodeToString(buf)
}

// forceExpire sidesteps RLS via TxBypass so the test can shift a magic
// link into the expired bucket without waiting 15 minutes.
func forceExpire(t *testing.T, db *postgres.DB, linkID string) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("forceExpire begin: %v", err)
	}
	defer postgres.Rollback(tx)
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE customer_portal_magic_links SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		linkID); err != nil {
		t.Fatalf("forceExpire update: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("forceExpire commit: %v", err)
	}
}

// lookupAdapter bridges customer.PostgresStore → customerportal.CustomerLookup.
// Mirrors the production adapter at internal/api/adapters.go so the test
// exercises the same shape of wiring.
type lookupAdapter struct {
	store *customer.PostgresStore
}

func (a *lookupAdapter) FindByEmailBlindIndex(ctx context.Context, blind string, limit int) ([]customerportal.CustomerMatch, error) {
	matches, err := a.store.FindByEmailBlindIndex(ctx, blind, limit)
	if err != nil {
		return nil, err
	}
	out := make([]customerportal.CustomerMatch, len(matches))
	for i, m := range matches {
		out[i] = customerportal.CustomerMatch{
			TenantID:   m.TenantID,
			CustomerID: m.CustomerID,
			Livemode:   m.Livemode,
		}
	}
	return out, nil
}

// captureDelivery / captureCall mirror the in-package fake; redeclared
// here because the test lives in _test (black-box) and can't reach the
// unexported helper.
type captureDelivery struct {
	calls []captureCall
}

type captureCall struct {
	TenantID   string
	CustomerID string
	RawToken   string
}

func (c *captureDelivery) DeliverMagicLink(_ context.Context, tenantID, customerID, rawToken string) error {
	c.calls = append(c.calls, captureCall{tenantID, customerID, rawToken})
	return nil
}

func (c *captureDelivery) snap() []captureCall {
	out := make([]captureCall, len(c.calls))
	copy(out, c.calls)
	return out
}

func (c *captureDelivery) reset() {
	c.calls = nil
}
