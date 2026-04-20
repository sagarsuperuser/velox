package testclock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// --- Tests ---

func TestCreate_RequiresFrozenTime(t *testing.T) {
	s := NewService(newMockStore())
	_, err := s.Create(context.Background(), "t1", CreateInput{Name: "jan 1"})
	if err == nil {
		t.Fatal("expected error for zero frozen_time")
	}
	if !errors.Is(err, errs.ErrValidation) {
		t.Errorf("want validation error, got %T: %v", err, err)
	}
}

func TestCreate_RejectsLongName(t *testing.T) {
	s := NewService(newMockStore())
	longName := make([]byte, 201)
	for i := range longName {
		longName[i] = 'x'
	}
	_, err := s.Create(context.Background(), "t1", CreateInput{
		Name:       string(longName),
		FrozenTime: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error for name > 200 chars")
	}
}

func TestCreate_Succeeds(t *testing.T) {
	store := newMockStore()
	s := NewService(store)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	clk, err := s.Create(context.Background(), "t1", CreateInput{
		Name:       "fy26",
		FrozenTime: now,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clk.Name != "fy26" {
		t.Errorf("name: got %q, want fy26", clk.Name)
	}
	if !clk.FrozenTime.Equal(now) {
		t.Errorf("frozen_time: got %v, want %v", clk.FrozenTime, now)
	}
	if clk.Status != domain.TestClockStatusReady {
		t.Errorf("status: got %q, want ready", clk.Status)
	}
}

func TestAdvance_RejectsNonReadyClock(t *testing.T) {
	store := newMockStore()
	store.clocks["c1"] = domain.TestClock{
		ID:         "c1",
		TenantID:   "t1",
		Status:     domain.TestClockStatusAdvancing,
		FrozenTime: time.Now(),
	}
	s := NewService(store)

	_, err := s.Advance(context.Background(), "t1", "c1", AdvanceInput{
		FrozenTime: time.Now().Add(24 * time.Hour),
	})
	if err == nil {
		t.Fatal("expected error advancing non-ready clock")
	}
}

func TestAdvance_RejectsBackwardTime(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newMockStore()
	store.clocks["c1"] = domain.TestClock{
		ID:         "c1",
		TenantID:   "t1",
		Status:     domain.TestClockStatusReady,
		FrozenTime: now,
	}
	s := NewService(store)

	_, err := s.Advance(context.Background(), "t1", "c1", AdvanceInput{
		FrozenTime: now.Add(-time.Hour),
	})
	if err == nil {
		t.Fatal("advancing backward must be rejected")
	}
}

func TestAdvance_RunsBillingUntilQuiet(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newTime := start.Add(60 * 24 * time.Hour) // 60 days forward → ~2 monthly cycles

	store := newMockStore()
	store.clocks["c1"] = domain.TestClock{
		ID: "c1", TenantID: "t1", Status: domain.TestClockStatusReady, FrozenTime: start,
	}
	// One sub attached; the runner advances its next_billing_at by 30 days
	// each call, mimicking a monthly invoice being emitted.
	sub := domain.Subscription{
		ID:          "s1",
		TenantID:    "t1",
		TestClockID: "c1",
		Status:      domain.SubscriptionActive,
	}
	nextBilling := start.Add(1 * time.Hour)
	sub.NextBillingAt = &nextBilling
	store.subsOnClock["c1"] = []domain.Subscription{sub}

	runner := &stubRunner{
		store:    store,
		clockID:  "c1",
		subID:    "s1",
		cycleDur: 30 * 24 * time.Hour,
	}
	s := NewService(store)
	s.SetBillingRunner(runner)

	clk, err := s.Advance(context.Background(), "t1", "c1", AdvanceInput{FrozenTime: newTime})
	if err != nil {
		t.Fatalf("advance failed: %v", err)
	}
	if clk.Status != domain.TestClockStatusReady {
		t.Errorf("final status: got %q, want ready", clk.Status)
	}
	if !clk.FrozenTime.Equal(newTime) {
		t.Errorf("frozen_time: got %v, want %v", clk.FrozenTime, newTime)
	}
	// Two cycles should have been billed (day 1 + day 31); after the second
	// cycle next_billing_at = day 61, which is after frozen_time (day 60)
	// so the loop exits.
	if runner.calls != 2 {
		t.Errorf("billing runs: got %d, want 2", runner.calls)
	}
}

func TestAdvance_BillingFailureMarksClockFailed(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := newMockStore()
	store.clocks["c1"] = domain.TestClock{
		ID: "c1", TenantID: "t1", Status: domain.TestClockStatusReady, FrozenTime: start,
	}
	sub := domain.Subscription{
		ID: "s1", TenantID: "t1", TestClockID: "c1", Status: domain.SubscriptionActive,
	}
	nextBilling := start.Add(1 * time.Hour)
	sub.NextBillingAt = &nextBilling
	store.subsOnClock["c1"] = []domain.Subscription{sub}

	runner := &stubRunner{
		store:   store,
		clockID: "c1",
		subID:   "s1",
		err:     errors.New("boom"),
	}
	s := NewService(store)
	s.SetBillingRunner(runner)

	_, err := s.Advance(context.Background(), "t1", "c1", AdvanceInput{
		FrozenTime: start.Add(30 * 24 * time.Hour),
	})
	if err == nil {
		t.Fatal("expected error on billing failure")
	}
	if store.clocks["c1"].Status != domain.TestClockStatusInternalFailed {
		t.Errorf("clock status after failure: got %q, want internal_failure",
			store.clocks["c1"].Status)
	}
}

// --- Mocks ---

type mockStore struct {
	clocks      map[string]domain.TestClock
	subsOnClock map[string][]domain.Subscription
}

func newMockStore() *mockStore {
	return &mockStore{
		clocks:      make(map[string]domain.TestClock),
		subsOnClock: make(map[string][]domain.Subscription),
	}
}

func (m *mockStore) Create(_ context.Context, tenantID string, clk domain.TestClock) (domain.TestClock, error) {
	clk.ID = "c_" + clk.Name
	clk.TenantID = tenantID
	clk.Status = domain.TestClockStatusReady
	clk.CreatedAt = time.Now()
	clk.UpdatedAt = clk.CreatedAt
	m.clocks[clk.ID] = clk
	return clk, nil
}

func (m *mockStore) Get(_ context.Context, _, id string) (domain.TestClock, error) {
	c, ok := m.clocks[id]
	if !ok {
		return domain.TestClock{}, errs.ErrNotFound
	}
	return c, nil
}

func (m *mockStore) List(_ context.Context, _ string) ([]domain.TestClock, error) {
	out := make([]domain.TestClock, 0, len(m.clocks))
	for _, c := range m.clocks {
		out = append(out, c)
	}
	return out, nil
}

func (m *mockStore) Delete(_ context.Context, _, id string) error {
	if _, ok := m.clocks[id]; !ok {
		return errs.ErrNotFound
	}
	delete(m.clocks, id)
	return nil
}

func (m *mockStore) MarkAdvancing(_ context.Context, _, id string, newFrozenTime time.Time) (domain.TestClock, error) {
	c, ok := m.clocks[id]
	if !ok {
		return domain.TestClock{}, errs.ErrNotFound
	}
	if c.Status != domain.TestClockStatusReady {
		return domain.TestClock{}, errs.InvalidState("not ready")
	}
	c.Status = domain.TestClockStatusAdvancing
	c.FrozenTime = newFrozenTime
	m.clocks[id] = c
	return c, nil
}

func (m *mockStore) CompleteAdvance(_ context.Context, _, id string) (domain.TestClock, error) {
	c, ok := m.clocks[id]
	if !ok {
		return domain.TestClock{}, errs.ErrNotFound
	}
	if c.Status != domain.TestClockStatusAdvancing {
		return domain.TestClock{}, errs.InvalidState("not advancing")
	}
	c.Status = domain.TestClockStatusReady
	m.clocks[id] = c
	return c, nil
}

func (m *mockStore) MarkFailed(_ context.Context, _, id string) (domain.TestClock, error) {
	c, ok := m.clocks[id]
	if !ok {
		return domain.TestClock{}, errs.ErrNotFound
	}
	c.Status = domain.TestClockStatusInternalFailed
	m.clocks[id] = c
	return c, nil
}

func (m *mockStore) ListSubscriptionsOnClock(_ context.Context, _, clockID string) ([]domain.Subscription, error) {
	return m.subsOnClock[clockID], nil
}

// stubRunner simulates one invoice per billing cycle by bumping the sub's
// next_billing_at forward. The service.runCatchup loop calls RunCycle until
// next_billing_at overshoots frozen_time.
type stubRunner struct {
	store    *mockStore
	clockID  string
	subID    string
	cycleDur time.Duration
	calls    int
	err      error
}

func (r *stubRunner) RunCycle(_ context.Context, _ int) (int, []error) {
	r.calls++
	if r.err != nil {
		return 0, []error{r.err}
	}
	subs := r.store.subsOnClock[r.clockID]
	for i := range subs {
		if subs[i].ID != r.subID || subs[i].NextBillingAt == nil {
			continue
		}
		next := subs[i].NextBillingAt.Add(r.cycleDur)
		subs[i].NextBillingAt = &next
	}
	r.store.subsOnClock[r.clockID] = subs
	return 1, nil
}
