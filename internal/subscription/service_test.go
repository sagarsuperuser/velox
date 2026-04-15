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

func TestCreate(t *testing.T) {
	svc := NewService(newMemStore())
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
			{DisplayName: "x", CustomerID: "c", PlanID: "p"},           // missing code
			{Code: "x", CustomerID: "c", PlanID: "p"},                  // missing display_name
			{Code: "x", DisplayName: "x", PlanID: "p"},                 // missing customer_id
			{Code: "x", DisplayName: "x", CustomerID: "c"},             // missing plan_id
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
	svc := NewService(newMemStore())
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
	svc := NewService(newMemStore())
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

func TestPauseAndResume(t *testing.T) {
	svc := NewService(newMemStore())
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
		svc.Cancel(ctx, "t1", sub.ID)
		_, err := svc.Pause(ctx, "t1", sub.ID)
		if err == nil {
			t.Fatal("expected error pausing canceled sub")
		}
	})
}
