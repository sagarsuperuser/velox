package subscription

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type memStore struct {
	subs map[string]domain.Subscription
}

func newMemStore() *memStore {
	return &memStore{subs: make(map[string]domain.Subscription)}
}

func (m *memStore) Create(_ context.Context, tenantID string, s domain.Subscription) (domain.Subscription, error) {
	for _, existing := range m.subs {
		if existing.TenantID == tenantID && existing.Code == s.Code {
			return domain.Subscription{}, fmt.Errorf("%w: subscription code %q", errs.ErrAlreadyExists, s.Code)
		}
	}
	s.ID = fmt.Sprintf("vlx_sub_%d", len(m.subs)+1)
	s.TenantID = tenantID
	now := time.Now().UTC()
	s.CreatedAt = now
	s.UpdatedAt = now
	m.subs[s.ID] = s
	return s, nil
}

func (m *memStore) Get(_ context.Context, tenantID, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok || s.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	return s, nil
}

func (m *memStore) List(_ context.Context, filter ListFilter) ([]domain.Subscription, int, error) {
	var result []domain.Subscription
	for _, s := range m.subs {
		if s.TenantID != filter.TenantID {
			continue
		}
		if filter.Status != "" && string(s.Status) != filter.Status {
			continue
		}
		result = append(result, s)
	}
	return result, len(result), nil
}

func (m *memStore) Update(_ context.Context, tenantID string, s domain.Subscription) (domain.Subscription, error) {
	_, ok := m.subs[s.ID]
	if !ok {
		return domain.Subscription{}, errs.ErrNotFound
	}
	m.subs[s.ID] = s
	return s, nil
}

func (m *memStore) GetDueBilling(_ context.Context, _ time.Time, _ int) ([]domain.Subscription, error) {
	return nil, nil
}

func (m *memStore) UpdateBillingCycle(_ context.Context, _, _ string, _, _, _ time.Time) error {
	return nil
}

func (m *memStore) PauseAtomic(_ context.Context, tenantID, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok || s.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if s.Status != domain.SubscriptionActive {
		return domain.Subscription{}, fmt.Errorf("can only pause active subscriptions, current status: %s", s.Status)
	}
	s.Status = domain.SubscriptionPaused
	s.UpdatedAt = time.Now().UTC()
	m.subs[id] = s
	return s, nil
}

func (m *memStore) ResumeAtomic(_ context.Context, tenantID, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok || s.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if s.Status != domain.SubscriptionPaused {
		return domain.Subscription{}, fmt.Errorf("can only resume paused subscriptions, current status: %s", s.Status)
	}
	s.Status = domain.SubscriptionActive
	s.UpdatedAt = time.Now().UTC()
	m.subs[id] = s
	return s, nil
}

func (m *memStore) CancelAtomic(_ context.Context, tenantID, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok || s.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if s.Status != domain.SubscriptionActive && s.Status != domain.SubscriptionPaused {
		return domain.Subscription{}, fmt.Errorf("can only cancel active or paused subscriptions, current status: %s", s.Status)
	}
	now := time.Now().UTC()
	s.Status = domain.SubscriptionCanceled
	s.CanceledAt = &now
	s.UpdatedAt = now
	m.subs[id] = s
	return s, nil
}

func (m *memStore) SetPendingPlan(_ context.Context, tenantID, id, pendingPlanID string, effectiveAt time.Time) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok || s.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	s.PendingPlanID = pendingPlanID
	s.PendingPlanEffectiveAt = &effectiveAt
	s.UpdatedAt = time.Now().UTC()
	m.subs[id] = s
	return s, nil
}

func (m *memStore) ClearPendingPlan(_ context.Context, tenantID, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok || s.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	s.PendingPlanID = ""
	s.PendingPlanEffectiveAt = nil
	s.UpdatedAt = time.Now().UTC()
	m.subs[id] = s
	return s, nil
}

func (m *memStore) ApplyPendingPlanAtomic(_ context.Context, tenantID, id string, now time.Time) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok || s.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if s.PendingPlanID == "" || s.PendingPlanEffectiveAt == nil || s.PendingPlanEffectiveAt.After(now) {
		return domain.Subscription{}, errs.ErrNotFound
	}
	s.PreviousPlanID = s.PlanID
	s.PlanID = s.PendingPlanID
	s.PlanChangedAt = &now
	s.PendingPlanID = ""
	s.PendingPlanEffectiveAt = nil
	s.UpdatedAt = now
	m.subs[id] = s
	return s, nil
}

func TestCreate(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	t.Run("draft by default", func(t *testing.T) {
		sub, err := svc.Create(ctx, "t1", CreateInput{
			Code: "sub-001", DisplayName: "Pro Monthly",
			CustomerID: "cus_1", PlanID: "pln_1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sub.Status != domain.SubscriptionDraft {
			t.Errorf("got status %q, want draft", sub.Status)
		}
		if sub.BillingTime != domain.BillingTimeCalendar {
			t.Errorf("got billing_time %q, want calendar", sub.BillingTime)
		}
	})

	t.Run("start_now activates immediately", func(t *testing.T) {
		sub, err := svc.Create(ctx, "t1", CreateInput{
			Code: "sub-002", DisplayName: "Active",
			CustomerID: "cus_1", PlanID: "pln_1", StartNow: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sub.Status != domain.SubscriptionActive {
			t.Errorf("got status %q, want active", sub.Status)
		}
		if sub.StartedAt == nil {
			t.Error("started_at should be set")
		}
	})

	t.Run("trial_days sets trial window", func(t *testing.T) {
		sub, err := svc.Create(ctx, "t1", CreateInput{
			Code: "sub-003", DisplayName: "Trial",
			CustomerID: "cus_1", PlanID: "pln_1", TrialDays: 14,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sub.TrialStartAt == nil || sub.TrialEndAt == nil {
			t.Fatal("trial dates should be set")
		}
		diff := sub.TrialEndAt.Sub(*sub.TrialStartAt)
		if diff.Hours() < 13*24 || diff.Hours() > 15*24 {
			t.Errorf("trial duration %v, expected ~14 days", diff)
		}
	})

	t.Run("validation errors", func(t *testing.T) {
		cases := []CreateInput{
			{DisplayName: "x", CustomerID: "c", PlanID: "p"},                                   // missing code
			{Code: "x", CustomerID: "c", PlanID: "p"},                                          // missing display_name
			{Code: "x", DisplayName: "x", PlanID: "p"},                                         // missing customer_id
			{Code: "x", DisplayName: "x", CustomerID: "c"},                                     // missing plan_id
			{Code: "x", DisplayName: "x", CustomerID: "c", PlanID: "p", BillingTime: "weekly"}, // invalid billing_time
		}
		for _, input := range cases {
			_, err := svc.Create(ctx, "t1", input)
			if err == nil {
				t.Errorf("expected error for input %+v", input)
			}
		}
	})
}

func TestActivateAndCancel(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	sub, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "sub-act", DisplayName: "Test", CustomerID: "c", PlanID: "p",
	})

	t.Run("activate draft", func(t *testing.T) {
		activated, err := svc.Activate(ctx, "t1", sub.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if activated.Status != domain.SubscriptionActive {
			t.Errorf("got status %q, want active", activated.Status)
		}
	})

	t.Run("cannot activate again", func(t *testing.T) {
		_, err := svc.Activate(ctx, "t1", sub.ID)
		if err == nil {
			t.Fatal("expected error activating non-draft")
		}
	})

	t.Run("cancel active", func(t *testing.T) {
		canceled, err := svc.Cancel(ctx, "t1", sub.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if canceled.Status != domain.SubscriptionCanceled {
			t.Errorf("got status %q, want canceled", canceled.Status)
		}
	})

	t.Run("cannot cancel canceled", func(t *testing.T) {
		_, err := svc.Cancel(ctx, "t1", sub.ID)
		if err == nil {
			t.Fatal("expected error canceling already canceled")
		}
	})
}

func TestChangePlan(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	sub, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "sub-change", DisplayName: "Test", CustomerID: "c", PlanID: "plan_old", StartNow: true,
	})

	// Set billing period for proration calculation
	store := svc.store.(*memStore)
	s := store.subs[sub.ID]
	start := time.Now().UTC().AddDate(0, 0, -15)
	end := time.Now().UTC().AddDate(0, 0, 15)
	s.CurrentBillingPeriodStart = &start
	s.CurrentBillingPeriodEnd = &end
	store.subs[sub.ID] = s

	t.Run("immediate change with proration", func(t *testing.T) {
		result, err := svc.ChangePlan(ctx, "t1", sub.ID, ChangePlanInput{
			NewPlanID: "plan_new",
			Immediate: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Subscription.PlanID != "plan_new" {
			t.Errorf("plan_id: got %q, want plan_new", result.Subscription.PlanID)
		}
		if result.Subscription.PreviousPlanID != "plan_old" {
			t.Errorf("previous_plan_id: got %q, want plan_old", result.Subscription.PreviousPlanID)
		}
		if result.ProrationFactor <= 0 || result.ProrationFactor > 1 {
			t.Errorf("proration_factor: got %f, should be between 0 and 1", result.ProrationFactor)
		}
		if result.Subscription.PlanChangedAt == nil {
			t.Error("plan_changed_at should be set")
		}
	})

	t.Run("same plan rejected", func(t *testing.T) {
		_, err := svc.ChangePlan(ctx, "t1", sub.ID, ChangePlanInput{NewPlanID: "plan_new"})
		if err == nil {
			t.Fatal("expected error for same plan")
		}
	})

	t.Run("missing new_plan_id", func(t *testing.T) {
		_, err := svc.ChangePlan(ctx, "t1", sub.ID, ChangePlanInput{})
		if err == nil {
			t.Fatal("expected error for missing plan")
		}
	})
}

// A scheduled plan change (immediate=false) must not mutate plan_id. It
// records pending fields and reports an effective_at that honours the current
// cycle boundary. Prior behaviour swapped plan_id immediately, making the
// response's effective_at a lie — this test locks in the fix.
func TestChangePlan_Scheduled(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	sub, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "sub-sched", DisplayName: "Test", CustomerID: "c", PlanID: "plan_old", StartNow: true,
	})

	store := svc.store.(*memStore)
	s := store.subs[sub.ID]
	start := time.Now().UTC().AddDate(0, 0, -5)
	end := time.Now().UTC().AddDate(0, 0, 25)
	s.CurrentBillingPeriodStart = &start
	s.CurrentBillingPeriodEnd = &end
	store.subs[sub.ID] = s

	t.Run("records pending, leaves plan_id untouched", func(t *testing.T) {
		result, err := svc.ChangePlan(ctx, "t1", sub.ID, ChangePlanInput{
			NewPlanID: "plan_new",
			Immediate: false,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Subscription.PlanID != "plan_old" {
			t.Errorf("plan_id must not change on scheduled: got %q, want plan_old", result.Subscription.PlanID)
		}
		if result.Subscription.PendingPlanID != "plan_new" {
			t.Errorf("pending_plan_id: got %q, want plan_new", result.Subscription.PendingPlanID)
		}
		if result.Subscription.PendingPlanEffectiveAt == nil {
			t.Fatal("pending_plan_effective_at must be set")
		}
		if !result.Subscription.PendingPlanEffectiveAt.Equal(end) {
			t.Errorf("pending_plan_effective_at: got %v, want period end %v", *result.Subscription.PendingPlanEffectiveAt, end)
		}
		if !result.EffectiveAt.Equal(end) {
			t.Errorf("response effective_at: got %v, want %v", result.EffectiveAt, end)
		}
	})

	t.Run("cancel pending restores state", func(t *testing.T) {
		updated, err := svc.CancelPendingPlanChange(ctx, "t1", sub.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if updated.PendingPlanID != "" {
			t.Errorf("pending_plan_id should be cleared: got %q", updated.PendingPlanID)
		}
		if updated.PendingPlanEffectiveAt != nil {
			t.Errorf("pending_plan_effective_at should be nil: got %v", updated.PendingPlanEffectiveAt)
		}
		if updated.PlanID != "plan_old" {
			t.Errorf("plan_id should remain unchanged: got %q", updated.PlanID)
		}
	})

	t.Run("cancel with no pending is no-op", func(t *testing.T) {
		updated, err := svc.CancelPendingPlanChange(ctx, "t1", sub.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if updated.PendingPlanID != "" {
			t.Errorf("expected no pending: got %q", updated.PendingPlanID)
		}
	})

	t.Run("immediate supersedes pending", func(t *testing.T) {
		// Re-schedule a change, then issue an immediate one — pending must be cleared.
		_, err := svc.ChangePlan(ctx, "t1", sub.ID, ChangePlanInput{NewPlanID: "plan_scheduled", Immediate: false})
		if err != nil {
			t.Fatalf("schedule: %v", err)
		}
		result, err := svc.ChangePlan(ctx, "t1", sub.ID, ChangePlanInput{NewPlanID: "plan_immediate", Immediate: true})
		if err != nil {
			t.Fatalf("immediate: %v", err)
		}
		if result.Subscription.PlanID != "plan_immediate" {
			t.Errorf("plan_id: got %q, want plan_immediate", result.Subscription.PlanID)
		}
		if result.Subscription.PendingPlanID != "" {
			t.Errorf("pending_plan_id should be cleared: got %q", result.Subscription.PendingPlanID)
		}
	})
}

func TestPauseAndResume(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	sub, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "sub-pause", DisplayName: "Test", CustomerID: "c", PlanID: "p", StartNow: true,
	})

	t.Run("pause active", func(t *testing.T) {
		paused, err := svc.Pause(ctx, "t1", sub.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if paused.Status != domain.SubscriptionPaused {
			t.Errorf("status: got %q, want paused", paused.Status)
		}
	})

	t.Run("resume paused", func(t *testing.T) {
		resumed, err := svc.Resume(ctx, "t1", sub.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resumed.Status != domain.SubscriptionActive {
			t.Errorf("status: got %q, want active", resumed.Status)
		}
	})

	t.Run("cannot pause non-active", func(t *testing.T) {
		_, _ = svc.Cancel(ctx, "t1", sub.ID)
		_, err := svc.Pause(ctx, "t1", sub.ID)
		if err == nil {
			t.Fatal("expected error pausing canceled sub")
		}
	})
}
