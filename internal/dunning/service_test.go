package dunning

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type memStore struct {
	policy            *domain.DunningPolicy
	runs              map[string]domain.InvoiceDunningRun
	events            []domain.InvoiceDunningEvent
	customerOverrides map[string]domain.CustomerDunningOverride
}

func newMemStore() *memStore {
	return &memStore{
		policy: &domain.DunningPolicy{
			ID: "dpol_1", TenantID: "t1", Name: "Default",
			Enabled: true, MaxRetryAttempts: 3, GracePeriodDays: 3,
			RetrySchedule: []string{"72h", "120h"},
			FinalAction:   domain.DunningActionManualReview,
		},
		runs:              make(map[string]domain.InvoiceDunningRun),
		customerOverrides: make(map[string]domain.CustomerDunningOverride),
	}
}

func (m *memStore) GetPolicy(_ context.Context, _ string) (domain.DunningPolicy, error) {
	if m.policy == nil {
		return domain.DunningPolicy{}, errs.ErrNotFound
	}
	return *m.policy, nil
}

func (m *memStore) UpsertPolicy(_ context.Context, _ string, p domain.DunningPolicy) (domain.DunningPolicy, error) {
	p.ID = "dpol_1"
	m.policy = &p
	return p, nil
}

func (m *memStore) UpsertPolicyTx(ctx context.Context, _ *sql.Tx, tenantID string, p domain.DunningPolicy) (domain.DunningPolicy, error) {
	return m.UpsertPolicy(ctx, tenantID, p)
}

func (m *memStore) CreateRun(_ context.Context, tenantID string, run domain.InvoiceDunningRun) (domain.InvoiceDunningRun, error) {
	run.ID = fmt.Sprintf("drun_%d", len(m.runs)+1)
	run.TenantID = tenantID
	now := time.Now().UTC()
	run.CreatedAt = now
	run.UpdatedAt = now
	m.runs[run.ID] = run
	return run, nil
}

func (m *memStore) GetRun(_ context.Context, _, id string) (domain.InvoiceDunningRun, error) {
	r, ok := m.runs[id]
	if !ok {
		return domain.InvoiceDunningRun{}, errs.ErrNotFound
	}
	return r, nil
}

func (m *memStore) GetActiveRunByInvoice(_ context.Context, _, invoiceID string) (domain.InvoiceDunningRun, error) {
	for _, r := range m.runs {
		if r.InvoiceID == invoiceID && r.State != domain.DunningResolved && r.State != domain.DunningEscalated {
			return r, nil
		}
	}
	return domain.InvoiceDunningRun{}, errs.ErrNotFound
}

func (m *memStore) ListRuns(_ context.Context, filter RunListFilter) ([]domain.InvoiceDunningRun, int, error) {
	var result []domain.InvoiceDunningRun
	for _, r := range m.runs {
		if filter.State != "" && string(r.State) != filter.State {
			continue
		}
		result = append(result, r)
	}
	return result, len(result), nil
}

func (m *memStore) UpdateRun(_ context.Context, _ string, run domain.InvoiceDunningRun) (domain.InvoiceDunningRun, error) {
	m.runs[run.ID] = run
	return run, nil
}

func (m *memStore) ListDueRuns(_ context.Context, _ string, before time.Time, limit int) ([]domain.InvoiceDunningRun, error) {
	var result []domain.InvoiceDunningRun
	for _, r := range m.runs {
		if r.NextActionAt != nil && !r.NextActionAt.After(before) && !r.Paused &&
			r.State != domain.DunningResolved && r.State != domain.DunningEscalated {
			result = append(result, r)
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *memStore) CreateEvent(_ context.Context, _ string, event domain.InvoiceDunningEvent) (domain.InvoiceDunningEvent, error) {
	event.ID = fmt.Sprintf("devt_%d", len(m.events)+1)
	m.events = append(m.events, event)
	return event, nil
}

func (m *memStore) ListEvents(_ context.Context, _, runID string) ([]domain.InvoiceDunningEvent, error) {
	var result []domain.InvoiceDunningEvent
	for _, e := range m.events {
		if e.RunID == runID {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *memStore) GetCustomerOverride(_ context.Context, _, customerID string) (domain.CustomerDunningOverride, error) {
	o, ok := m.customerOverrides[customerID]
	if !ok {
		return domain.CustomerDunningOverride{}, errs.ErrNotFound
	}
	return o, nil
}

func (m *memStore) UpsertCustomerOverride(_ context.Context, tenantID string, o domain.CustomerDunningOverride) (domain.CustomerDunningOverride, error) {
	o.TenantID = tenantID
	m.customerOverrides[o.CustomerID] = o
	return o, nil
}

func (m *memStore) DeleteCustomerOverride(_ context.Context, _, customerID string) error {
	if _, ok := m.customerOverrides[customerID]; !ok {
		return errs.ErrNotFound
	}
	delete(m.customerOverrides, customerID)
	return nil
}

type noopRetrier struct{}

func (n *noopRetrier) RetryPayment(_ context.Context, _, _, _ string) error { return nil }

type failingRetrier struct{}

func (f *failingRetrier) RetryPayment(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("payment declined")
}

func TestStartDunning(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, &noopRetrier{}, nil)
	ctx := context.Background()

	t.Run("creates run", func(t *testing.T) {
		run, err := svc.StartDunning(ctx, "t1", "inv_1", "cus_1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if run.InvoiceID != "inv_1" {
			t.Errorf("invoice_id: got %q, want inv_1", run.InvoiceID)
		}
		if run.State != domain.DunningActive {
			t.Errorf("state: got %q, want scheduled", run.State)
		}
		if run.NextActionAt == nil {
			t.Error("next_action_at should be set from retry schedule")
		}
		if run.PolicyID != "dpol_1" {
			t.Errorf("policy_id: got %q, want dpol_1", run.PolicyID)
		}
	})

	t.Run("idempotent — returns existing", func(t *testing.T) {
		run2, err := svc.StartDunning(ctx, "t1", "inv_1", "cus_1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should return the same run, not create a new one
		if len(store.runs) != 1 {
			t.Errorf("expected 1 run, got %d", len(store.runs))
		}
		if run2.InvoiceID != "inv_1" {
			t.Errorf("should return existing run")
		}
	})

	t.Run("records start event", func(t *testing.T) {
		found := false
		for _, e := range store.events {
			if e.EventType == domain.DunningEventStarted {
				found = true
			}
		}
		if !found {
			t.Error("should record dunning_started event")
		}
	})
}

func TestStartDunning_DisabledPolicy(t *testing.T) {
	store := newMemStore()
	store.policy.Enabled = false
	svc := NewService(store, &noopRetrier{}, nil)

	_, err := svc.StartDunning(context.Background(), "t1", "inv_2", "cus_1")
	if err == nil {
		t.Fatal("expected error when dunning is disabled")
	}
}

func TestProcessDueRuns(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, &failingRetrier{}, nil) // Use failing retrier
	ctx := context.Background()

	// Start a run, then make it due
	run, _ := svc.StartDunning(ctx, "t1", "inv_1", "cus_1")
	past := time.Now().UTC().Add(-1 * time.Hour)
	run.NextActionAt = &past
	store.runs[run.ID] = run

	processed, errs := svc.ProcessDueRuns(ctx, "t1", 20)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if processed != 1 {
		t.Errorf("processed: got %d, want 1", processed)
	}

	// Verify run was updated — retry failed so state stays scheduled
	updated := store.runs[run.ID]
	if updated.AttemptCount != 1 {
		t.Errorf("attempt_count: got %d, want 1", updated.AttemptCount)
	}
	if updated.State != domain.DunningActive {
		t.Errorf("state: got %q, want scheduled", updated.State)
	}
}

func TestProcessDueRuns_MaxRetriesExhausted(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, &noopRetrier{}, nil)
	ctx := context.Background()

	run, _ := svc.StartDunning(ctx, "t1", "inv_1", "cus_1")

	// Simulate max retries reached
	run.AttemptCount = 3 // equals MaxRetryAttempts
	past := time.Now().UTC().Add(-1 * time.Hour)
	run.NextActionAt = &past
	store.runs[run.ID] = run

	svc.ProcessDueRuns(ctx, "t1", 20)

	updated := store.runs[run.ID]
	if updated.State != domain.DunningEscalated {
		t.Errorf("state: got %q, want escalated (manual_review policy)", updated.State)
	}
	if updated.Resolution != domain.ResolutionRetriesExhausted {
		t.Errorf("resolution: got %q, want retries_exhausted", updated.Resolution)
	}
	if updated.ResolvedAt == nil {
		t.Error("resolved_at should be set")
	}
}

func TestResolveRun(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, &noopRetrier{}, nil)
	ctx := context.Background()

	run, _ := svc.StartDunning(ctx, "t1", "inv_1", "cus_1")

	resolved, err := svc.ResolveRun(ctx, "t1", run.ID, domain.ResolutionPaymentRecovered)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.State != domain.DunningResolved {
		t.Errorf("state: got %q, want resolved", resolved.State)
	}
	if resolved.Resolution != domain.ResolutionPaymentRecovered {
		t.Errorf("resolution: got %q, want payment_succeeded", resolved.Resolution)
	}
	if resolved.ResolvedAt == nil {
		t.Error("resolved_at should be set")
	}
}

func TestUpsertPolicy(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, &noopRetrier{}, nil)
	ctx := context.Background()

	policy, err := svc.UpsertPolicy(ctx, "t1", domain.DunningPolicy{
		Name:    "Custom",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if policy.MaxRetryAttempts != 3 {
		t.Errorf("default max_retry: got %d, want 3", policy.MaxRetryAttempts)
	}
	if policy.GracePeriodDays != 3 {
		t.Errorf("default grace_period: got %d, want 3", policy.GracePeriodDays)
	}
	if policy.FinalAction != domain.DunningActionManualReview {
		t.Errorf("default final_action: got %q, want manual_review", policy.FinalAction)
	}
}
