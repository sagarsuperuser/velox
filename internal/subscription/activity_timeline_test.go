package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// auditRecorderMock fakes the handler's audit seam for timeline tests —
// records the QueryFilter it was handed so tests can pin the resource scope.
type auditRecorderMock struct {
	entries   []domain.AuditEntry
	total     int
	queryErr  error
	gotFilter audit.QueryFilter
}

func (m *auditRecorderMock) Log(_ context.Context, _, _, _, _, _ string, _ map[string]any) error {
	return nil
}

func (m *auditRecorderMock) Query(_ context.Context, _ string, f audit.QueryFilter) ([]domain.AuditEntry, int, error) {
	m.gotFilter = f
	if m.queryErr != nil {
		return nil, 0, m.queryErr
	}
	return m.entries, m.total, nil
}

func timelineRequest(ctx context.Context, subID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/subscriptions/%s/timeline", subID), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", subID)
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
}

// The audit log is the timeline's ONLY event source: a query failure must
// surface as a 5xx, never a 200 with an empty feed indistinguishable from
// "this subscription has no history" — the empty-200 shape sent CS reps
// away thinking nothing had happened (audit e2e 2026-07-13).
func TestActivityTimeline_AuditQueryErrorSurfacesAs500(t *testing.T) {
	tenantID := "t1"
	store := newMemStore()
	subID, _ := seedSubWithItem(t, store, tenantID, "cus_1", "plan_a")

	h := NewHandler(NewService(store, nil))
	h.SetAuditLogger(&auditRecorderMock{queryErr: errors.New("audit db down")})

	rr := httptest.NewRecorder()
	h.activityTimeline(rr, timelineRequest(
		context.WithValue(context.Background(), auth.TestTenantIDKey(), tenantID), subID))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500 (empty-200 masks audit failures). body=%s", rr.Code, rr.Body.String())
	}
}

// Pins two contracts at once (audit e2e 2026-07-13, Q13):
//  1. SCOPE — the timeline is an object-scoped activity view under
//     PermSubscriptionRead; its audit query must always be pinned to this
//     one subscription (ResourceType+ResourceID). Widening it would turn a
//     weaker permission into a general audit-log reader.
//  2. TRUNCATION — the store clamps at 100 rows; when the sub has more, the
//     response must say so explicitly instead of silently starting the
//     timeline mid-history.
func TestActivityTimeline_ScopePinnedAndTruncationFlagged(t *testing.T) {
	tenantID := "t1"
	store := newMemStore()
	subID, _ := seedSubWithItem(t, store, tenantID, "cus_1", "plan_a")

	entries := make([]domain.AuditEntry, 100)
	for i := range entries {
		entries[i] = domain.AuditEntry{
			Action:    "subscription.updated",
			CreatedAt: time.Now().UTC().Add(-time.Duration(i) * time.Minute),
		}
	}
	rec := &auditRecorderMock{entries: entries, total: 150}

	h := NewHandler(NewService(store, nil))
	h.SetAuditLogger(rec)

	rr := httptest.NewRecorder()
	h.activityTimeline(rr, timelineRequest(
		context.WithValue(context.Background(), auth.TestTenantIDKey(), tenantID), subID))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}
	if rec.gotFilter.ResourceType != "subscription" || rec.gotFilter.ResourceID != subID {
		t.Errorf("audit query must stay pinned to this subscription; got filter %+v", rec.gotFilter)
	}

	var resp struct {
		Events    []map[string]any `json:"events"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.Truncated {
		t.Error("truncated: got false, want true (total 150 > 100 fetched)")
	}
	if len(resp.Events) != 100 {
		t.Errorf("events: got %d, want 100", len(resp.Events))
	}
}

func TestActivityTimeline_NoTruncationOnFullHistory(t *testing.T) {
	tenantID := "t1"
	store := newMemStore()
	subID, _ := seedSubWithItem(t, store, tenantID, "cus_1", "plan_a")

	rec := &auditRecorderMock{
		entries: []domain.AuditEntry{{Action: "subscription.created", CreatedAt: time.Now().UTC()}},
		total:   1,
	}
	h := NewHandler(NewService(store, nil))
	h.SetAuditLogger(rec)

	rr := httptest.NewRecorder()
	h.activityTimeline(rr, timelineRequest(
		context.WithValue(context.Background(), auth.TestTenantIDKey(), tenantID), subID))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Truncated {
		t.Error("truncated: got true, want false (full history fetched)")
	}
}
