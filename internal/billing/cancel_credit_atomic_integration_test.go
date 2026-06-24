package billing_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/creditnote"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/tax"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// cnGrantAdapter bridges credit.Service → creditnote.CreditGranter so Issue()
// grants the cancel-proration credit to the customer's balance (mirrors the
// production creditGrantAdapter in internal/api/adapters.go).
type cnGrantAdapter struct{ svc *credit.Service }

func (a *cnGrantAdapter) GrantForCreditNote(ctx context.Context, tenantID, creditNoteID string, in creditnote.CreditGrantInput) error {
	_, err := a.svc.GrantForCreditNote(ctx, tenantID, creditNoteID, credit.GrantInput{
		CustomerID: in.CustomerID, AmountCents: in.AmountCents, Description: in.Description, InvoiceID: in.InvoiceID,
	})
	return err
}

func (a *cnGrantAdapter) Grant(ctx context.Context, tenantID string, in creditnote.CreditGrantInput) error {
	_, err := a.svc.Grant(ctx, tenantID, credit.GrantInput{
		CustomerID: in.CustomerID, AmountCents: in.AmountCents, Description: in.Description, InvoiceID: in.InvoiceID,
	})
	return err
}

var errInjectedDraftFail = errors.New("injected cancel-credit draft create failure")

// failingDraftAdjuster fails CreateAdjustmentDraftTx — the in-tx draft-create
// failure the atomic cancel path must roll the cancel flip back from.
type failingDraftAdjuster struct{ err error }

func (f *failingDraftAdjuster) CreateAndIssueAdjustment(context.Context, string, string, int64, string, string) (domain.CreditNote, error) {
	return domain.CreditNote{}, f.err
}
func (f *failingDraftAdjuster) CreateAdjustmentDraftTx(context.Context, *sql.Tx, string, string, int64, string, string) (domain.CreditNote, error) {
	return domain.CreditNote{}, f.err
}
func (f *failingDraftAdjuster) Issue(context.Context, string, string) (domain.CreditNote, error) {
	return domain.CreditNote{}, nil
}

// TestCancelCredit_DraftFailure_RealTxRollsBackCancel is the real-Postgres proof
// of the atomic guarantee: when the in-tx cancel-credit DRAFT create fails,
// CancelAtomicWithBill must roll the status flip back — the subscription stays
// active, never canceled-with-a-silently-lost credit. The happy-path test above
// commits; this drives the failure leg through the REAL cancel tx (only a real
// tx can prove the flip is undone — the in-tx draft is created on the caller's
// coordinator tx, not a separate one).
func TestCancelCredit_DraftFailure_RealTxRollsBackCancel(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)

	customerStore := customer.NewPostgresStore(db)
	pricingStore := pricing.NewPostgresStore(db)
	subStore := subscription.NewPostgresStore(db)
	invoiceStore := invoice.NewPostgresStore(db)
	usageStore := usage.NewPostgresStore(db)
	creditStore := credit.NewPostgresStore(db)
	creditSvc := credit.NewService(creditStore)
	settingsStore := tenant.NewSettingsStore(db)

	tenantID := testutil.CreateTestTenant(t, db, "Cancel Credit Rollback")
	plan, err := pricingStore.CreatePlan(ctx, tenantID, domain.Plan{
		Code: "pro-adv-rb", Name: "Pro", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
		BaseAmountCents: 4900, BaseBillTiming: domain.BillInAdvance,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	cancelAt := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)

	e := billing.NewEngine(
		&subStoreAdapter{subStore}, &usageStoreAdapter{usageStore},
		&pricingStoreAdapter{pricingStore}, &invoiceStoreAdapter{invoiceStore},
		nil, settingsStore, nil, nil, clock.NewFake(periodStart.Add(time.Hour)),
	)
	e.SetTaxProviderResolver(tax.NewResolver(nil))
	e.SetCreditGranter(creditSvc)
	e.SetCreditNoteAdjuster(&failingDraftAdjuster{err: errInjectedDraftFail})

	cust, err := customerStore.Create(ctx, tenantID, domain.Customer{ExternalID: "cus_rb", DisplayName: "RB"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	sub, err := subStore.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-rb", DisplayName: "RB", CustomerID: cust.ID,
		Items:  []domain.SubscriptionItem{{PlanID: plan.ID, Quantity: 1}},
		Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &periodStart,
	})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	if err := subStore.UpdateBillingCycle(ctx, tenantID, sub.ID, periodStart, periodEnd, periodEnd, 0); err != nil {
		t.Fatalf("billing cycle: %v", err)
	}
	sub.CurrentBillingPeriodStart = &periodStart
	sub.CurrentBillingPeriodEnd = &periodEnd
	inv, err := e.BillOnCreate(ctx, sub)
	if err != nil {
		t.Fatalf("BillOnCreate: %v", err)
	}
	if _, err := invoiceStore.MarkPaid(ctx, tenantID, inv.ID, "pi_rb", periodStart); err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	// Cancel through the real service → the billFn calls BillOnCancelDraftsTx,
	// whose draft-create fails on the failing adjuster → the cancel tx rolls back.
	subSvc := subscription.NewService(subStore, clock.NewFake(cancelAt))
	subSvc.SetBiller(e)
	if _, _, err := subSvc.Cancel(ctx, tenantID, sub.ID); err == nil {
		t.Fatal("cancel must fail when the in-tx cancel-credit draft create fails")
	}

	after, err := subStore.Get(ctx, tenantID, sub.ID)
	if err != nil {
		t.Fatalf("get after rollback: %v", err)
	}
	if after.Status != domain.SubscriptionActive {
		t.Fatalf("cancel must roll back on the in-tx draft failure; status=%q, want active", after.Status)
	}
	if after.CanceledAt != nil {
		t.Errorf("canceled_at must be unset after rollback; got %v", after.CanceledAt)
	}
}

// TestCancelCredit_PaidInAdvance_DraftAtomicAndReconcilerRecovers is the
// headline real-Postgres proof of the ADR-057 cancel-credit extension: on an
// all-PAID in_advance cancel, BillOnCancelDraftsTx creates the proration credit
// as an issue_pending DRAFT on the caller's tx. We then SIMULATE the post-commit
// crash (never call IssueCancelDrafts) and prove RetryPendingClawbackIssue
// recovers it — the customer's owed credit lands EXACTLY ONCE, deduped on a
// second sweep. This is the crash-window the work closes: the credit can no
// longer be silently lost between the cancel commit and the post-commit issue.
func TestCancelCredit_PaidInAdvance_DraftAtomicAndReconcilerRecovers(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)

	customerStore := customer.NewPostgresStore(db)
	pricingStore := pricing.NewPostgresStore(db)
	subStore := subscription.NewPostgresStore(db)
	invoiceStore := invoice.NewPostgresStore(db)
	usageStore := usage.NewPostgresStore(db)
	creditStore := credit.NewPostgresStore(db)
	creditSvc := credit.NewService(creditStore)
	creditNoteStore := creditnote.NewPostgresStore(db)
	settingsStore := tenant.NewSettingsStore(db)

	tenantID := testutil.CreateTestTenant(t, db, "Cancel Credit Atomic")

	plan, err := pricingStore.CreatePlan(ctx, tenantID, domain.Plan{
		Code: "pro-adv", Name: "Pro Advance", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
		BaseAmountCents: 4900, BaseBillTiming: domain.BillInAdvance,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	invoiceSvc := invoice.NewService(invoiceStore, clock.Real(), settingsStore)
	creditNoteSvc := creditnote.NewService(creditNoteStore, invoiceStore, nil, &cnGrantAdapter{creditSvc})
	creditNoteSvc.SetNumberGenerator(settingsStore)

	e := billing.NewEngine(
		&subStoreAdapter{subStore}, &usageStoreAdapter{usageStore},
		&pricingStoreAdapter{pricingStore}, &invoiceStoreAdapter{invoiceStore},
		nil, settingsStore, nil, nil, clock.NewFake(periodStart.Add(time.Hour)),
	)
	e.SetTaxProviderResolver(tax.NewResolver(nil))
	e.SetCreditGranter(creditSvc)
	e.SetInvoiceVoider(invoiceSvc)
	e.SetCreditNoteAdjuster(creditNoteSvc)
	e.SetCreditHeadroomReader(creditNoteSvc)

	cust, err := customerStore.Create(ctx, tenantID, domain.Customer{ExternalID: "cus_cc", DisplayName: "CC"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	sub, err := subStore.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-cc", DisplayName: "CC", CustomerID: cust.ID,
		Items:  []domain.SubscriptionItem{{PlanID: plan.ID, Quantity: 1}},
		Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &periodStart,
	})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	if err := subStore.UpdateBillingCycle(ctx, tenantID, sub.ID, periodStart, periodEnd, periodEnd, 0); err != nil {
		t.Fatalf("set billing cycle: %v", err)
	}
	sub.CurrentBillingPeriodStart = &periodStart
	sub.CurrentBillingPeriodEnd = &periodEnd

	// Day-1 prebill, then PAY it → the funding source is PAID (the all-paid gate).
	inv, err := e.BillOnCreate(ctx, sub)
	if err != nil {
		t.Fatalf("BillOnCreate: %v", err)
	}
	if _, err := invoiceStore.MarkPaid(ctx, tenantID, inv.ID, "pi_test", periodStart); err != nil {
		t.Fatalf("mark prebill paid: %v", err)
	}

	// Cancel mid-period: Jun 16 → 15 of 30 days unused → owed 4900*15/30 = 2450.
	cancelAt := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	sub.Status = domain.SubscriptionCanceled
	sub.CanceledAt = &cancelAt

	const wantCredit = 2450

	// The atomic half: create the draft on a real tx (as Cancel's billFn does).
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	ids, credited, handled, err := e.BillOnCancelDraftsTx(ctx, tx, sub)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("BillOnCancelDraftsTx: %v", err)
	}
	if !handled {
		_ = tx.Rollback()
		t.Fatal("an all-paid in_advance cancel must be handled by the in-tx draft path")
	}
	if credited != wantCredit || len(ids) != 1 {
		_ = tx.Rollback()
		t.Fatalf("credited=%d ids=%v, want %d / 1 id", credited, ids, wantCredit)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit cancel+draft: %v", err)
	}

	// The draft is durable + pending (NOT yet issued) — the crash window: we
	// deliberately do NOT call IssueCancelDrafts.
	pending, err := creditNoteStore.ListPendingClawbackDrafts(ctx, 100, false)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	var found bool
	for _, cn := range pending {
		if cn.ID == ids[0] {
			found = true
		}
	}
	if !found {
		t.Fatal("the cancel-credit draft must be a pending issue_pending draft after commit (pre-issue)")
	}
	if bal, _ := creditSvc.GetBalance(ctx, tenantID, cust.ID); bal.BalanceCents != 0 {
		t.Fatalf("balance must be 0 before the draft is issued; got %d", bal.BalanceCents)
	}

	// Reconciler recovers the never-issued draft → credit lands exactly once.
	issued, errs := creditNoteSvc.RetryPendingClawbackIssue(ctx, 100)
	if len(errs) != 0 {
		t.Fatalf("RetryPendingClawbackIssue errs: %v", errs)
	}
	if issued != 1 {
		t.Fatalf("issued=%d, want 1", issued)
	}
	if bal, _ := creditSvc.GetBalance(ctx, tenantID, cust.ID); bal.BalanceCents != wantCredit {
		t.Fatalf("balance after reconciler issue: got %d, want %d", bal.BalanceCents, wantCredit)
	}

	// Second sweep: the issued note has dropped out of the scan → no double-credit.
	issued2, _ := creditNoteSvc.RetryPendingClawbackIssue(ctx, 100)
	if issued2 != 0 {
		t.Errorf("second sweep issued=%d, want 0 (draft already issued)", issued2)
	}
	if bal, _ := creditSvc.GetBalance(ctx, tenantID, cust.ID); bal.BalanceCents != wantCredit {
		t.Errorf("balance after second sweep: got %d, want %d (no double-credit)", bal.BalanceCents, wantCredit)
	}
}
