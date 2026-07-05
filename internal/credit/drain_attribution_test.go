package credit_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestDrainAttribution_PersistedInMetadata is the real-Postgres proof of the
// 2026-07-05 capture leg: every drain (invoice apply + negative adjustment)
// stamps the per-block {"drained_blocks": [{block_id, take_cents}, ...]}
// list into the consuming ledger entry's metadata. Pre-fix
// drainPositiveBlocks computed exactly this list and discarded it while the
// entry's metadata was written as '{}' — so the block-level attribution any
// future per-block reversal or commit-burndown report needs was permanently
// destroyed on every drain. The reversal-semantics redesign stays deferred;
// this test pins that the DATA survives.
func TestDrainAttribution_PersistedInMetadata(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)

	creditStore := credit.NewPostgresStore(db)
	svc := credit.NewService(creditStore)
	tenantID := testutil.CreateTestTenant(t, db, "Drain Attribution")

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_drain_attr", DisplayName: "Drain Attr",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	// Two grants so a big drain spans blocks: $40 then $60 (FIFO by
	// created_at within the same kind class).
	g1, err := svc.Grant(ctx, tenantID, credit.GrantInput{
		CustomerID: cust.ID, AmountCents: 4000, Description: "g1",
	})
	if err != nil {
		t.Fatalf("grant 1: %v", err)
	}
	g2, err := svc.Grant(ctx, tenantID, credit.GrantInput{
		CustomerID: cust.ID, AmountCents: 6000, Description: "g2",
	})
	if err != nil {
		t.Fatalf("grant 2: %v", err)
	}

	type drained struct {
		BlockID   string `json:"block_id"`
		TakeCents int64  `json:"take_cents"`
	}
	// readAttribution fetches the newest entry of the given type and
	// decodes its metadata attribution list.
	readAttribution := func(entryType string) []drained {
		t.Helper()
		tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
		if err != nil {
			t.Fatalf("begin read tx: %v", err)
		}
		defer postgres.Rollback(tx)
		var raw []byte
		if err := tx.QueryRowContext(ctx, `
			SELECT metadata FROM customer_credit_ledger
			WHERE customer_id = $1 AND entry_type = $2
			ORDER BY created_at DESC, id DESC LIMIT 1`, cust.ID, entryType,
		).Scan(&raw); err != nil {
			t.Fatalf("read %s entry metadata: %v", entryType, err)
		}
		var doc struct {
			DrainedBlocks []drained `json:"drained_blocks"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode metadata %q: %v", raw, err)
		}
		return doc.DrainedBlocks
	}

	// (1) Invoice apply spanning both blocks: $70 = all of g1 ($40) +
	// $30 of g2.
	invoiceStore := invoice.NewPostgresStore(db)
	inv, err := invoiceStore.Create(ctx, tenantID, domain.Invoice{
		CustomerID: cust.ID, Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentPending,
		AmountDueCents: 7000, TotalAmountCents: 7000, SubtotalCents: 7000, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	applied, err := creditStore.ApplyToInvoiceAtomic(ctx, tenantID, cust.ID, inv.ID, "apply", 7000, time.Now().UTC())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied != 7000 {
		t.Fatalf("applied: got %d, want 7000", applied)
	}
	got := readAttribution(string(domain.CreditUsage))
	if len(got) != 2 ||
		got[0].BlockID != g1.ID || got[0].TakeCents != 4000 ||
		got[1].BlockID != g2.ID || got[1].TakeCents != 3000 {
		t.Fatalf("usage attribution: got %+v, want [{%s 4000} {%s 3000}]", got, g1.ID, g2.ID)
	}

	// (2) Negative adjustment drains the rest of g2 ($30 remains).
	if _, err := svc.Adjust(ctx, tenantID, credit.AdjustInput{
		CustomerID: cust.ID, AmountCents: -3000, Description: "clawback",
	}); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	got = readAttribution(string(domain.CreditAdjustment))
	if len(got) != 1 || got[0].BlockID != g2.ID || got[0].TakeCents != 3000 {
		t.Fatalf("adjustment attribution: got %+v, want [{%s 3000}]", got, g2.ID)
	}
}

// TestGrantTx_CarriesGrantKind pins the one-liner the reassessment found
// riding this file: GrantTx's entry literal omitted GrantKind, so every
// in-tx grant (the CN / proration path) minted a NULL-kind block —
// misclassified into the paid drain class and invisible to kind-scoped
// reporting. Also pins that GrantTx now enforces the same 'commit is
// reserved' gate as Grant.
func TestGrantTx_CarriesGrantKind(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)

	creditStore := credit.NewPostgresStore(db)
	svc := credit.NewService(creditStore)
	tenantID := testutil.CreateTestTenant(t, db, "GrantTx Kind")

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_granttx_kind", DisplayName: "GrantTx Kind",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	runInTx := func(fn func(tx *sql.Tx) error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		defer postgres.Rollback(tx)
		if err := fn(tx); err != nil {
			t.Fatalf("in-tx: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	var entry domain.CreditLedgerEntry
	runInTx(func(tx *sql.Tx) error {
		var err error
		entry, err = svc.GrantTx(ctx, tx, tenantID, credit.GrantInput{
			CustomerID: cust.ID, AmountCents: 1000, Description: "promo in-tx",
			GrantKind: domain.GrantKindPromotional, At: time.Now().UTC(),
		})
		return err
	})

	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	defer postgres.Rollback(tx)
	var kind string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(grant_kind, '') FROM customer_credit_ledger WHERE id = $1`, entry.ID,
	).Scan(&kind); err != nil {
		t.Fatalf("read grant_kind: %v", err)
	}
	if kind != string(domain.GrantKindPromotional) {
		t.Fatalf("grant_kind: got %q, want promotional (pre-fix: silently NULL)", kind)
	}

	// 'commit' stays reserved on the Tx path too.
	tx2, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	defer postgres.Rollback(tx2)
	if _, err := svc.GrantTx(ctx, tx2, tenantID, credit.GrantInput{
		CustomerID: cust.ID, AmountCents: 1000, Description: "sneaky commit",
		GrantKind: domain.GrantKindCommit,
	}); err == nil {
		t.Fatal("GrantTx must reject grant_kind=commit (reserved for invoice-finalize funding)")
	}
}
