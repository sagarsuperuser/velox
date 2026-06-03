package subscription

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// capturingAudit records audit Log calls so a test can assert the metadata a
// handler wrote, without a real DB. Satisfies auditRecorder.
type capturingAudit struct {
	entries []capturedAudit
}

type capturedAudit struct {
	action string
	meta   map[string]any
}

func (c *capturingAudit) Log(_ context.Context, _, action, _, _, _ string, metadata map[string]any) error {
	c.entries = append(c.entries, capturedAudit{action: action, meta: metadata})
	return nil
}

func (c *capturingAudit) Query(_ context.Context, _ string, _ audit.QueryFilter) ([]domain.AuditEntry, int, error) {
	return nil, 0, nil
}

func (c *capturingAudit) firstOf(action string) (capturedAudit, bool) {
	for _, e := range c.entries {
		if e.action == action {
			return e, true
		}
	}
	return capturedAudit{}, false
}

func (c *capturingAudit) actions() []string {
	out := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e.action)
	}
	return out
}

// A plan change on a CLOCK-PINNED subscription must record the test clock +
// simulated effect-time in the audit metadata (ADR-030), so the audit UI shows
// the wall-clock click time as the primary timestamp and "Effect on test clock
// at <sim>" as a subline. Pre-fix the item-update writer passed the raw payload
// instead of auditMetaForSub(sub, …) — the lone subscription audit path that
// omitted it — so these rows carried no test-clock context.
func TestHandler_UpdateItem_PlanChangeAuditCarriesSimContext(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	now := time.Now().UTC()
	ps := now.Add(-15 * 24 * time.Hour)
	pe := now.Add(15 * 24 * time.Hour)
	sub, err := store.Create(ctx, tenantID, domain.Subscription{
		Code: "sub-clk", DisplayName: "Clock Sub", CustomerID: "cus_1",
		Status:                    domain.SubscriptionActive,
		BillingTime:               domain.BillingTimeCalendar,
		CurrentBillingPeriodStart: &ps,
		CurrentBillingPeriodEnd:   &pe,
		TestClockID:               "tclk_1",
		Items:                     []domain.SubscriptionItem{{PlanID: "plan_old", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("seed clock-pinned sub: %v", err)
	}
	subID, itemID := sub.ID, sub.Items[0].ID

	svc := NewService(store, nil)
	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_old": {ID: "plan_old", Name: "Basic", BaseAmountCents: 1000, Currency: "USD", BaseBillTiming: domain.BillInAdvance},
		"plan_new": {ID: "plan_new", Name: "Pro", BaseAmountCents: 3000, Currency: "USD", BaseBillTiming: domain.BillInAdvance},
	}}
	h := NewHandler(svc)
	h.SetProrationDeps(plans, &invoicesMock{}, &creditsMock{})
	rec := &capturingAudit{}
	h.SetAuditLogger(rec)

	body, _ := json.Marshal(UpdateItemInput{NewPlanID: "plan_new", Immediate: true})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)
	rr := httptest.NewRecorder()
	h.updateItem(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}

	entry, ok := rec.firstOf("subscription.item_updated")
	if !ok {
		t.Fatalf("expected a subscription.item_updated audit row; got actions=%v", rec.actions())
	}
	if entry.meta["action"] != "item_plan_changed" {
		t.Errorf("meta.action: got %v, want item_plan_changed", entry.meta["action"])
	}
	if entry.meta["test_clock_id"] != "tclk_1" {
		t.Errorf("meta.test_clock_id: got %v, want tclk_1 — sim context dropped (the bug)", entry.meta["test_clock_id"])
	}
	if s, ok := entry.meta["sim_effective_at"].(string); !ok || s == "" {
		t.Errorf("meta.sim_effective_at: got %v, want a non-empty RFC3339 string", entry.meta["sim_effective_at"])
	}
}

// A plan change on a WALL-CLOCK (non-pinned) subscription must NOT inject
// sim-time context — auditMetaForSub is a no-op there.
func TestHandler_UpdateItem_PlanChangeAuditNoSimContextWhenUnpinned(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_old")

	svc := NewService(store, nil)
	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_old": {ID: "plan_old", Name: "Basic", BaseAmountCents: 1000, Currency: "USD", BaseBillTiming: domain.BillInAdvance},
		"plan_new": {ID: "plan_new", Name: "Pro", BaseAmountCents: 3000, Currency: "USD", BaseBillTiming: domain.BillInAdvance},
	}}
	h := NewHandler(svc)
	h.SetProrationDeps(plans, &invoicesMock{}, &creditsMock{})
	rec := &capturingAudit{}
	h.SetAuditLogger(rec)

	body, _ := json.Marshal(UpdateItemInput{NewPlanID: "plan_new", Immediate: true})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)
	rr := httptest.NewRecorder()
	h.updateItem(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}

	entry, ok := rec.firstOf("subscription.item_updated")
	if !ok {
		t.Fatalf("expected a subscription.item_updated audit row; got actions=%v", rec.actions())
	}
	if _, present := entry.meta["test_clock_id"]; present {
		t.Errorf("meta.test_clock_id present on a wall-clock sub: %v", entry.meta["test_clock_id"])
	}
	if _, present := entry.meta["sim_effective_at"]; present {
		t.Errorf("meta.sim_effective_at present on a wall-clock sub: %v", entry.meta["sim_effective_at"])
	}
}
