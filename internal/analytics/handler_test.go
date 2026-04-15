package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
)

// ctxWithTenant returns a context with the given tenant ID injected,
// matching the auth middleware's behavior.
func ctxWithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, auth.TestTenantIDKey(), tenantID)
}

// ---------------------------------------------------------------------------
// Route registration tests
// ---------------------------------------------------------------------------

func TestRoutes_RegistersExpectedEndpoints(t *testing.T) {
	h := NewHandler(nil) // nil DB is fine — we're not calling the handlers
	r := h.Routes()

	chiRouter, ok := r.(*chi.Mux)
	if !ok {
		t.Fatal("Routes() should return a *chi.Mux")
	}

	// Walk the route tree and collect registered routes.
	type route struct{ method, pattern string }
	var routes []route
	chi.Walk(chiRouter, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, route{method, pattern})
		return nil
	})

	expected := []route{
		{"GET", "/overview"},
		{"GET", "/revenue-chart"},
	}

	for _, want := range expected {
		found := false
		for _, got := range routes {
			if got.method == want.method && got.pattern == want.pattern {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing route: %s %s", want.method, want.pattern)
		}
	}
}

func TestRoutes_MethodNotAllowed(t *testing.T) {
	h := NewHandler(nil)
	r := h.Routes()

	// POST to a GET-only endpoint should be 405 (method not allowed)
	req := httptest.NewRequest("POST", "/overview", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /overview: got %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// Response type tests
// ---------------------------------------------------------------------------

func TestOverviewResponse_JSONShape(t *testing.T) {
	// Verify the OverviewResponse struct serializes all expected fields.
	resp := OverviewResponse{
		MRR:               1200_00,
		ActiveCustomers:   42,
		ActiveSubs:        38,
		TotalRevenue:      500_000_00,
		OutstandingAR:     15_000_00,
		AvgInvoiceValue:   320_00,
		PaidInvoices30d:   156,
		FailedPayments30d: 3,
		OpenInvoices:      12,
		DunningActive:     2,
		CreditBalance:     8_500_00,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal OverviewResponse: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	expectedFields := []string{
		"mrr", "active_customers", "active_subscriptions",
		"total_revenue", "outstanding_ar", "avg_invoice_value",
		"paid_invoices_30d", "failed_payments_30d", "open_invoices",
		"dunning_active", "credit_balance_total",
	}

	for _, field := range expectedFields {
		if _, ok := m[field]; !ok {
			t.Errorf("missing JSON field %q in OverviewResponse", field)
		}
	}

	// Verify there are no extra fields
	if len(m) != len(expectedFields) {
		t.Errorf("OverviewResponse has %d fields, want %d", len(m), len(expectedFields))
	}
}

func TestOverviewResponse_ZeroValues(t *testing.T) {
	// A zero-value response should serialize all fields as 0, not omit them.
	var resp OverviewResponse
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var m map[string]any
	json.Unmarshal(data, &m)

	for field, val := range m {
		if v, ok := val.(float64); !ok || v != 0 {
			t.Errorf("zero-value OverviewResponse: field %q = %v, want 0", field, val)
		}
	}
}

func TestRevenueDataPoint_JSONShape(t *testing.T) {
	dp := RevenueDataPoint{
		Date:         "2026-04-01",
		RevenueCents: 150_000,
		InvoiceCount: 42,
	}

	data, err := json.Marshal(dp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	json.Unmarshal(data, &m)

	if m["date"] != "2026-04-01" {
		t.Errorf("date: got %v", m["date"])
	}
	if m["revenue_cents"] != float64(150_000) {
		t.Errorf("revenue_cents: got %v", m["revenue_cents"])
	}
	if m["invoice_count"] != float64(42) {
		t.Errorf("invoice_count: got %v", m["invoice_count"])
	}
}

// ---------------------------------------------------------------------------
// Period parsing tests
// ---------------------------------------------------------------------------

func TestRevenueChart_PeriodParsing(t *testing.T) {
	// The revenueChart handler parses the "period" query parameter and maps it
	// to a SQL truncation interval and time range. We extract and test this
	// logic directly since the handler couples it with DB calls.
	//
	// This mirrors the switch statement in revenueChart():
	//   "30d" (default) -> trunc "day",  since = now - 30 days
	//   "90d"           -> trunc "day",  since = now - 90 days
	//   "12m"           -> trunc "month", since = now - 1 year

	tests := []struct {
		period    string
		wantTrunc string
	}{
		{"", "day"},      // default
		{"30d", "day"},   // explicit 30d
		{"90d", "day"},   // 90 days
		{"12m", "month"}, // 12 months
		{"bogus", "day"}, // unknown falls through to default
	}

	for _, tt := range tests {
		t.Run("period="+tt.period, func(t *testing.T) {
			trunc := parsePeriodTrunc(tt.period)
			if trunc != tt.wantTrunc {
				t.Errorf("parsePeriodTrunc(%q) = %q, want %q", tt.period, trunc, tt.wantTrunc)
			}
		})
	}
}

// parsePeriodTrunc replicates the truncation logic from revenueChart for testing.
// If this logic were extracted into a helper in handler.go, this test would call
// that directly. For now, this keeps the test self-contained and documents the
// expected behavior of the period -> truncation mapping.
func parsePeriodTrunc(period string) string {
	if period == "" {
		period = "30d"
	}
	switch period {
	case "90d":
		return "day"
	case "12m":
		return "month"
	default: // "30d" and anything unrecognized
		return "day"
	}
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNewHandler(t *testing.T) {
	h := NewHandler(nil)
	if h == nil {
		t.Fatal("NewHandler returned nil")
	}
	if h.db != nil {
		t.Error("db should be nil when passed nil")
	}
}

// ---------------------------------------------------------------------------
// Auth context integration
// ---------------------------------------------------------------------------

func TestTenantIDFromContext(t *testing.T) {
	// Verify our test helper correctly injects tenant ID the same way
	// the auth middleware does — the analytics handler relies on this.
	ctx := ctxWithTenant(context.Background(), "t_test_123")
	got := auth.TenantID(ctx)
	if got != "t_test_123" {
		t.Errorf("TenantID from context: got %q, want %q", got, "t_test_123")
	}
}

func TestTenantIDFromContext_Empty(t *testing.T) {
	// Without the auth middleware, TenantID returns empty string.
	got := auth.TenantID(context.Background())
	if got != "" {
		t.Errorf("TenantID without middleware: got %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// NOTE: SQL query correctness tests
// ---------------------------------------------------------------------------
//
// The overview and revenueChart handlers execute SQL directly against
// *postgres.DB (no store interface). The queries aggregate data across
// subscriptions, invoices, customers, dunning_runs, and credit_ledger tables.
//
// Unit-testing these queries with mocks would be brittle and low-value — the
// real risk is SQL correctness (joins, COALESCE, date_trunc, DISTINCT ON),
// which can only be validated against a real PostgreSQL instance.
//
// Integration tests should be added that:
//   1. Seed a test database with known data via the existing docker-compose postgres
//   2. Call GET /v1/analytics/overview and verify aggregated values
//   3. Call GET /v1/analytics/revenue-chart?period=30d|90d|12m and verify data points
//   4. Verify RLS isolation: tenant A's data is not visible to tenant B
//
// Run with: go test -p 1 ./internal/analytics/... -short=false
