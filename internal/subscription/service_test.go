package subscription

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// memStore is an in-memory Store implementation used across the subscription
// package's unit tests. It does not enforce every invariant the postgres store
// does — it covers just the behaviour the service layer depends on (item CRUD,
// atomic transitions, and pending-item bookkeeping).
type memStore struct {
	subs  map[string]domain.Subscription
	items map[string]domain.SubscriptionItem
}

func newMemStore() *memStore {
	return &memStore{
		subs:  make(map[string]domain.Subscription),
		items: make(map[string]domain.SubscriptionItem),
	}
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

	hydrated := make([]domain.SubscriptionItem, 0, len(s.Items))
	for i, it := range s.Items {
		it.ID = fmt.Sprintf("%s_item_%d", s.ID, i+1)
		it.SubscriptionID = s.ID
		it.TenantID = tenantID
		it.CreatedAt = now
		it.UpdatedAt = now
		m.items[it.ID] = it
		hydrated = append(hydrated, it)
	}
	s.Items = hydrated
	m.subs[s.ID] = s
	return s, nil
}

func (m *memStore) Get(_ context.Context, tenantID, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok || s.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	s.Items = m.hydrateItems(s.ID)
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
		s.Items = m.hydrateItems(s.ID)
		result = append(result, s)
	}
	return result, len(result), nil
}

func (m *memStore) Update(_ context.Context, tenantID string, s domain.Subscription) (domain.Subscription, error) {
	cur, ok := m.subs[s.ID]
	if !ok || cur.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	s.Items = cur.Items
	s.UpdatedAt = time.Now().UTC()
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

func (m *memStore) ListItems(_ context.Context, tenantID, subscriptionID string) ([]domain.SubscriptionItem, error) {
	s, ok := m.subs[subscriptionID]
	if !ok || s.TenantID != tenantID {
		return nil, errs.ErrNotFound
	}
	return m.hydrateItems(subscriptionID), nil
}

func (m *memStore) GetItem(_ context.Context, tenantID, itemID string) (domain.SubscriptionItem, error) {
	it, ok := m.items[itemID]
	if !ok || it.TenantID != tenantID {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	return it, nil
}

func (m *memStore) AddItem(_ context.Context, tenantID string, item domain.SubscriptionItem) (domain.SubscriptionItem, error) {
	for _, existing := range m.items {
		if existing.SubscriptionID == item.SubscriptionID && existing.PlanID == item.PlanID {
			return domain.SubscriptionItem{}, errs.ErrAlreadyExists
		}
	}
	item.ID = fmt.Sprintf("%s_item_%d", item.SubscriptionID, len(m.items)+1)
	item.TenantID = tenantID
	now := time.Now().UTC()
	item.CreatedAt = now
	item.UpdatedAt = now
	m.items[item.ID] = item
	return item, nil
}

func (m *memStore) UpdateItemQuantity(_ context.Context, tenantID, itemID string, quantity int64) (domain.SubscriptionItem, error) {
	it, ok := m.items[itemID]
	if !ok || it.TenantID != tenantID {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	it.Quantity = quantity
	it.UpdatedAt = time.Now().UTC()
	m.items[itemID] = it
	return it, nil
}

func (m *memStore) ApplyItemPlanImmediately(_ context.Context, tenantID, itemID, newPlanID string, changedAt time.Time) (domain.SubscriptionItem, error) {
	it, ok := m.items[itemID]
	if !ok || it.TenantID != tenantID {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	it.PlanID = newPlanID
	it.PlanChangedAt = &changedAt
	it.PendingPlanID = ""
	it.PendingPlanEffectiveAt = nil
	it.UpdatedAt = changedAt
	m.items[itemID] = it
	return it, nil
}

func (m *memStore) SetItemPendingPlan(_ context.Context, tenantID, itemID, pendingPlanID string, effectiveAt time.Time) (domain.SubscriptionItem, error) {
	it, ok := m.items[itemID]
	if !ok || it.TenantID != tenantID {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	it.PendingPlanID = pendingPlanID
	it.PendingPlanEffectiveAt = &effectiveAt
	it.UpdatedAt = time.Now().UTC()
	m.items[itemID] = it
	return it, nil
}

func (m *memStore) ClearItemPendingPlan(_ context.Context, tenantID, itemID string) (domain.SubscriptionItem, error) {
	it, ok := m.items[itemID]
	if !ok || it.TenantID != tenantID {
		return domain.SubscriptionItem{}, errs.ErrNotFound
	}
	it.PendingPlanID = ""
	it.PendingPlanEffectiveAt = nil
	it.UpdatedAt = time.Now().UTC()
	m.items[itemID] = it
	return it, nil
}

func (m *memStore) ApplyDuePendingItemPlansAtomic(_ context.Context, tenantID, subscriptionID string, now time.Time) ([]domain.SubscriptionItem, error) {
	var applied []domain.SubscriptionItem
	for id, it := range m.items {
		if it.TenantID != tenantID || it.SubscriptionID != subscriptionID {
			continue
		}
		if it.PendingPlanID == "" || it.PendingPlanEffectiveAt == nil || it.PendingPlanEffectiveAt.After(now) {
			continue
		}
		it.PlanID = it.PendingPlanID
		it.PlanChangedAt = &now
		it.PendingPlanID = ""
		it.PendingPlanEffectiveAt = nil
		it.UpdatedAt = now
		m.items[id] = it
		applied = append(applied, it)
	}
	return applied, nil
}

func (m *memStore) RemoveItem(_ context.Context, tenantID, itemID string) error {
	it, ok := m.items[itemID]
	if !ok || it.TenantID != tenantID {
		return errs.ErrNotFound
	}
	delete(m.items, itemID)
	return nil
}

func (m *memStore) hydrateItems(subID string) []domain.SubscriptionItem {
	var out []domain.SubscriptionItem
	for _, it := range m.items {
		if it.SubscriptionID == subID {
			out = append(out, it)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCreate(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	t.Run("draft by default", func(t *testing.T) {
		sub, err := svc.Create(ctx, "t1", CreateInput{
			Code: "sub-001", DisplayName: "Pro Monthly",
			CustomerID: "cus_1",
			Items:      []CreateItemInput{{PlanID: "pln_1"}},
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
		if len(sub.Items) != 1 || sub.Items[0].PlanID != "pln_1" || sub.Items[0].Quantity != 1 {
			t.Errorf("items: got %+v, want single pln_1 qty=1", sub.Items)
		}
	})

	t.Run("multiple items", func(t *testing.T) {
		sub, err := svc.Create(ctx, "t1", CreateInput{
			Code: "sub-multi", DisplayName: "Bundle",
			CustomerID: "cus_1",
			Items: []CreateItemInput{
				{PlanID: "pln_base", Quantity: 2},
				{PlanID: "pln_addon", Quantity: 10},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sub.Items) != 2 {
			t.Fatalf("items count: got %d, want 2", len(sub.Items))
		}
	})

	t.Run("start_now activates immediately", func(t *testing.T) {
		sub, err := svc.Create(ctx, "t1", CreateInput{
			Code: "sub-002", DisplayName: "Active",
			CustomerID: "cus_1",
			Items:      []CreateItemInput{{PlanID: "pln_1"}},
			StartNow:   true,
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
			CustomerID: "cus_1",
			Items:      []CreateItemInput{{PlanID: "pln_1"}},
			TrialDays:  14,
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
			{DisplayName: "x", CustomerID: "c", Items: []CreateItemInput{{PlanID: "p"}}},                                   // missing code
			{Code: "x", CustomerID: "c", Items: []CreateItemInput{{PlanID: "p"}}},                                          // missing display_name
			{Code: "x", DisplayName: "x", Items: []CreateItemInput{{PlanID: "p"}}},                                         // missing customer_id
			{Code: "x", DisplayName: "x", CustomerID: "c"},                                                                 // missing items
			{Code: "x", DisplayName: "x", CustomerID: "c", Items: []CreateItemInput{{}}},                                   // item missing plan_id
			{Code: "x", DisplayName: "x", CustomerID: "c", Items: []CreateItemInput{{PlanID: "p"}, {PlanID: "p"}}},         // duplicate plan
			{Code: "x", DisplayName: "x", CustomerID: "c", Items: []CreateItemInput{{PlanID: "p"}}, BillingTime: "weekly"}, // invalid billing_time
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
		Code: "sub-act", DisplayName: "Test", CustomerID: "c",
		Items: []CreateItemInput{{PlanID: "p"}},
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

// TestUpdateItem_PlanChange exercises immediate and scheduled per-item plan
// changes. Mirrors the invariants the old TestChangePlan covered, scoped to
// a single item: immediate stamps plan_changed_at and overwrites plan_id;
// scheduled leaves plan_id alone and records pending fields keyed to the
// current billing period end.
func TestUpdateItem_PlanChange(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	sub, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "sub-change", DisplayName: "Test", CustomerID: "c",
		Items:    []CreateItemInput{{PlanID: "plan_old"}},
		StartNow: true,
	})

	// Set billing period so scheduled-change effective_at lines up with it.
	store := svc.store.(*memStore)
	s := store.subs[sub.ID]
	start := time.Now().UTC().AddDate(0, 0, -15)
	end := time.Now().UTC().AddDate(0, 0, 15)
	s.CurrentBillingPeriodStart = &start
	s.CurrentBillingPeriodEnd = &end
	store.subs[sub.ID] = s

	itemID := sub.Items[0].ID

	t.Run("immediate plan swap", func(t *testing.T) {
		result, err := svc.UpdateItem(ctx, "t1", sub.ID, itemID, UpdateItemInput{
			NewPlanID: "plan_new",
			Immediate: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Item.PlanID != "plan_new" {
			t.Errorf("plan_id: got %q, want plan_new", result.Item.PlanID)
		}
		if result.Item.PlanChangedAt == nil {
			t.Error("plan_changed_at should be set")
		}
	})

	t.Run("same plan rejected", func(t *testing.T) {
		_, err := svc.UpdateItem(ctx, "t1", sub.ID, itemID, UpdateItemInput{NewPlanID: "plan_new", Immediate: true})
		if err == nil {
			t.Fatal("expected error for same plan")
		}
	})

	t.Run("missing new_plan_id and quantity", func(t *testing.T) {
		_, err := svc.UpdateItem(ctx, "t1", sub.ID, itemID, UpdateItemInput{})
		if err == nil {
			t.Fatal("expected error for missing fields")
		}
	})

	t.Run("both quantity and new_plan_id rejected", func(t *testing.T) {
		q := int64(2)
		_, err := svc.UpdateItem(ctx, "t1", sub.ID, itemID, UpdateItemInput{Quantity: &q, NewPlanID: "other"})
		if err == nil {
			t.Fatal("expected error when both fields set")
		}
	})
}

// TestUpdateItem_Scheduled locks in that a non-immediate plan change records
// pending fields at the billing period boundary and leaves the active plan_id
// untouched. Cancel-pending and immediate-supersede-pending are exercised too.
func TestUpdateItem_Scheduled(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	sub, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "sub-sched", DisplayName: "Test", CustomerID: "c",
		Items:    []CreateItemInput{{PlanID: "plan_old"}},
		StartNow: true,
	})

	store := svc.store.(*memStore)
	s := store.subs[sub.ID]
	start := time.Now().UTC().AddDate(0, 0, -5)
	end := time.Now().UTC().AddDate(0, 0, 25)
	s.CurrentBillingPeriodStart = &start
	s.CurrentBillingPeriodEnd = &end
	store.subs[sub.ID] = s

	itemID := sub.Items[0].ID

	t.Run("records pending, leaves plan_id untouched", func(t *testing.T) {
		result, err := svc.UpdateItem(ctx, "t1", sub.ID, itemID, UpdateItemInput{
			NewPlanID: "plan_new",
			Immediate: false,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Item.PlanID != "plan_old" {
			t.Errorf("plan_id must not change on scheduled: got %q, want plan_old", result.Item.PlanID)
		}
		if result.Item.PendingPlanID != "plan_new" {
			t.Errorf("pending_plan_id: got %q, want plan_new", result.Item.PendingPlanID)
		}
		if result.Item.PendingPlanEffectiveAt == nil {
			t.Fatal("pending_plan_effective_at must be set")
		}
		if !result.Item.PendingPlanEffectiveAt.Equal(end) {
			t.Errorf("pending_plan_effective_at: got %v, want period end %v", *result.Item.PendingPlanEffectiveAt, end)
		}
		if !result.EffectiveAt.Equal(end) {
			t.Errorf("response effective_at: got %v, want %v", result.EffectiveAt, end)
		}
	})

	t.Run("cancel pending clears scheduled change", func(t *testing.T) {
		updated, err := svc.CancelPendingItemChange(ctx, "t1", sub.ID, itemID)
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
		updated, err := svc.CancelPendingItemChange(ctx, "t1", sub.ID, itemID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if updated.PendingPlanID != "" {
			t.Errorf("expected no pending: got %q", updated.PendingPlanID)
		}
	})

	t.Run("immediate supersedes pending", func(t *testing.T) {
		_, err := svc.UpdateItem(ctx, "t1", sub.ID, itemID, UpdateItemInput{NewPlanID: "plan_scheduled", Immediate: false})
		if err != nil {
			t.Fatalf("schedule: %v", err)
		}
		result, err := svc.UpdateItem(ctx, "t1", sub.ID, itemID, UpdateItemInput{NewPlanID: "plan_immediate", Immediate: true})
		if err != nil {
			t.Fatalf("immediate: %v", err)
		}
		if result.Item.PlanID != "plan_immediate" {
			t.Errorf("plan_id: got %q, want plan_immediate", result.Item.PlanID)
		}
		if result.Item.PendingPlanID != "" {
			t.Errorf("pending_plan_id should be cleared: got %q", result.Item.PendingPlanID)
		}
	})
}

// TestAddRemoveItem covers item add/remove rules: duplicate plans rejected,
// removing the last item on an active subscription is blocked (caller should
// Cancel instead), removing a non-last item succeeds.
func TestAddRemoveItem(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	sub, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "sub-items", DisplayName: "Test", CustomerID: "c",
		Items:    []CreateItemInput{{PlanID: "plan_base"}},
		StartNow: true,
	})

	t.Run("add second item", func(t *testing.T) {
		added, err := svc.AddItem(ctx, "t1", sub.ID, AddItemInput{PlanID: "plan_addon", Quantity: 5})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if added.PlanID != "plan_addon" || added.Quantity != 5 {
			t.Errorf("added item: got %+v", added)
		}
	})

	t.Run("duplicate plan rejected", func(t *testing.T) {
		_, err := svc.AddItem(ctx, "t1", sub.ID, AddItemInput{PlanID: "plan_base"})
		if err == nil {
			t.Fatal("expected ErrAlreadyExists for duplicate plan on subscription")
		}
	})

	t.Run("remove non-last item", func(t *testing.T) {
		fresh, _ := svc.Get(ctx, "t1", sub.ID)
		var addonID string
		for _, it := range fresh.Items {
			if it.PlanID == "plan_addon" {
				addonID = it.ID
			}
		}
		if addonID == "" {
			t.Fatal("addon item not found")
		}
		if err := svc.RemoveItem(ctx, "t1", sub.ID, addonID); err != nil {
			t.Fatalf("remove item: %v", err)
		}
	})

	t.Run("cannot remove last item on active subscription", func(t *testing.T) {
		fresh, _ := svc.Get(ctx, "t1", sub.ID)
		if len(fresh.Items) != 1 {
			t.Fatalf("expected 1 item remaining, got %d", len(fresh.Items))
		}
		err := svc.RemoveItem(ctx, "t1", sub.ID, fresh.Items[0].ID)
		if err == nil {
			t.Fatal("expected error removing last item from active sub")
		}
	})
}

func TestPauseAndResume(t *testing.T) {
	svc := NewService(newMemStore(), nil)
	ctx := context.Background()

	sub, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "sub-pause", DisplayName: "Test", CustomerID: "c",
		Items:    []CreateItemInput{{PlanID: "p"}},
		StartNow: true,
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
