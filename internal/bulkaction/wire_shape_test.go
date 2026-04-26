package bulkaction

import (
	"encoding/json"
	"testing"
	"time"
)

// TestWireShape_BulkActions_SnakeCase pins the JSON contract for every
// bulk_actions endpoint. The dashboard reads:
//
//	POST /v1/admin/bulk_actions/apply_coupon       → wireCommitResponse
//	POST /v1/admin/bulk_actions/schedule_cancel    → wireCommitResponse
//	GET  /v1/admin/bulk_actions                    → wireListResponse
//	GET  /v1/admin/bulk_actions/{id}               → wireDetailItem
//
// Drift here breaks the BulkActions page + customers multi-select drawer
// at runtime, so we marshal each response and assert every documented
// snake_case key plus the absence of PascalCase leakage.
func TestWireShape_BulkActions_SnakeCase(t *testing.T) {
	t.Run("commit response", func(t *testing.T) {
		commit := CommitResult{
			BulkActionID:   "vlx_bact_abc",
			Status:         StatusPartial,
			TargetCount:    10,
			SucceededCount: 7,
			FailedCount:    3,
			Errors: []TargetError{
				{CustomerID: "vlx_cus_a", Error: "coupon expired"},
			},
			IdempotentReplay: true,
		}
		raw := marshalToMap(t, toWireCommitResponse(commit))

		for _, k := range []string{
			"bulk_action_id", "status", "target_count", "succeeded_count",
			"failed_count", "errors",
		} {
			if _, ok := raw[k]; !ok {
				t.Errorf("commit response missing %q (keys=%v)", k, mapKeys(raw))
			}
		}
		for _, k := range []string{
			"BulkActionID", "Status", "TargetCount", "SucceededCount",
			"FailedCount", "Errors", "IdempotentReplay",
		} {
			if _, ok := raw[k]; ok {
				t.Errorf("commit response leaked PascalCase key %q", k)
			}
		}
		// idempotent_replay is omitempty; present here because true.
		if _, ok := raw["idempotent_replay"]; !ok {
			t.Errorf("commit response missing idempotent_replay when true (keys=%v)", mapKeys(raw))
		}
		// errors is always-array; per-row keys snake_case.
		errs, ok := raw["errors"].([]any)
		if !ok || len(errs) == 0 {
			t.Fatalf("errors must marshal as non-empty JSON array, got %T", raw["errors"])
		}
		row, ok := errs[0].(map[string]any)
		if !ok {
			t.Fatalf("errors[0] must be a JSON object, got %T", errs[0])
		}
		for _, k := range []string{"customer_id", "error"} {
			if _, ok := row[k]; !ok {
				t.Errorf("errors[0] missing %q (keys=%v)", k, mapKeys(row))
			}
		}
		for _, k := range []string{"CustomerID", "Error"} {
			if _, ok := row[k]; ok {
				t.Errorf("errors[0] leaked PascalCase key %q", k)
			}
		}
		// Numeric fields must serialise as JSON numbers, not strings.
		if _, isFloat := raw["target_count"].(float64); !isFloat {
			t.Errorf("target_count must marshal as JSON number, got %T", raw["target_count"])
		}
	})

	t.Run("commit response with empty errors is always array", func(t *testing.T) {
		commit := CommitResult{
			BulkActionID:   "vlx_bact_abc",
			Status:         StatusCompleted,
			TargetCount:    1,
			SucceededCount: 1,
			Errors:         nil, // exercise nil → [] coercion
		}
		raw := marshalToMap(t, toWireCommitResponse(commit))
		errs, ok := raw["errors"].([]any)
		if !ok {
			t.Fatalf("errors must marshal as JSON array even when nil, got %T", raw["errors"])
		}
		if len(errs) != 0 {
			t.Errorf("errors expected empty array, got %d entries", len(errs))
		}
	})

	t.Run("list response", func(t *testing.T) {
		completed := time.Date(2026, 4, 26, 12, 30, 0, 0, time.UTC)
		row := Action{
			ID:             "vlx_bact_abc",
			TenantID:       "vlx_tenant_xyz",
			IdempotencyKey: "bact_2026-04-26-001",
			ActionType:     ActionApplyCoupon,
			CustomerFilter: CustomerFilter{Type: "all"},
			Params:         map[string]any{"coupon_code": "SUMMER20"},
			Status:         StatusCompleted,
			TargetCount:    5,
			SucceededCount: 5,
			FailedCount:    0,
			Errors:         []TargetError{},
			CreatedBy:      "vlx_apik_op",
			CreatedAt:      time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
			CompletedAt:    &completed,
		}
		resp := wireListResponse{
			BulkActions: []wireListItem{toWireListItem(row)},
			NextCursor:  "vlx_bact_prev",
		}
		raw := marshalToMap(t, resp)

		for _, k := range []string{"bulk_actions", "next_cursor"} {
			if _, ok := raw[k]; !ok {
				t.Errorf("list response missing %q (keys=%v)", k, mapKeys(raw))
			}
		}
		for _, k := range []string{"BulkActions", "NextCursor"} {
			if _, ok := raw[k]; ok {
				t.Errorf("list response leaked PascalCase key %q", k)
			}
		}

		items, ok := raw["bulk_actions"].([]any)
		if !ok || len(items) == 0 {
			t.Fatalf("bulk_actions must marshal as non-empty JSON array, got %T", raw["bulk_actions"])
		}
		item, ok := items[0].(map[string]any)
		if !ok {
			t.Fatalf("bulk_actions[0] must be a JSON object, got %T", items[0])
		}
		for _, k := range []string{
			"bulk_action_id", "action_type", "status", "target_count",
			"succeeded_count", "failed_count", "customer_filter", "params",
			"idempotency_key", "created_by", "created_at", "completed_at",
		} {
			if _, ok := item[k]; !ok {
				t.Errorf("bulk_actions[0] missing %q (keys=%v)", k, mapKeys(item))
			}
		}
		for _, k := range []string{
			"BulkActionID", "ActionType", "Status", "TargetCount",
			"SucceededCount", "FailedCount", "CustomerFilter", "Params",
			"IdempotencyKey", "CreatedBy", "CreatedAt", "CompletedAt",
		} {
			if _, ok := item[k]; ok {
				t.Errorf("bulk_actions[0] leaked PascalCase key %q", k)
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
		// params is always an object.
		params, ok := item["params"].(map[string]any)
		if !ok {
			t.Fatalf("params must marshal as JSON object, got %T", item["params"])
		}
		if params["coupon_code"] != "SUMMER20" {
			t.Errorf("params.coupon_code round-trip lost, got %v", params["coupon_code"])
		}
		// created_at / completed_at are RFC3339 strings.
		if _, isString := item["created_at"].(string); !isString {
			t.Errorf("created_at must marshal as JSON string, got %T", item["created_at"])
		}
	})

	t.Run("detail response includes errors array", func(t *testing.T) {
		row := Action{
			ID:             "vlx_bact_abc",
			ActionType:     ActionScheduleCancel,
			CustomerFilter: CustomerFilter{Type: "ids", IDs: []string{"vlx_cus_a"}},
			Params:         map[string]any{"at_period_end": true},
			Status:         StatusPartial,
			TargetCount:    2,
			SucceededCount: 1,
			FailedCount:    1,
			Errors: []TargetError{
				{CustomerID: "vlx_cus_b", Error: "no active subscriptions"},
			},
			CreatedAt: time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		}
		raw := marshalToMap(t, toWireDetailItem(row))

		// Detail flattens list keys + adds errors[].
		for _, k := range []string{"bulk_action_id", "errors"} {
			if _, ok := raw[k]; !ok {
				t.Errorf("detail response missing %q (keys=%v)", k, mapKeys(raw))
			}
		}
		errs, ok := raw["errors"].([]any)
		if !ok {
			t.Fatalf("detail.errors must marshal as JSON array, got %T", raw["errors"])
		}
		if len(errs) != 1 {
			t.Errorf("detail.errors expected 1 entry, got %d", len(errs))
		}
		// customer_filter ids round-trip — ensure ids array carries through.
		cf, ok := raw["customer_filter"].(map[string]any)
		if !ok {
			t.Fatalf("customer_filter must marshal as JSON object, got %T", raw["customer_filter"])
		}
		ids, ok := cf["ids"].([]any)
		if !ok || len(ids) != 1 || ids[0] != "vlx_cus_a" {
			t.Errorf("customer_filter.ids round-trip lost, got %v", cf["ids"])
		}
	})
}

// marshalToMap round-trips any value through encoding/json into a generic
// map for key-by-key inspection. Mirrors the helper used in other
// wire_shape_test.go files in this repo.
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
