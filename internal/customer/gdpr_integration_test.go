package customer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// newGDPRServiceForTest wires a GDPRService against real postgres stores.
// auditLogger is nil — the service is nil-safe and the tests don't inspect
// audit writes (a dedicated audit test owns that surface).
func newGDPRServiceForTest(db *postgres.DB) *customer.GDPRService {
	return customer.NewGDPRService(
		customer.NewPostgresStore(db),
		invoice.NewPostgresStore(db),
		credit.NewPostgresStore(db),
		subscription.NewPostgresStore(db),
		nil,
	)
}

// TestGDPR_ExportCustomerData asserts the right-to-portability contract
// (GDPR Art. 15/20): every data class tied to a customer — profile, billing
// profile, payment setup, credit ledger — is returned by a single export
// call, and Stripe IDs are redacted to just their last four characters.
//
// If a field is silently dropped, a data-subject request ships an incomplete
// export and the regression only surfaces under regulator audit. Hence the
// exhaustive field-level assertions below.
func TestGDPR_ExportCustomerData(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "GDPR Export")
	customerStore := customer.NewPostgresStore(db)
	creditStore := credit.NewPostgresStore(db)

	cust, err := customerStore.Create(ctx, tenantID, domain.Customer{
		ExternalID:  "cus_gdpr_export",
		DisplayName: "Acme Corp",
		Email:       "ops@acme.example",
		Status:      domain.CustomerStatusActive,
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	if _, err := customerStore.UpsertBillingProfile(ctx, tenantID, domain.CustomerBillingProfile{
		CustomerID:    cust.ID,
		LegalName:     "Acme Corp, Inc.",
		Email:         "billing@acme.example",
		Phone:         "+1-555-0100",
		AddressLine1:  "1 Market St",
		City:          "San Francisco",
		State:         "CA",
		PostalCode:    "94103",
		Country:       "US",
		Currency:      "USD",
		ProfileStatus: domain.BillingProfileReady,
	}); err != nil {
		t.Fatalf("upsert billing profile: %v", err)
	}

	if _, err := customerStore.UpsertPaymentSetup(ctx, tenantID, domain.CustomerPaymentSetup{
		CustomerID:                  cust.ID,
		SetupStatus:                 domain.PaymentSetupReady,
		DefaultPaymentMethodPresent: true,
		PaymentMethodType:           "card",
		StripeCustomerID:            "cus_stripeABCDEFGH1234",
		StripePaymentMethodID:       "pm_stripeXYZ987654321",
		CardBrand:                   "visa",
		CardLast4:                   "4242",
		CardExpMonth:                12,
		CardExpYear:                 2030,
	}); err != nil {
		t.Fatalf("upsert payment setup: %v", err)
	}

	if _, err := creditStore.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  cust.ID,
		EntryType:   domain.CreditGrant,
		AmountCents: 10000,
		Description: "welcome credit",
	}); err != nil {
		t.Fatalf("append credit: %v", err)
	}

	export, err := newGDPRServiceForTest(db).ExportCustomerData(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if export.Customer.ID != cust.ID {
		t.Errorf("export customer id: got %q, want %q", export.Customer.ID, cust.ID)
	}
	if export.Customer.Email != "ops@acme.example" {
		t.Errorf("export customer email: got %q", export.Customer.Email)
	}
	if export.BillingProfile == nil {
		t.Fatalf("export billing profile missing")
	}
	if export.BillingProfile.LegalName != "Acme Corp, Inc." {
		t.Errorf("export legal_name: got %q", export.BillingProfile.LegalName)
	}
	if export.BillingProfile.Phone != "+1-555-0100" {
		t.Errorf("export phone: got %q", export.BillingProfile.Phone)
	}

	if export.PaymentSetup == nil {
		t.Fatalf("export payment setup missing")
	}
	// Redaction contract: Stripe IDs are exposed only as "...<last4>".
	// Full-value leakage here is a PCI/PII incident — fail loudly.
	if !strings.HasPrefix(export.PaymentSetup.StripeCustomerID, "...") ||
		!strings.HasSuffix(export.PaymentSetup.StripeCustomerID, "1234") {
		t.Errorf("stripe_customer_id not redacted: %q", export.PaymentSetup.StripeCustomerID)
	}
	if strings.Contains(export.PaymentSetup.StripeCustomerID, "ABCDEFGH") {
		t.Errorf("stripe_customer_id leaked full value: %q", export.PaymentSetup.StripeCustomerID)
	}
	if !strings.HasPrefix(export.PaymentSetup.StripePaymentMethodID, "...") ||
		!strings.HasSuffix(export.PaymentSetup.StripePaymentMethodID, "4321") {
		t.Errorf("stripe_payment_method_id not redacted: %q", export.PaymentSetup.StripePaymentMethodID)
	}
	// card_last4 is already a truncated token — keep it readable in the export
	// so the customer recognises the card on their statement.
	if export.PaymentSetup.CardLast4 != "4242" {
		t.Errorf("card_last4 unexpectedly redacted: %q", export.PaymentSetup.CardLast4)
	}

	if len(export.CreditEntries) != 1 {
		t.Fatalf("expected 1 credit entry, got %d", len(export.CreditEntries))
	}
	if export.CreditEntries[0].AmountCents != 10000 {
		t.Errorf("credit entry amount: got %d", export.CreditEntries[0].AmountCents)
	}
	if export.ExportedAt.IsZero() {
		t.Errorf("exported_at not populated")
	}
}

// TestGDPR_DeleteCustomerData_Anonymizes asserts the right-to-erasure
// contract (GDPR Art. 17): when a customer has no active subscriptions,
// Delete must blank PII on both the customer row and the billing profile,
// archive the customer, but preserve financial records (credit ledger)
// which are retained on a separate legal basis.
//
// A silent regression here (e.g., forgetting to clear phone or tax_id)
// leaves identifying data behind in plaintext.
func TestGDPR_DeleteCustomerData_Anonymizes(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "GDPR Delete")
	customerStore := customer.NewPostgresStore(db)
	creditStore := credit.NewPostgresStore(db)

	cust, err := customerStore.Create(ctx, tenantID, domain.Customer{
		ExternalID:  "cus_gdpr_delete",
		DisplayName: "Bob Identifiable",
		Email:       "bob@personal.example",
		Status:      domain.CustomerStatusActive,
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	if _, err := customerStore.UpsertBillingProfile(ctx, tenantID, domain.CustomerBillingProfile{
		CustomerID:    cust.ID,
		LegalName:     "Bob Identifiable",
		Email:         "bob@personal.example",
		Phone:         "+1-555-9999",
		AddressLine1:  "742 Evergreen Terrace",
		AddressLine2:  "Apt 2B",
		City:          "Springfield",
		State:         "IL",
		PostalCode:    "62701",
		Country:       "US",
		Currency:      "USD",
		TaxID:         "US-TAX-555-00-1234",
		TaxIDType:     "us_ein",
		ProfileStatus: domain.BillingProfileReady,
	}); err != nil {
		t.Fatalf("upsert billing profile: %v", err)
	}

	if _, err := creditStore.AppendEntry(ctx, tenantID, domain.CreditLedgerEntry{
		CustomerID:  cust.ID,
		EntryType:   domain.CreditGrant,
		AmountCents: 500,
		Description: "pre-erasure grant",
	}); err != nil {
		t.Fatalf("append credit: %v", err)
	}

	if err := newGDPRServiceForTest(db).DeleteCustomerData(ctx, tenantID, cust.ID); err != nil {
		t.Fatalf("delete customer data: %v", err)
	}

	got, err := customerStore.Get(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("re-fetch customer: %v", err)
	}
	if got.DisplayName != "Deleted Customer" {
		t.Errorf("display_name not anonymized: %q", got.DisplayName)
	}
	if got.Email != "" {
		t.Errorf("email not cleared: %q", got.Email)
	}
	if got.Status != domain.CustomerStatusArchived {
		t.Errorf("status not archived: %q", got.Status)
	}

	bp, err := customerStore.GetBillingProfile(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("re-fetch billing profile: %v", err)
	}
	// Every PII-bearing field must be blank after erasure. Iterate them in
	// one table to keep the failure mode legible when a new field is added
	// but the anonymizer is not updated.
	piiFields := map[string]string{
		"legal_name":    bp.LegalName,
		"email":         bp.Email,
		"phone":         bp.Phone,
		"address_line1": bp.AddressLine1,
		"address_line2": bp.AddressLine2,
		"city":          bp.City,
		"state":         bp.State,
		"postal_code":   bp.PostalCode,
		"tax_id":        bp.TaxID,
	}
	for field, value := range piiFields {
		if value != "" {
			t.Errorf("billing profile %s not cleared: %q", field, value)
		}
	}
	if bp.ProfileStatus != domain.BillingProfileIncomplete {
		t.Errorf("billing profile status: got %q, want incomplete", bp.ProfileStatus)
	}

	// Financial record preservation: credit ledger is retained post-erasure
	// because tax/accounting law obliges us to keep it. If this disappears,
	// the right-to-erasure flow has overreached.
	entries, err := creditStore.ListEntries(ctx, credit.ListFilter{
		TenantID:   tenantID,
		CustomerID: cust.ID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("list credit entries: %v", err)
	}
	if len(entries) != 1 || entries[0].AmountCents != 500 {
		t.Errorf("credit ledger not preserved after erasure: %+v", entries)
	}
}

// TestGDPR_DeleteCustomerData_RejectsActiveSubscription asserts the
// precondition for erasure: a customer with an active subscription cannot
// be deleted. The caller must cancel first. If this check regressed, we
// would silently anonymize a customer while Stripe still has an active
// recurring charge against them — a payments-and-compliance double-fault.
func TestGDPR_DeleteCustomerData_RejectsActiveSubscription(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "GDPR ActiveSub")
	customerStore := customer.NewPostgresStore(db)

	cust, err := customerStore.Create(ctx, tenantID, domain.Customer{
		ExternalID:  "cus_gdpr_active",
		DisplayName: "Carol Subscriber",
		Email:       "carol@example.com",
		Status:      domain.CustomerStatusActive,
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	plan, err := pricing.NewPostgresStore(db).CreatePlan(ctx, tenantID, domain.Plan{
		Code:            "plan-gdpr-active",
		Name:            "GDPR Active Plan",
		Currency:        "USD",
		BillingInterval: domain.BillingMonthly,
		Status:          domain.PlanActive,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	now := time.Now().UTC()
	if _, err := subscription.NewPostgresStore(db).Create(ctx, tenantID, domain.Subscription{
		Code:        "sub-gdpr-active",
		DisplayName: "Active Sub",
		CustomerID:  cust.ID,
		Status:      domain.SubscriptionActive,
		BillingTime: domain.BillingTimeCalendar,
		StartedAt:   &now,
		Items:       []domain.SubscriptionItem{{PlanID: plan.ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	err = newGDPRServiceForTest(db).DeleteCustomerData(ctx, tenantID, cust.ID)
	if err == nil {
		t.Fatalf("expected delete to fail with active subscription, got nil")
	}
	if !strings.Contains(err.Error(), "active subscriptions") {
		t.Errorf("expected error to mention active subscriptions, got: %v", err)
	}

	// Verify the customer was NOT mutated by the failed delete — the check
	// must run before any write, otherwise the request is neither "deleted"
	// nor "intact" and clients have no way to recover.
	got, err := customerStore.Get(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("re-fetch customer: %v", err)
	}
	if got.DisplayName != "Carol Subscriber" || got.Email != "carol@example.com" {
		t.Errorf("customer PII leaked into failed-delete path: %+v", got)
	}
	if got.Status != domain.CustomerStatusActive {
		t.Errorf("customer status flipped despite delete failure: %q", got.Status)
	}
}

// TestGDPR_DeleteCustomerData_MultiTenantIsolation proves the erasure flow
// is tenant-scoped end-to-end: deleting customer A in tenant A must not
// touch customer B in tenant B, even when both share the same external_id
// and display name. If either the customer update or the billing-profile
// update ever lost its tenant scoping, this test would catch the
// cross-tenant bleed.
func TestGDPR_DeleteCustomerData_MultiTenantIsolation(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tenantA := testutil.CreateTestTenant(t, db, "GDPR Iso A")
	tenantB := testutil.CreateTestTenant(t, db, "GDPR Iso B")
	customerStore := customer.NewPostgresStore(db)

	custA, err := customerStore.Create(ctx, tenantA, domain.Customer{
		ExternalID:  "cus_shared_external",
		DisplayName: "Shared Name",
		Email:       "a@example.com",
		Status:      domain.CustomerStatusActive,
	})
	if err != nil {
		t.Fatalf("create customer A: %v", err)
	}
	custB, err := customerStore.Create(ctx, tenantB, domain.Customer{
		ExternalID:  "cus_shared_external",
		DisplayName: "Shared Name",
		Email:       "b@example.com",
		Status:      domain.CustomerStatusActive,
	})
	if err != nil {
		t.Fatalf("create customer B: %v", err)
	}

	if _, err := customerStore.UpsertBillingProfile(ctx, tenantB, domain.CustomerBillingProfile{
		CustomerID:    custB.ID,
		LegalName:     "Tenant B Co",
		Email:         "billing-b@example.com",
		ProfileStatus: domain.BillingProfileReady,
	}); err != nil {
		t.Fatalf("upsert tenant B billing profile: %v", err)
	}

	if err := newGDPRServiceForTest(db).DeleteCustomerData(ctx, tenantA, custA.ID); err != nil {
		t.Fatalf("delete customer A: %v", err)
	}

	gotB, err := customerStore.Get(ctx, tenantB, custB.ID)
	if err != nil {
		t.Fatalf("re-fetch customer B: %v", err)
	}
	if gotB.DisplayName != "Shared Name" {
		t.Errorf("tenant B customer display_name modified: %q", gotB.DisplayName)
	}
	if gotB.Email != "b@example.com" {
		t.Errorf("tenant B customer email modified: %q", gotB.Email)
	}
	if gotB.Status != domain.CustomerStatusActive {
		t.Errorf("tenant B customer status modified: %q", gotB.Status)
	}

	bpB, err := customerStore.GetBillingProfile(ctx, tenantB, custB.ID)
	if err != nil {
		t.Fatalf("re-fetch tenant B billing profile: %v", err)
	}
	if bpB.LegalName != "Tenant B Co" || bpB.Email != "billing-b@example.com" {
		t.Errorf("tenant B billing profile modified: %+v", bpB)
	}
}
