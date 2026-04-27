package importstripe

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// fakeSource yields a fixed list of customers in order.
type fakeSource struct {
	customers []*stripe.Customer
}

func (f *fakeSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	for _, c := range f.customers {
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}

// IterateProducts / IteratePrices are no-op for the customer-only tests.
// Source widened in Phase 1; Phase 0 tests opt out by yielding nothing.
func (f *fakeSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	return nil
}

func (f *fakeSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	return nil
}

func (f *fakeSource) IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error {
	return nil
}

func (f *fakeSource) IterateInvoices(ctx context.Context, fn func(*stripe.Invoice) error) error {
	return nil
}

// fakeStore is the minimal CustomerService + CustomerLookup stand-in used
// by the unit-level driver tests. It models customers in two parallel maps
// (id -> Customer, id -> BillingProfile) keyed by Velox id, plus a
// secondary index from external_id -> Velox id.
type fakeStore struct {
	customers   map[string]domain.Customer
	profiles    map[string]domain.CustomerBillingProfile
	byExternal  map[string]string
	createCalls int
	upsertCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		customers:  map[string]domain.Customer{},
		profiles:   map[string]domain.CustomerBillingProfile{},
		byExternal: map[string]string{},
	}
}

func (s *fakeStore) Create(ctx context.Context, tenantID string, in customer.CreateInput) (domain.Customer, error) {
	s.createCalls++
	id := "vlx_cus_" + in.ExternalID
	c := domain.Customer{
		ID:          id,
		TenantID:    tenantID,
		ExternalID:  in.ExternalID,
		DisplayName: in.DisplayName,
		Email:       in.Email,
		Status:      domain.CustomerStatusActive,
	}
	s.customers[id] = c
	s.byExternal[in.ExternalID] = id
	return c, nil
}

func (s *fakeStore) UpsertBillingProfile(ctx context.Context, tenantID string, bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error) {
	s.upsertCalls++
	bp.TenantID = tenantID
	s.profiles[bp.CustomerID] = bp
	return bp, nil
}

func (s *fakeStore) GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error) {
	id, ok := s.byExternal[externalID]
	if !ok {
		return domain.Customer{}, errs.ErrNotFound
	}
	return s.customers[id], nil
}

func (s *fakeStore) GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error) {
	bp, ok := s.profiles[customerID]
	if !ok {
		return domain.CustomerBillingProfile{}, errs.ErrNotFound
	}
	return bp, nil
}

func TestCustomerImporter_FirstRunInsertsAll(t *testing.T) {
	store := newFakeStore()
	src := &fakeSource{customers: []*stripe.Customer{
		loadFixture(t, "customer_full.json"),
		loadFixture(t, "customer_multi_taxid.json"),
	}}
	var buf bytes.Buffer
	report, err := NewReport(&buf)
	if err != nil {
		t.Fatalf("NewReport: %v", err)
	}
	imp := &CustomerImporter{
		Source:   src,
		Service:  store,
		Lookup:   store,
		Report:   report,
		TenantID: "ten_test",
		Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Inserted != 2 {
		t.Errorf("Inserted = %d, want 2", report.Inserted)
	}
	if report.SkippedEquiv+report.SkippedDivergent+report.Errored != 0 {
		t.Errorf("expected only inserts; got skipequiv=%d skipdiv=%d err=%d",
			report.SkippedEquiv, report.SkippedDivergent, report.Errored)
	}
	if store.createCalls != 2 {
		t.Errorf("createCalls = %d, want 2", store.createCalls)
	}
	if store.upsertCalls != 2 {
		t.Errorf("upsertCalls = %d, want 2", store.upsertCalls)
	}
	if !strings.Contains(buf.String(), "cus_NfJG2N4m6X,customer,insert") {
		t.Errorf("CSV missing expected insert row; got:\n%s", buf.String())
	}
}

func TestCustomerImporter_SecondRunIsSkipEquivalent(t *testing.T) {
	store := newFakeStore()
	src := &fakeSource{customers: []*stripe.Customer{
		loadFixture(t, "customer_full.json"),
	}}
	// First run.
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &CustomerImporter{Source: src, Service: store, Lookup: store, Report: r1, TenantID: "ten_test", Livemode: true}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if r1.Inserted != 1 {
		t.Fatalf("setup: first run should insert 1; got %d", r1.Inserted)
	}
	// Second run, identical input.
	var buf2 bytes.Buffer
	r2, _ := NewReport(&buf2)
	imp.Report = r2
	imp.Service = store // service was zeroed by struct copy; restore (paranoia, no-op here)
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if r2.SkippedEquiv != 1 {
		t.Errorf("SkippedEquiv = %d, want 1", r2.SkippedEquiv)
	}
	if r2.Inserted != 0 {
		t.Errorf("Inserted = %d on rerun, want 0", r2.Inserted)
	}
	if store.createCalls != 1 {
		t.Errorf("createCalls = %d after rerun, want 1 (no new create)", store.createCalls)
	}
}

func TestCustomerImporter_StripeChangeIsSkipDivergent(t *testing.T) {
	store := newFakeStore()
	original := loadFixture(t, "customer_full.json")
	src := &fakeSource{customers: []*stripe.Customer{original}}
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &CustomerImporter{Source: src, Service: store, Lookup: store, Report: r1, TenantID: "ten_test", Livemode: true}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// Stripe-side mutation: email changed, name unchanged.
	mutated := *original
	mutated.Email = "alice+new@example.com"
	src.customers = []*stripe.Customer{&mutated}

	var buf bytes.Buffer
	r2, _ := NewReport(&buf)
	imp.Report = r2
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	_ = r2.Close()
	if r2.SkippedDivergent != 1 {
		t.Errorf("SkippedDivergent = %d, want 1", r2.SkippedDivergent)
	}
	if !strings.Contains(buf.String(), "email stripe=") || !strings.Contains(buf.String(), "alice+new@example.com") {
		t.Errorf("CSV missing field-level email diff; got:\n%s", buf.String())
	}
}

func TestCustomerImporter_DryRunSkipsWrites(t *testing.T) {
	store := newFakeStore()
	src := &fakeSource{customers: []*stripe.Customer{
		loadFixture(t, "customer_full.json"),
		loadFixture(t, "customer_multi_taxid.json"),
	}}
	report, _ := NewReport(&bytes.Buffer{})
	imp := &CustomerImporter{
		Source: src, Service: store, Lookup: store, Report: report,
		TenantID: "ten_test", Livemode: true, DryRun: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Inserted != 2 {
		t.Errorf("Inserted (reported) = %d, want 2", report.Inserted)
	}
	if store.createCalls != 0 {
		t.Errorf("DryRun: createCalls = %d, want 0", store.createCalls)
	}
	if store.upsertCalls != 0 {
		t.Errorf("DryRun: upsertCalls = %d, want 0", store.upsertCalls)
	}
}

func TestCustomerImporter_LivemodeMismatchErrors(t *testing.T) {
	store := newFakeStore()
	cust := loadFixture(t, "customer_full.json") // livemode=true
	src := &fakeSource{customers: []*stripe.Customer{cust}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &CustomerImporter{
		Source: src, Service: store, Lookup: store, Report: report,
		TenantID: "ten_test", Livemode: false, // intentionally wrong
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "livemode mismatch") {
		t.Errorf("CSV missing livemode mismatch detail; got:\n%s", buf.String())
	}
	if store.createCalls != 0 {
		t.Errorf("livemode mismatch: createCalls = %d, want 0", store.createCalls)
	}
}

func TestCustomerImporter_EmptyIDIsError(t *testing.T) {
	store := newFakeStore()
	src := &fakeSource{customers: []*stripe.Customer{{ID: "", Livemode: true}}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &CustomerImporter{Source: src, Service: store, Lookup: store, Report: report, TenantID: "ten_test", Livemode: true}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
}
