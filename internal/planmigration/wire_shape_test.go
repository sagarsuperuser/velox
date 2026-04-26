package planmigration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/billing"
)

// TestWireShape_PlanMigrationPreview_SnakeCase pins the JSON contract for
// POST /v1/admin/plan_migrations/preview. The dashboard reads:
//
//	previews[].{customer_id, current_plan_id, target_plan_id, before, after, delta_amount_cents, currency}
//	totals[].{currency, before_amount_cents, after_amount_cents, delta_amount_cents}
//	warnings (always-array, never null)
//
// Drift here breaks the migration tool's preview table at runtime, so we
// marshal a populated PreviewResult and assert every documented key plus
// the absence of PascalCase leakage.
func TestWireShape_PlanMigrationPreview_SnakeCase(t *testing.T) {
	preview := PreviewResult{
		Previews: []CustomerPreview{
			{
				CustomerID:    "vlx_cus_abc",
				CurrentPlanID: "vlx_plan_starter",
				TargetPlanID:  "vlx_plan_pro",
				Before: billing.PreviewResult{
					Totals: []billing.PreviewTotal{{Currency: "USD", AmountCents: 5000}},
				},
				After: billing.PreviewResult{
					Totals: []billing.PreviewTotal{{Currency: "USD", AmountCents: 10000}},
				},
				DeltaAmountCents: 5000,
				Currency:         "USD",
			},
		},
		Totals: []MigrationTotal{
			{Currency: "USD", BeforeAmountCents: 5000, AfterAmountCents: 10000, DeltaAmountCents: 5000},
		},
		Warnings: nil, // exercise nil → [] coercion
	}

	raw := marshalToMap(t, toWirePreviewResponse(preview))

	// Top-level keys.
	for _, k := range []string{"previews", "totals", "warnings"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("response missing %q (keys=%v)", k, mapKeys(raw))
		}
	}
	for _, k := range []string{"Previews", "Totals", "Warnings"} {
		if _, ok := raw[k]; ok {
			t.Errorf("response leaked PascalCase key %q", k)
		}
	}

	// warnings must always be an array (never null) — frontend iterates
	// without nil-guarding.
	wArr, ok := raw["warnings"].([]any)
	if !ok {
		t.Fatalf("warnings must marshal as JSON array, got %T", raw["warnings"])
	}
	if len(wArr) != 0 {
		t.Errorf("warnings should be empty array in this fixture, got %d entries", len(wArr))
	}

	// Per-preview row shape.
	pvArr, ok := raw["previews"].([]any)
	if !ok || len(pvArr) == 0 {
		t.Fatalf("previews must marshal as non-empty JSON array, got %T (len=%d)", raw["previews"], len(pvArr))
	}
	pv, ok := pvArr[0].(map[string]any)
	if !ok {
		t.Fatalf("previews[0] must be a JSON object, got %T", pvArr[0])
	}
	for _, k := range []string{
		"customer_id", "current_plan_id", "target_plan_id",
		"before", "after", "delta_amount_cents", "currency",
	} {
		if _, ok := pv[k]; !ok {
			t.Errorf("previews[0] missing %q (keys=%v)", k, mapKeys(pv))
		}
	}
	for _, k := range []string{
		"CustomerID", "CurrentPlanID", "TargetPlanID",
		"Before", "After", "DeltaAmountCents", "Currency",
	} {
		if _, ok := pv[k]; ok {
			t.Errorf("previews[0] leaked PascalCase key %q", k)
		}
	}

	// delta_amount_cents must be an int64-shaped JSON number, not a string.
	if _, isFloat := pv["delta_amount_cents"].(float64); !isFloat {
		t.Errorf("previews[0].delta_amount_cents must marshal as JSON number, got %T", pv["delta_amount_cents"])
	}

	// Per-total row shape (always-array of currency-keyed buckets).
	totArr, ok := raw["totals"].([]any)
	if !ok || len(totArr) == 0 {
		t.Fatalf("totals must marshal as non-empty JSON array, got %T (len=%d)", raw["totals"], len(totArr))
	}
	tot, ok := totArr[0].(map[string]any)
	if !ok {
		t.Fatalf("totals[0] must be a JSON object, got %T", totArr[0])
	}
	for _, k := range []string{
		"currency", "before_amount_cents", "after_amount_cents", "delta_amount_cents",
	} {
		if _, ok := tot[k]; !ok {
			t.Errorf("totals[0] missing %q (keys=%v)", k, mapKeys(tot))
		}
	}
	for _, k := range []string{
		"Currency", "BeforeAmountCents", "AfterAmountCents", "DeltaAmountCents",
	} {
		if _, ok := tot[k]; ok {
			t.Errorf("totals[0] leaked PascalCase key %q", k)
		}
	}
}

// TestWireShape_PlanMigrationCommit_SnakeCase pins the JSON contract for
// POST /v1/admin/plan_migrations/commit. Dashboard reads:
//
//	{migration_id, applied_count, audit_log_id, idempotent_replay?}
func TestWireShape_PlanMigrationCommit_SnakeCase(t *testing.T) {
	resp := wireCommitResponse{
		MigrationID:      "vlx_pmig_abc",
		AppliedCount:     12,
		AuditLogID:       "vlx_aud_xyz",
		IdempotentReplay: true,
	}

	raw := marshalToMap(t, resp)

	for _, k := range []string{"migration_id", "applied_count", "audit_log_id"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("response missing %q (keys=%v)", k, mapKeys(raw))
		}
	}
	for _, k := range []string{
		"MigrationID", "AppliedCount", "AuditLogID", "IdempotentReplay",
	} {
		if _, ok := raw[k]; ok {
			t.Errorf("response leaked PascalCase key %q", k)
		}
	}
	// idempotent_replay is omitempty — present here because we set it true.
	if _, ok := raw["idempotent_replay"]; !ok {
		t.Errorf("response missing idempotent_replay when true (keys=%v)", mapKeys(raw))
	}

	// Numeric fields must serialise as JSON numbers, not strings.
	if _, isFloat := raw["applied_count"].(float64); !isFloat {
		t.Errorf("applied_count must marshal as JSON number, got %T", raw["applied_count"])
	}
}

// TestWireShape_PlanMigrationList_SnakeCase pins the JSON contract for
// GET /v1/admin/plan_migrations. Dashboard reads:
//
//	{migrations: [{migration_id, from_plan_id, to_plan_id, effective, applied_at,
//	  applied_by, applied_by_type, applied_count, customer_filter, totals,
//	  idempotency_key, audit_log_id?}], next_cursor}
func TestWireShape_PlanMigrationList_SnakeCase(t *testing.T) {
	row := Migration{
		ID:             "vlx_pmig_abc",
		TenantID:       "vlx_tenant_xyz",
		IdempotencyKey: "key-2026-04-26-001",
		FromPlanID:     "vlx_plan_starter",
		ToPlanID:       "vlx_plan_pro",
		CustomerFilter: CustomerFilter{Type: "all"},
		Effective:      "immediate",
		AppliedCount:   3,
		Totals: []MigrationTotal{
			{Currency: "USD", BeforeAmountCents: 1500, AfterAmountCents: 3000, DeltaAmountCents: 1500},
		},
		AppliedBy:     "vlx_apik_op",
		AppliedByType: "api_key",
		AuditLogID:    "vlx_aud_xyz",
		CreatedAt:     time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
	}
	resp := wireListResponse{
		Migrations: []wireMigrationListItem{toWireListItem(row)},
		NextCursor: "vlx_pmig_prev",
	}

	raw := marshalToMap(t, resp)

	// Top-level keys.
	for _, k := range []string{"migrations", "next_cursor"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("response missing %q (keys=%v)", k, mapKeys(raw))
		}
	}
	for _, k := range []string{"Migrations", "NextCursor"} {
		if _, ok := raw[k]; ok {
			t.Errorf("response leaked PascalCase key %q", k)
		}
	}

	migrations, ok := raw["migrations"].([]any)
	if !ok || len(migrations) == 0 {
		t.Fatalf("migrations must marshal as non-empty JSON array, got %T (len=%d)", raw["migrations"], len(migrations))
	}
	item, ok := migrations[0].(map[string]any)
	if !ok {
		t.Fatalf("migrations[0] must be a JSON object, got %T", migrations[0])
	}
	for _, k := range []string{
		"migration_id", "from_plan_id", "to_plan_id", "effective",
		"applied_at", "applied_by", "applied_by_type", "applied_count",
		"customer_filter", "totals", "idempotency_key", "audit_log_id",
	} {
		if _, ok := item[k]; !ok {
			t.Errorf("migrations[0] missing %q (keys=%v)", k, mapKeys(item))
		}
	}
	for _, k := range []string{
		"MigrationID", "FromPlanID", "ToPlanID", "Effective", "AppliedAt",
		"AppliedBy", "AppliedByType", "AppliedCount", "CustomerFilter",
		"Totals", "IdempotencyKey", "AuditLogID",
	} {
		if _, ok := item[k]; ok {
			t.Errorf("migrations[0] leaked PascalCase key %q", k)
		}
	}

	// customer_filter is always an object (not null).
	cf, ok := item["customer_filter"].(map[string]any)
	if !ok {
		t.Fatalf("customer_filter must marshal as JSON object, got %T", item["customer_filter"])
	}
	if cf["type"] != "all" {
		t.Errorf("customer_filter.type round-trip lost, got %v", cf["type"])
	}

	// totals is an always-array (never null).
	totals, ok := item["totals"].([]any)
	if !ok {
		t.Fatalf("totals must marshal as JSON array, got %T", item["totals"])
	}
	if len(totals) != 1 {
		t.Errorf("totals expected 1 entry, got %d", len(totals))
	}

	// applied_at is RFC3339 string.
	if _, isString := item["applied_at"].(string); !isString {
		t.Errorf("applied_at must marshal as JSON string, got %T", item["applied_at"])
	}
}

// marshalToMap round-trips any value through encoding/json into a generic
// map for key-by-key inspection. Mirrors the helper used in other
// wire_shape_test.go files in this repo (billing, billingalert, etc).
func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return raw
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
