package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// Read-path integration coverage (audit e2e 2026-07-13 — the read path had
// ZERO tests). Pins: explicit tenant+livemode predicate isolation, the
// cursor round-trip with over-fetch, FilterOptions scoping, and the 0147
// index migration (duplicate dropped, read index present).
func TestAuditReadPath_PredicatesCursorAndIndex(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenant1 := testutil.CreateTestTenant(t, db, "Read Path T1")
	tenant2 := testutil.CreateTestTenant(t, db, "Read Path T2")

	base := context.Background()
	ctxLive := postgres.WithLivemode(base, true)
	ctxTest := postgres.WithLivemode(base, false)
	ctx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()

	logger := audit.NewLogger(db)

	// Seed: 3 live rows for tenant1, 1 test-mode row for tenant1,
	// 1 live row for tenant2 (with a distinct action so FilterOptions
	// leakage would be visible).
	for _, action := range []string{"invoice.finalize", "invoice.void", "invoice.send"} {
		if err := logger.Log(ctxLive, tenant1, action, "invoice", "vlx_inv_t1", "", nil); err != nil {
			t.Fatalf("seed t1 live: %v", err)
		}
		// Distinct created_at per row so cursor ordering is deterministic.
		time.Sleep(5 * time.Millisecond)
	}
	if err := logger.Log(ctxTest, tenant1, "invoice.finalize", "invoice", "vlx_inv_t1_test", "", nil); err != nil {
		t.Fatalf("seed t1 test-mode: %v", err)
	}
	if err := logger.Log(ctxLive, tenant2, "customer.delete", "customer", "vlx_cus_t2", "", nil); err != nil {
		t.Fatalf("seed t2 live: %v", err)
	}

	t.Run("tenant and livemode isolation", func(t *testing.T) {
		live, total, err := logger.Query(ctxLive, tenant1, audit.QueryFilter{})
		if err != nil {
			t.Fatalf("query t1 live: %v", err)
		}
		if len(live) != 3 || total != 3 {
			t.Fatalf("t1 live rows: got %d (total %d), want 3", len(live), total)
		}
		for _, e := range live {
			if e.TenantID != tenant1 {
				t.Errorf("cross-tenant row leaked: %+v", e)
			}
		}

		testMode, total, err := logger.Query(ctxTest, tenant1, audit.QueryFilter{})
		if err != nil {
			t.Fatalf("query t1 test-mode: %v", err)
		}
		if len(testMode) != 1 || total != 1 {
			t.Fatalf("t1 test-mode rows: got %d (total %d), want 1 — live/test planes must not mix", len(testMode), total)
		}
	})

	t.Run("cursor page 2 continues without overlap", func(t *testing.T) {
		page1, _, err := logger.Query(ctxLive, tenant1, audit.QueryFilter{Limit: 2})
		if err != nil {
			t.Fatalf("page 1: %v", err)
		}
		if len(page1) != 2 {
			t.Fatalf("page 1 rows: got %d, want 2", len(page1))
		}
		last := page1[len(page1)-1]

		// Cursor path over-fetches limit+1 so the handler can derive
		// has_more; only 1 row remains, so expect exactly 1.
		page2, _, err := logger.Query(ctxLive, tenant1, audit.QueryFilter{
			Limit:          2,
			AfterCreatedAt: last.CreatedAt,
			AfterID:        last.ID,
		})
		if err != nil {
			t.Fatalf("page 2: %v", err)
		}
		if len(page2) != 1 {
			t.Fatalf("page 2 rows: got %d, want 1", len(page2))
		}
		seen := map[string]bool{page1[0].ID: true, page1[1].ID: true}
		if seen[page2[0].ID] {
			t.Errorf("cursor page 2 re-served a page-1 row: %s", page2[0].ID)
		}
	})

	t.Run("filter options scoped to tenant and mode", func(t *testing.T) {
		actions, resourceTypes, err := logger.FilterOptions(ctxLive, tenant1)
		if err != nil {
			t.Fatalf("filter options: %v", err)
		}
		for _, a := range actions {
			if a == "customer.delete" {
				t.Error("tenant2's action leaked into tenant1's filter vocabulary")
			}
		}
		for _, rt := range resourceTypes {
			if rt == "customer" {
				t.Error("tenant2's resource_type leaked into tenant1's filter vocabulary")
			}
		}
		if len(actions) != 3 {
			t.Errorf("actions: got %v, want the 3 live-mode t1 actions", actions)
		}
	})

	t.Run("0147 index shape", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer postgres.Rollback(tx)
		rows, err := tx.QueryContext(ctx,
			`SELECT indexname FROM pg_indexes WHERE tablename = 'audit_log'`)
		if err != nil {
			t.Fatalf("pg_indexes: %v", err)
		}
		defer func() { _ = rows.Close() }()
		have := map[string]bool{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatalf("scan: %v", err)
			}
			have[n] = true
		}
		if !have["idx_audit_log_tenant_read"] {
			t.Error("idx_audit_log_tenant_read missing — migration 0147 not applied?")
		}
		if have["idx_audit_log_created"] || have["idx_audit_log_tenant"] {
			t.Error("duplicate (tenant_id, created_at DESC) index survived 0147")
		}
	})
}
