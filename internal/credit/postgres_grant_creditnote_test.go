package credit_test

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestGrantForCreditNote_IdempotentNoDoubleCredit asserts the real
// migration-0093 partial unique index idx_credit_ledger_credit_note_dedup
// makes GrantForCreditNote exactly-once.
//
// The paid-CN clawback path (creditnote.Issue → GrantForCreditNote) can be
// retried after a downstream-step failure (tax reversal / UpdateStatus). The
// retry calls GrantForCreditNote again with the SAME credit-note id. Without
// the DB-enforced unique index, the second Grant() appends a SECOND grant row
// and double-credits the customer. The index makes the second insert violate
// idx_credit_ledger_credit_note_dedup → store returns ErrAlreadyExists
// (code "credit_note_source_taken") → service fetches the existing grant via
// GetByCreditNoteSource and returns it as an idempotent no-op.
//
// A mem-store emulation guards this today; this test exercises the actual
// Postgres constraint + the real route in appendEntryInTx.
func TestGrantForCreditNote_IdempotentNoDoubleCredit(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)

	creditStore := credit.NewPostgresStore(db)
	svc := credit.NewService(creditStore)
	tenantID := testutil.CreateTestTenant(t, db, "Grant CN Idempotent")

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_grant_cn_idem", DisplayName: "Grant CN Idem",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	// Same shape creditnote.Issue() uses for a credit-type CN.
	const creditNoteID = "vlx_cn_grant_idem"
	input := credit.GrantInput{
		CustomerID:  cust.ID,
		AmountCents: 4000,
		Description: "Credit note CN-001 — duplicate_charge",
	}

	// First grant: restores $40 to the customer's balance.
	first, err := svc.GrantForCreditNote(ctx, tenantID, creditNoteID, input)
	if err != nil {
		t.Fatalf("first GrantForCreditNote: %v", err)
	}
	if first.AmountCents != 4000 {
		t.Fatalf("first grant amount: got %d, want 4000", first.AmountCents)
	}

	balAfterFirst, err := svc.GetBalance(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("get balance after first grant: %v", err)
	}
	if balAfterFirst.BalanceCents != 4000 {
		t.Fatalf("balance after first grant: got %d, want 4000", balAfterFirst.BalanceCents)
	}

	// Second grant: retry of Issue() after a downstream-step failure. Pre-index
	// this appended a SECOND $40 grant → balance $80. Post-index the insert hits
	// idx_credit_ledger_credit_note_dedup; GrantForCreditNote returns the
	// EXISTING grant with no error and no second row.
	second, err := svc.GrantForCreditNote(ctx, tenantID, creditNoteID, input)
	if err != nil {
		t.Fatalf("second GrantForCreditNote: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second grant should return the existing row: got id %q, want %q", second.ID, first.ID)
	}
	if second.AmountCents != 4000 {
		t.Errorf("second grant amount: got %d, want 4000 (existing grant)", second.AmountCents)
	}

	// Balance reflects a SINGLE grant, not doubled.
	balAfterSecond, err := svc.GetBalance(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("get balance after second grant: %v", err)
	}
	if balAfterSecond.BalanceCents != 4000 {
		t.Errorf("balance after second grant: got %d, want 4000 (no double-credit)", balAfterSecond.BalanceCents)
	}

	// Exactly one ledger grant row exists for this credit-note source — the
	// invariant the partial unique index enforces.
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin bypass tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var grantRows int
	if err := tx.QueryRow(`
		SELECT count(*) FROM customer_credit_ledger
		WHERE source_credit_note_id = $1
	`, creditNoteID).Scan(&grantRows); err != nil {
		t.Fatalf("count credit-note grant rows: %v", err)
	}
	if grantRows != 1 {
		t.Errorf("grant rows for credit note: got %d, want 1", grantRows)
	}
}
