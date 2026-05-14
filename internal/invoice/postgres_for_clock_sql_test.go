package invoice_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestPerClockQueries_SQLValid is a regression for the ambiguous-column
// bug surfaced 2026-05-08 — the three per-clock invoice queries
// (ListAutoChargePendingForClock, ListPendingTaxRetryForClock,
// ListApproachingDueForClock) JOIN invoices to subscriptions, and the
// SELECT had bare invCols (no alias prefix), so Postgres rejected
// `id`/`tenant_id`/etc. as ambiguous (SQLSTATE 42702). Live operator
// hit this on the first real Advance after ADR-029 shipped.
//
// This test executes each query against a real Postgres against an
// empty table — it asserts the SQL is valid (no syntax / ambiguous-
// column error) regardless of fixture data. Catches the same class
// of bug in CI before any operator hits it again. Mirrors the
// principle from feedback_long_term_means_cross_flow_audit: when
// adding code that reaches the SQL layer, exercise it with a real
// connection at least once, not just with mock interfaces.
func TestPerClockQueries_SQLValid(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := invoice.NewPostgresStore(db)
	ctx := postgres.WithLivemode(context.Background(), false)

	// Empty-DB smoke: each query should return ([], nil) — proving the
	// SQL parses and executes, not that anything matches.
	t.Run("ListAutoChargePendingForClock", func(t *testing.T) {
		got, err := store.ListAutoChargePendingForClock(ctx, "vlx_ten_test", "vlx_tclk_test", 50)
		if err != nil {
			t.Fatalf("expected nil error on empty DB; got: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice; got %d rows", len(got))
		}
	})

	t.Run("ListPendingTaxRetryForClock", func(t *testing.T) {
		got, err := store.ListPendingTaxRetryForClock(ctx, "vlx_ten_test", "vlx_tclk_test",
			[]string{"provider_outage", "unknown"}, 8, 50)
		if err != nil {
			t.Fatalf("expected nil error on empty DB; got: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice; got %d rows", len(got))
		}
	})

	t.Run("ListApproachingDueForClock", func(t *testing.T) {
		got, err := store.ListApproachingDueForClock(ctx, "vlx_ten_test", "vlx_tclk_test",
			time.Now(), 3)
		if err != nil {
			t.Fatalf("expected nil error on empty DB; got: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice; got %d rows", len(got))
		}
	})
}
