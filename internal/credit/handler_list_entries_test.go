package credit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// filterSpyStore records the ListFilter the handler builds. The ledger
// endpoint silently ignored limit/offset pre-fix, so every consumer
// (pagination, type filter, CSV export) got the store's default-50 slice
// presented as the complete ledger; these tests fail if the handler stops
// forwarding any pagination param or the response loses `total`.
type filterSpyStore struct {
	*memStore
	listFilter  ListFilter
	countFilter ListFilter
}

func (s *filterSpyStore) ListEntries(ctx context.Context, filter ListFilter) ([]domain.CreditLedgerEntry, error) {
	s.listFilter = filter
	return s.memStore.ListEntries(ctx, filter)
}

func (s *filterSpyStore) CountEntries(ctx context.Context, filter ListFilter) (int, error) {
	s.countFilter = filter
	return s.memStore.CountEntries(ctx, filter)
}

// ledgerReq is creditReq's GET sibling: query string instead of body.
func ledgerReq(t *testing.T, h *Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/?"+query, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("customer_id", "cus_1")
	ctx := auth.WithTenantID(req.Context(), "tnt_test")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	rr := httptest.NewRecorder()
	h.listEntries(rr, req.WithContext(ctx))
	return rr
}

func TestCreditHandler_ListEntries_PaginationContract(t *testing.T) {
	newSpyHandler := func() (*filterSpyStore, *Handler) {
		spy := &filterSpyStore{memStore: newMemStore()}
		return spy, NewHandler(NewService(spy))
	}

	t.Run("forwards limit, offset, and entry_type to the store", func(t *testing.T) {
		spy, h := newSpyHandler()
		rr := ledgerReq(t, h, "limit=2&offset=7&entry_type=grant&sort=created_at&dir=asc")
		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, body=%s", rr.Code, rr.Body.String())
		}
		f := spy.listFilter
		if f.Limit != 2 || f.Offset != 7 || f.EntryType != "grant" || f.Sort != "created_at" || f.SortDir != "asc" {
			t.Errorf("filter not forwarded: %+v", f)
		}
	})

	t.Run("clamps limit to 1..100 and offset to >=0", func(t *testing.T) {
		for _, c := range []struct {
			query               string
			wantLimit, wantOffs int
		}{
			{"limit=500&offset=-3", 100, 0},
			{"limit=0", 50, 0},
			{"", 50, 0},
			{"limit=junk&offset=junk", 50, 0},
		} {
			spy, h := newSpyHandler()
			ledgerReq(t, h, c.query)
			if spy.listFilter.Limit != c.wantLimit || spy.listFilter.Offset != c.wantOffs {
				t.Errorf("query %q: got limit=%d offset=%d, want %d/%d",
					c.query, spy.listFilter.Limit, spy.listFilter.Offset, c.wantLimit, c.wantOffs)
			}
		}
	})

	t.Run("response carries total from CountEntries, not the page length", func(t *testing.T) {
		spy, h := newSpyHandler()
		for i := 0; i < 3; i++ {
			spy.entries = append(spy.entries, domain.CreditLedgerEntry{
				CustomerID: "cus_1", EntryType: domain.CreditGrant, AmountCents: 100,
			})
		}
		rr := ledgerReq(t, h, "limit=1")
		var resp struct {
			Data  []json.RawMessage `json:"data"`
			Total int               `json:"total"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
		}
		if resp.Total != 3 {
			t.Errorf("total: got %d, want 3 (the full count, independent of the page)", resp.Total)
		}
		if resp.Data == nil {
			t.Error("data must be a JSON array, never null")
		}
	})
}
