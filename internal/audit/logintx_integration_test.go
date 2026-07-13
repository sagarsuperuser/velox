package audit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// LogInTx totality gates (ADR-090, panel amendment A2). LogInTx runs inside
// money transactions — an input it cannot persist aborts the mutation, so
// every failure mode a caller could trigger must be either unrepresentable
// or absorbed. These tests run against real Postgres because the guarantees
// live in the schema (NOT NULLs, CHECK constraints, trigger, GUCs).
func TestLogInTx_TotalityAndVocabulary(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, "LogInTx Totality")

	ctx, cancel := context.WithTimeout(postgres.WithLivemode(context.Background(), false), 60*time.Second)
	defer cancel()
	logger := audit.NewLogger(db)

	// Every declared action wire-string must survive the full INSERT path —
	// schema CHECK drift on a new constant fails HERE, not in a customer's
	// money tx (the enum+CHECK-constraint-sync bug class).
	vocabulary := []string{
		domain.AuditActionCreate, domain.AuditActionUpdate, domain.AuditActionDelete,
		domain.AuditActionActivate, domain.AuditActionCancel, domain.AuditActionPause,
		domain.AuditActionResume, domain.AuditActionFinalize, domain.AuditActionVoid,
		domain.AuditActionRevoke, domain.AuditActionGrant, domain.AuditActionRefund,
		domain.AuditActionCollect, domain.AuditActionSend, domain.AuditActionRetryTax,
		domain.AuditActionRotate,
		// Dotted service vocabulary in live use (frozen wire strings).
		"credit.adjustment", "credit.deduction",
	}
	t.Run("vocabulary round-trip", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer postgres.Rollback(tx)
		for _, action := range vocabulary {
			if err := logger.LogInTx(ctx, tx, audit.Entry{
				Action: action, ResourceType: "vocab_probe", ResourceID: "vlx_probe_1",
			}); err != nil {
				t.Fatalf("vocabulary value %q rejected by the schema: %v", action, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	})

	t.Run("tenant_id comes from the tx GUC, not the caller", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer postgres.Rollback(tx)
		if err := logger.LogInTx(ctx, tx, audit.Entry{
			Action: "update", ResourceType: "guc_probe", ResourceID: "vlx_probe_2",
		}); err != nil {
			t.Fatalf("log: %v", err)
		}
		var gotTenant string
		var gotLivemode bool
		if err := tx.QueryRowContext(ctx,
			`SELECT tenant_id, livemode FROM audit_log WHERE resource_type = 'guc_probe'`,
		).Scan(&gotTenant, &gotLivemode); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if gotTenant != tenantID {
			t.Errorf("tenant_id: got %q, want the tx GUC's %q", gotTenant, tenantID)
		}
		if gotLivemode != false {
			t.Errorf("livemode: got %v, want false (trigger-stamped from the tx session)", gotLivemode)
		}
		_ = tx.Commit()
	})

	t.Run("unmarshalable metadata degrades, never aborts", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer postgres.Rollback(tx)
		if err := logger.LogInTx(ctx, tx, audit.Entry{
			Action: "update", ResourceType: "marshal_probe", ResourceID: "vlx_probe_3",
			Metadata: map[string]any{"bad": func() {}}, // json.Marshal error
		}); err != nil {
			t.Fatalf("unmarshalable metadata must degrade, not abort the tx: %v", err)
		}
		var metaJSON string
		if err := tx.QueryRowContext(ctx,
			`SELECT metadata::text FROM audit_log WHERE resource_type = 'marshal_probe'`,
		).Scan(&metaJSON); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if !strings.Contains(metaJSON, "marshal_error") {
			t.Errorf("degraded metadata must carry marshal_error; got %s", metaJSON)
		}
		_ = tx.Commit()
	})

	t.Run("sim context lands in columns and legacy metadata keys", func(t *testing.T) {
		simAt := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
		tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer postgres.Rollback(tx)
		if err := logger.LogInTx(ctx, tx, audit.Entry{
			Action: "cancel", ResourceType: "sim_probe", ResourceID: "vlx_probe_4",
			Sim: &audit.SimContext{EffectiveAt: simAt, TestClockID: "vlx_clk_1"},
		}); err != nil {
			t.Fatalf("log: %v", err)
		}
		var colAt time.Time
		var colClock, metaJSON string
		if err := tx.QueryRowContext(ctx,
			`SELECT sim_effective_at, test_clock_id, metadata::text FROM audit_log WHERE resource_type = 'sim_probe'`,
		).Scan(&colAt, &colClock, &metaJSON); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if !colAt.Equal(simAt) || colClock != "vlx_clk_1" {
			t.Errorf("sim columns: got (%v, %q), want (%v, vlx_clk_1) — migration 0148", colAt, colClock, simAt)
		}
		if !strings.Contains(metaJSON, "sim_effective_at") || !strings.Contains(metaJSON, "vlx_clk_1") {
			t.Errorf("legacy metadata mirror missing (dashboard renders these keys today): %s", metaJSON)
		}
		_ = tx.Commit()
	})

	t.Run("rollback takes the audit row with it — shared fate", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := logger.LogInTx(ctx, tx, audit.Entry{
			Action: "update", ResourceType: "rollback_probe", ResourceID: "vlx_probe_5",
		}); err != nil {
			t.Fatalf("log: %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		entries, _, err := logger.Query(ctx, tenantID, audit.QueryFilter{ResourceType: "rollback_probe"})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("phantom audit row survived a rolled-back tx: %+v", entries)
		}
	})
}
