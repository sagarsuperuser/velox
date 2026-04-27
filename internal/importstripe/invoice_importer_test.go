package importstripe

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// fakeInvoiceSource yields a fixed list of invoices in order. Other
// iterators are no-ops because the driver test only exercises the invoice
// path.
type fakeInvoiceSource struct {
	invoices []*stripe.Invoice
}

func (f *fakeInvoiceSource) IterateCustomers(ctx context.Context, fn func(*stripe.Customer) error) error {
	return nil
}

func (f *fakeInvoiceSource) IterateProducts(ctx context.Context, fn func(*stripe.Product) error) error {
	return nil
}

func (f *fakeInvoiceSource) IteratePrices(ctx context.Context, fn func(*stripe.Price) error) error {
	return nil
}

func (f *fakeInvoiceSource) IterateSubscriptions(ctx context.Context, fn func(*stripe.Subscription) error) error {
	return nil
}

func (f *fakeInvoiceSource) IterateInvoices(ctx context.Context, fn func(*stripe.Invoice) error) error {
	for _, inv := range f.invoices {
		if err := fn(inv); err != nil {
			return err
		}
	}
	return nil
}

// fakeInvoiceStore is a minimal in-memory InvoiceStore used by driver tests.
// Models invoices keyed by Velox id with a secondary index by Stripe
// invoice id for the GetByStripeInvoiceID lookup.
type fakeInvoiceStore struct {
	invoices            map[string]domain.Invoice
	bySID               map[string]string
	lineItems           map[string][]domain.InvoiceLineItem
	createCalls         int
	createdInvoiceIDs   []string
	failNextCreate      error
	failNextLookup      error
	failOnceLookup      bool
	storedExternalLines map[string][]domain.InvoiceLineItem
}

func newFakeInvoiceStore() *fakeInvoiceStore {
	return &fakeInvoiceStore{
		invoices:            map[string]domain.Invoice{},
		bySID:               map[string]string{},
		lineItems:           map[string][]domain.InvoiceLineItem{},
		storedExternalLines: map[string][]domain.InvoiceLineItem{},
	}
}

func (s *fakeInvoiceStore) CreateWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	s.createCalls++
	if s.failNextCreate != nil {
		err := s.failNextCreate
		s.failNextCreate = nil
		return domain.Invoice{}, err
	}
	id := "vlx_inv_" + inv.StripeInvoiceID
	inv.ID = id
	inv.TenantID = tenantID
	inv.CreatedAt = time.Now().UTC()
	inv.UpdatedAt = inv.CreatedAt
	s.invoices[id] = inv
	if inv.StripeInvoiceID != "" {
		s.bySID[inv.StripeInvoiceID] = id
	}
	// Hydrate line items with synthetic IDs and copy them so the round-trip
	// looks like the real store.
	hydrated := make([]domain.InvoiceLineItem, 0, len(items))
	for i, li := range items {
		li.ID = "vlx_ili_" + inv.StripeInvoiceID + "_" + itoa(i)
		li.InvoiceID = id
		li.TenantID = tenantID
		hydrated = append(hydrated, li)
	}
	s.lineItems[id] = hydrated
	s.createdInvoiceIDs = append(s.createdInvoiceIDs, id)
	return inv, nil
}

func (s *fakeInvoiceStore) GetByStripeInvoiceID(ctx context.Context, tenantID, stripeInvoiceID string) (domain.Invoice, error) {
	if s.failOnceLookup {
		s.failOnceLookup = false
		return domain.Invoice{}, s.failNextLookup
	}
	id, ok := s.bySID[stripeInvoiceID]
	if !ok {
		return domain.Invoice{}, errs.ErrNotFound
	}
	return s.invoices[id], nil
}

func (s *fakeInvoiceStore) ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error) {
	return s.lineItems[invoiceID], nil
}

// itoa is a tiny int→string helper so tests don't take a strconv dep just
// for synthetic ids.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// fakeInvoiceCustomerLookup is the minimal InvoiceCustomerLookup stand-in.
type fakeInvoiceCustomerLookup struct {
	byExternal map[string]domain.Customer
}

func (f *fakeInvoiceCustomerLookup) GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error) {
	c, ok := f.byExternal[externalID]
	if !ok {
		return domain.Customer{}, errs.ErrNotFound
	}
	return c, nil
}

// fakeInvoiceSubscriptionLookup is the minimal InvoiceSubscriptionLookup
// stand-in. Pre-seeded with the subs the invoice expects.
type fakeInvoiceSubscriptionLookup struct {
	subs []domain.Subscription
}

func (f *fakeInvoiceSubscriptionLookup) List(ctx context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error) {
	return f.subs, len(f.subs), nil
}

// seedInvoiceDeps wires customer + subscription lookups in one place.
func seedInvoiceDeps() (*fakeInvoiceStore, *fakeInvoiceCustomerLookup, *fakeInvoiceSubscriptionLookup) {
	store := newFakeInvoiceStore()
	customers := &fakeInvoiceCustomerLookup{byExternal: map[string]domain.Customer{
		"cus_paid_001":          {ID: "vlx_cus_paid_001", ExternalID: "cus_paid_001"},
		"cus_void_001":          {ID: "vlx_cus_void_001", ExternalID: "cus_void_001"},
		"cus_uncollectible_001": {ID: "vlx_cus_uncoll_001", ExternalID: "cus_uncollectible_001"},
		"cus_multi_001":         {ID: "vlx_cus_multi_001", ExternalID: "cus_multi_001"},
		"cus_full_001":          {ID: "vlx_cus_full_001", ExternalID: "cus_full_001"},
		"cus_extras_001":        {ID: "vlx_cus_extras_001", ExternalID: "cus_extras_001"},
		"cus_nosub_001":         {ID: "vlx_cus_nosub_001", ExternalID: "cus_nosub_001"},
		"cus_unknown_reason_001": {
			ID: "vlx_cus_unknown_reason_001", ExternalID: "cus_unknown_reason_001",
		},
		"cus_open_001": {ID: "vlx_cus_open_001", ExternalID: "cus_open_001"},
	}}
	subs := &fakeInvoiceSubscriptionLookup{subs: []domain.Subscription{
		{ID: "vlx_sub_paid_001", Code: "sub_paid_001"},
		{ID: "vlx_sub_void_001", Code: "sub_void_001"},
		{ID: "vlx_sub_uncoll_001", Code: "sub_uncollectible_001"},
		{ID: "vlx_sub_multi_001", Code: "sub_multi_001"},
		{ID: "vlx_sub_full_001", Code: "sub_full_001"},
		{ID: "vlx_sub_extras_001", Code: "sub_extras_001"},
		{ID: "vlx_sub_unknown_001", Code: "sub_unknown_reason_001"},
	}}
	return store, customers, subs
}

func TestInvoiceImporter_FirstRunInsertsPaid(t *testing.T) {
	store, customers, subs := seedInvoiceDeps()
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_paid.json"),
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &InvoiceImporter{
		Source:             src,
		Store:              store,
		CustomerLookup:     customers,
		SubscriptionLookup: subs,
		Report:             report,
		TenantID:           "ten_test",
		Livemode:           true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Inserted != 1 {
		t.Errorf("Inserted = %d, want 1; CSV:\n%s", report.Inserted, buf.String())
	}
	if store.createCalls != 1 {
		t.Errorf("createCalls = %d, want 1", store.createCalls)
	}
	id := store.bySID["in_paid_001"]
	persisted := store.invoices[id]
	if persisted.CustomerID != "vlx_cus_paid_001" {
		t.Errorf("CustomerID = %q, want vlx_cus_paid_001", persisted.CustomerID)
	}
	if persisted.SubscriptionID != "vlx_sub_paid_001" {
		t.Errorf("SubscriptionID = %q, want vlx_sub_paid_001", persisted.SubscriptionID)
	}
	if persisted.Status != domain.InvoicePaid {
		t.Errorf("Status = %q, want paid", persisted.Status)
	}
	if persisted.PaidAt == nil {
		t.Error("PaidAt is nil; want set")
	}
	if !strings.Contains(buf.String(), "in_paid_001,invoice,insert") {
		t.Errorf("CSV missing expected insert row; got:\n%s", buf.String())
	}
	// Line items persisted.
	lines := store.lineItems[id]
	if len(lines) != 1 {
		t.Fatalf("lineItems = %d, want 1", len(lines))
	}
	if lines[0].AmountCents != 4999 {
		t.Errorf("lineItems[0].AmountCents = %d, want 4999", lines[0].AmountCents)
	}
}

func TestInvoiceImporter_SecondRunIsSkipEquivalent(t *testing.T) {
	store, customers, subs := seedInvoiceDeps()
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_paid.json"),
	}}
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: r1, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if r1.Inserted != 1 {
		t.Fatalf("setup: first run should insert 1; got %d", r1.Inserted)
	}
	r2, _ := NewReport(&bytes.Buffer{})
	imp.Report = r2
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if r2.SkippedEquiv != 1 {
		t.Errorf("SkippedEquiv = %d, want 1", r2.SkippedEquiv)
	}
	if store.createCalls != 1 {
		t.Errorf("createCalls = %d after rerun, want 1", store.createCalls)
	}
}

func TestInvoiceImporter_StripeChangeIsSkipDivergent(t *testing.T) {
	store, customers, subs := seedInvoiceDeps()
	original := loadInvoiceFixture(t, "invoice_paid.json")
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{original}}
	r1, _ := NewReport(&bytes.Buffer{})
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: r1, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Mutate Stripe-side: subtotal goes from 4999 to 5999.
	mutated := *original
	mutated.Subtotal = 5999
	mutated.Total = 5999
	src.invoices = []*stripe.Invoice{&mutated}
	var buf bytes.Buffer
	r2, _ := NewReport(&buf)
	imp.Report = r2
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	_ = r2.Close()
	if r2.SkippedDivergent != 1 {
		t.Errorf("SkippedDivergent = %d, want 1; CSV:\n%s", r2.SkippedDivergent, buf.String())
	}
	if !strings.Contains(buf.String(), "subtotal_cents stripe=") {
		t.Errorf("CSV missing subtotal_cents diff; got:\n%s", buf.String())
	}
	// Confirm the persisted invoice retained the original totals (no overwrite).
	persisted := store.invoices[store.bySID["in_paid_001"]]
	if persisted.SubtotalCents != 4999 {
		t.Errorf("invoice subtotal overwritten: got %d, want 4999", persisted.SubtotalCents)
	}
}

func TestInvoiceImporter_DryRunSkipsWrites(t *testing.T) {
	store, customers, subs := seedInvoiceDeps()
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_paid.json"),
	}}
	report, _ := NewReport(&bytes.Buffer{})
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: report, TenantID: "ten_test", Livemode: true,
		DryRun: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Inserted != 1 {
		t.Errorf("Inserted (reported) = %d, want 1", report.Inserted)
	}
	if store.createCalls != 0 {
		t.Errorf("DryRun: createCalls = %d, want 0", store.createCalls)
	}
}

func TestInvoiceImporter_LivemodeMismatchErrors(t *testing.T) {
	store, customers, subs := seedInvoiceDeps()
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_paid.json"), // livemode=true
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: report, TenantID: "ten_test", Livemode: false,
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
		t.Errorf("createCalls = %d, want 0 on livemode mismatch", store.createCalls)
	}
}

func TestInvoiceImporter_OpenInvoiceErrors(t *testing.T) {
	store, customers, subs := seedInvoiceDeps()
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_open_rejected.json"),
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "Phase 3 only imports finalized invoices") {
		t.Errorf("CSV missing finalized-only detail; got:\n%s", buf.String())
	}
	if store.createCalls != 0 {
		t.Errorf("createCalls = %d, want 0 on open invoice", store.createCalls)
	}
}

func TestInvoiceImporter_MissingCustomerErrors(t *testing.T) {
	store := newFakeInvoiceStore()
	customers := &fakeInvoiceCustomerLookup{byExternal: map[string]domain.Customer{}} // empty
	subs := &fakeInvoiceSubscriptionLookup{subs: []domain.Subscription{}}
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_paid.json"),
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "run --resource=customers first") {
		t.Errorf("CSV missing actionable customer-missing detail; got:\n%s", buf.String())
	}
}

func TestInvoiceImporter_MissingSubscriptionErrors(t *testing.T) {
	store := newFakeInvoiceStore()
	customers := &fakeInvoiceCustomerLookup{byExternal: map[string]domain.Customer{
		"cus_paid_001": {ID: "vlx_cus_paid_001", ExternalID: "cus_paid_001"},
	}}
	subs := &fakeInvoiceSubscriptionLookup{subs: []domain.Subscription{}} // empty
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_paid.json"),
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "run --resource=subscriptions first") {
		t.Errorf("CSV missing actionable subscription-missing detail; got:\n%s", buf.String())
	}
}

func TestInvoiceImporter_ManualInvoiceWithoutSubscription(t *testing.T) {
	store, customers, subs := seedInvoiceDeps()
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_no_subscription.json"),
	}}
	report, _ := NewReport(&bytes.Buffer{})
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Inserted != 1 {
		t.Errorf("Inserted = %d, want 1 (manual invoice w/o sub should still import)", report.Inserted)
	}
	persisted := store.invoices[store.bySID["in_nosub_001"]]
	if persisted.SubscriptionID != "" {
		t.Errorf("SubscriptionID = %q, want empty (manual invoice)", persisted.SubscriptionID)
	}
	if persisted.BillingReason != domain.BillingReasonManual {
		t.Errorf("BillingReason = %q, want manual", persisted.BillingReason)
	}
}

func TestInvoiceImporter_MultiLineInsertsAllLines(t *testing.T) {
	store, customers, subs := seedInvoiceDeps()
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_multi_line.json"),
	}}
	report, _ := NewReport(&bytes.Buffer{})
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Inserted != 1 {
		t.Errorf("Inserted = %d, want 1", report.Inserted)
	}
	id := store.bySID["in_multi_001"]
	lines := store.lineItems[id]
	if len(lines) != 3 {
		t.Errorf("lineItems = %d, want 3 (multi-line invoice)", len(lines))
	}
}

// errFakeStorage is a synthetic fault used to exercise the create-failed path.
type errFakeStorage struct{ msg string }

func (e errFakeStorage) Error() string { return e.msg }

func TestInvoiceImporter_CreateFailureSurfacesAsError(t *testing.T) {
	store, customers, subs := seedInvoiceDeps()
	store.failNextCreate = errFakeStorage{msg: "synthetic db failure"}
	src := &fakeInvoiceSource{invoices: []*stripe.Invoice{
		loadInvoiceFixture(t, "invoice_paid.json"),
	}}
	var buf bytes.Buffer
	report, _ := NewReport(&buf)
	imp := &InvoiceImporter{
		Source: src, Store: store, CustomerLookup: customers,
		SubscriptionLookup: subs, Report: report, TenantID: "ten_test", Livemode: true,
	}
	if err := imp.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = report.Close()
	if report.Errored != 1 {
		t.Errorf("Errored = %d, want 1", report.Errored)
	}
	if !strings.Contains(buf.String(), "synthetic db failure") {
		t.Errorf("CSV missing create error detail; got:\n%s", buf.String())
	}
}
