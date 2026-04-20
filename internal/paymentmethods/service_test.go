package paymentmethods

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// memStore is a tight in-memory stand-in for PostgresStore. It mirrors the
// invariants enforced by the schema — unique (tenant,livemode,stripe_pm),
// at most one active default per customer — so Service-level tests see
// the same semantics as production.
type memStore struct {
	mu   sync.Mutex
	next int
	rows map[string]PaymentMethod // id → pm
}

func newMemStore() *memStore { return &memStore{rows: map[string]PaymentMethod{}} }

func (m *memStore) List(_ context.Context, tenantID, customerID string) ([]PaymentMethod, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []PaymentMethod
	for _, pm := range m.rows {
		if pm.TenantID == tenantID && pm.CustomerID == customerID && pm.DetachedAt == nil {
			out = append(out, pm)
		}
	}
	// defaults first, then newest first — mirrors store.go ordering
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if lessPM(out[j], out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

func lessPM(a, b PaymentMethod) bool {
	if a.IsDefault != b.IsDefault {
		return a.IsDefault
	}
	return a.CreatedAt.After(b.CreatedAt)
}

func (m *memStore) Get(_ context.Context, tenantID, pmID string) (PaymentMethod, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pm, ok := m.rows[pmID]
	if !ok || pm.TenantID != tenantID {
		return PaymentMethod{}, errs.ErrNotFound
	}
	return pm, nil
}

func (m *memStore) Upsert(_ context.Context, tenantID string, in PaymentMethod) (PaymentMethod, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, existing := range m.rows {
		if existing.TenantID == tenantID && existing.StripePaymentMethodID == in.StripePaymentMethodID {
			existing.CardBrand, existing.CardLast4 = in.CardBrand, in.CardLast4
			existing.CardExpMonth, existing.CardExpYear = in.CardExpMonth, in.CardExpYear
			existing.DetachedAt = nil
			existing.UpdatedAt = time.Now().UTC()
			m.rows[id] = existing
			return existing, nil
		}
	}

	hasDefault := false
	for _, existing := range m.rows {
		if existing.TenantID == tenantID && existing.CustomerID == in.CustomerID &&
			existing.IsDefault && existing.DetachedAt == nil {
			hasDefault = true
			break
		}
	}

	m.next++
	id := "vlx_pm_mem_" + in.StripePaymentMethodID
	now := time.Now().UTC()
	row := PaymentMethod{
		ID:                    id,
		TenantID:              tenantID,
		CustomerID:            in.CustomerID,
		StripePaymentMethodID: in.StripePaymentMethodID,
		Type:                  orDefault(in.Type, "card"),
		CardBrand:             in.CardBrand,
		CardLast4:             in.CardLast4,
		CardExpMonth:          in.CardExpMonth,
		CardExpYear:           in.CardExpYear,
		IsDefault:             !hasDefault,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	m.rows[id] = row
	return row, nil
}

func (m *memStore) SetDefault(_ context.Context, tenantID, customerID, pmID string) (PaymentMethod, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	target, ok := m.rows[pmID]
	if !ok || target.TenantID != tenantID || target.CustomerID != customerID || target.DetachedAt != nil {
		return PaymentMethod{}, errs.ErrNotFound
	}
	for id, row := range m.rows {
		if row.TenantID == tenantID && row.CustomerID == customerID && row.IsDefault && id != pmID {
			row.IsDefault = false
			row.UpdatedAt = time.Now().UTC()
			m.rows[id] = row
		}
	}
	target.IsDefault = true
	target.UpdatedAt = time.Now().UTC()
	m.rows[pmID] = target
	return target, nil
}

func (m *memStore) Detach(_ context.Context, tenantID, customerID, pmID string) (PaymentMethod, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[pmID]
	if !ok || row.TenantID != tenantID || row.CustomerID != customerID {
		return PaymentMethod{}, errs.ErrNotFound
	}
	if row.DetachedAt == nil {
		now := time.Now().UTC()
		row.DetachedAt = &now
	}
	row.IsDefault = false
	row.UpdatedAt = time.Now().UTC()
	m.rows[pmID] = row
	return row, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ---------------------------------------------------------------------------
// fakeStripe — only what Service touches.
// ---------------------------------------------------------------------------
type fakeStripe struct {
	setupCalls int
	detachCalls int
	detachErr   error
}

func (f *fakeStripe) CreateSetupIntent(_ context.Context, _ string, _ map[string]string) (string, string, error) {
	f.setupCalls++
	return "seti_secret_fake", "seti_fake", nil
}
func (f *fakeStripe) EnsureStripeCustomer(_ context.Context, _, _ string) (string, error) {
	return "cus_stripe_fake", nil
}
func (f *fakeStripe) DetachPaymentMethod(_ context.Context, _ string) error {
	f.detachCalls++
	return f.detachErr
}
func (f *fakeStripe) FetchPaymentMethodCard(_ context.Context, _ string) (string, string, int, int, error) {
	return "visa", "4242", 12, 2030, nil
}

// ---------------------------------------------------------------------------
// fakeSummary — captures the CustomerPaymentSetup row so tests can verify
// that Service keeps the 1:1 denorm in sync.
// ---------------------------------------------------------------------------
type fakeSummary struct {
	mu    sync.Mutex
	setup domain.CustomerPaymentSetup
}

func (f *fakeSummary) UpsertPaymentSetup(_ context.Context, _ string, ps domain.CustomerPaymentSetup) (domain.CustomerPaymentSetup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setup = ps
	return ps, nil
}
func (f *fakeSummary) GetPaymentSetup(_ context.Context, _, _ string) (domain.CustomerPaymentSetup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setup, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestAttach_FirstBecomesDefault — the first PM attached to a customer
// is auto-promoted to default, and the summary row reflects that.
func TestAttach_FirstBecomesDefault(t *testing.T) {
	store := newMemStore()
	stripe := &fakeStripe{}
	summary := &fakeSummary{}
	svc := NewService(store, stripe, summary)

	pm, err := svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_y", "pm_stripe_1")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !pm.IsDefault {
		t.Fatalf("first PM should be default")
	}
	if summary.setup.StripePaymentMethodID != "pm_stripe_1" || summary.setup.CardLast4 != "4242" {
		t.Fatalf("summary not synced: %+v", summary.setup)
	}
}

// TestAttach_SecondDoesNotStealDefault — adding a second card while one
// is already default must keep the existing default. The customer must
// explicitly call SetDefault to switch.
func TestAttach_SecondDoesNotStealDefault(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, &fakeStripe{}, &fakeSummary{})

	_, _ = svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_y", "pm_1")
	pm2, err := svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_y", "pm_2")
	if err != nil {
		t.Fatalf("attach 2: %v", err)
	}
	if pm2.IsDefault {
		t.Fatalf("second PM must not auto-default")
	}
}

// TestSetDefault_AtomicSwap — flipping default clears the old one and
// the summary row points at the new PM.
func TestSetDefault_AtomicSwap(t *testing.T) {
	store := newMemStore()
	summary := &fakeSummary{}
	svc := NewService(store, &fakeStripe{}, summary)

	_, _ = svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_y", "pm_1")
	pm2, _ := svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_y", "pm_2")

	promoted, err := svc.SetDefault(context.Background(), "tnt_x", "cus_y", pm2.ID)
	if err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	if !promoted.IsDefault {
		t.Fatalf("promoted PM must be default")
	}

	// Confirm only one row has is_default = true across the list.
	list, _ := svc.List(context.Background(), "tnt_x", "cus_y")
	defaults := 0
	for _, pm := range list {
		if pm.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one default, got %d", defaults)
	}
	if summary.setup.StripePaymentMethodID != "pm_2" {
		t.Fatalf("summary not updated on SetDefault: %+v", summary.setup)
	}
}

// TestDetach_PromotesReplacement — detaching the default promotes the
// next-most-recent PM and updates the summary accordingly.
func TestDetach_PromotesReplacement(t *testing.T) {
	store := newMemStore()
	summary := &fakeSummary{}
	svc := NewService(store, &fakeStripe{}, summary)

	pm1, _ := svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_y", "pm_1")
	_, _ = svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_y", "pm_2")

	if _, err := svc.Detach(context.Background(), "tnt_x", "cus_y", pm1.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	list, _ := svc.List(context.Background(), "tnt_x", "cus_y")
	if len(list) != 1 || !list[0].IsDefault {
		t.Fatalf("expected 1 active PM flagged default, got %+v", list)
	}
	if summary.setup.StripePaymentMethodID != "pm_2" {
		t.Fatalf("summary should point at pm_2 after promotion: %+v", summary.setup)
	}
}

// TestDetach_LastPMClearsSummary — if the detached PM was the only one,
// the summary row goes back to "missing" so billing won't try to charge
// a ghost card.
func TestDetach_LastPMClearsSummary(t *testing.T) {
	store := newMemStore()
	summary := &fakeSummary{}
	svc := NewService(store, &fakeStripe{}, summary)

	pm, _ := svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_y", "pm_1")
	if _, err := svc.Detach(context.Background(), "tnt_x", "cus_y", pm.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if summary.setup.SetupStatus != domain.PaymentSetupMissing {
		t.Fatalf("expected Missing status, got %q", summary.setup.SetupStatus)
	}
	if summary.setup.StripePaymentMethodID != "" {
		t.Fatalf("expected cleared stripe_pm on last detach: %+v", summary.setup)
	}
}

// TestDetach_StripeAlreadyGone — Stripe returning 404 on Detach (because
// the PM was already removed upstream) must still mark the local row
// detached; we don't leave orphans just because Stripe is ahead of us.
func TestDetach_StripeAlreadyGone(t *testing.T) {
	store := newMemStore()
	stripe := &fakeStripe{detachErr: errs.ErrNotFound}
	svc := NewService(store, stripe, &fakeSummary{})

	pm, _ := svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_y", "pm_1")
	if _, err := svc.Detach(context.Background(), "tnt_x", "cus_y", pm.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	got, _ := store.Get(context.Background(), "tnt_x", pm.ID)
	if got.DetachedAt == nil {
		t.Fatalf("local row should be detached even when Stripe 404s")
	}
}

// TestDetach_CrossCustomer — detaching a PM that belongs to another
// customer within the same tenant must 404, not silently succeed.
func TestDetach_CrossCustomer(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, &fakeStripe{}, &fakeSummary{})

	pm, _ := svc.AttachFromSetupIntent(context.Background(), "tnt_x", "cus_a", "pm_1")
	if _, err := svc.Detach(context.Background(), "tnt_x", "cus_b", pm.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
