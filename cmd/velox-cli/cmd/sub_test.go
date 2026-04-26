package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/cmd/velox-cli/internal/client"
	"github.com/sagarsuperuser/velox/cmd/velox-cli/internal/output"
)

// fixedSubsResponse is the canned shape an httptest server returns for
// the sub-list tests. We hand-build the JSON rather than depend on the
// internal/domain types so the CLI is exercised purely through the
// public JSON contract.
const fixedSubsResponse = `{
  "data": [
    {
      "id": "sub_001",
      "customer_id": "cus_alpha",
      "status": "active",
      "current_billing_period_end": "2026-05-01T00:00:00Z",
      "items": [{"plan_id": "plan_pro"}]
    },
    {
      "id": "sub_002",
      "customer_id": "cus_beta",
      "status": "trialing",
      "items": [{"plan_id": "plan_starter"}, {"plan_id": "plan_addon"}]
    }
  ],
  "total": 2
}`

func newSubServer(t *testing.T, wantQuery map[string]string, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/subscriptions" {
			t.Errorf("path = %s, want /v1/subscriptions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header = %q, want Bearer test-key", got)
		}
		for k, v := range wantQuery {
			if got := r.URL.Query().Get(k); got != v {
				t.Errorf("query %s = %q, want %q", k, got, v)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
}

func TestSubList(t *testing.T) {
	tests := []struct {
		name        string
		params      subListParams
		wantQuery   map[string]string
		wantContain []string
	}{
		{
			name: "text_default",
			params: subListParams{
				limit:  20,
				format: output.FormatText,
			},
			wantQuery: map[string]string{"limit": "20"},
			wantContain: []string{
				"ID", "CUSTOMER", "PLAN", "STATUS", "CURRENT_PERIOD_END",
				"sub_001", "cus_alpha", "plan_pro", "active", "2026-05-01T00:00:00Z",
				"sub_002", "cus_beta", "plan_starter,plan_addon", "trialing",
			},
		},
		{
			name: "text_with_filters",
			params: subListParams{
				customer: "cus_alpha",
				plan:     "plan_pro",
				status:   "active",
				limit:    50,
				format:   output.FormatText,
			},
			wantQuery: map[string]string{
				"customer_id": "cus_alpha",
				"plan_id":     "plan_pro",
				"status":      "active",
				"limit":       "50",
			},
			wantContain: []string{"sub_001", "active"},
		},
		{
			name: "json_passthrough",
			params: subListParams{
				limit:  20,
				format: output.FormatJSON,
			},
			wantQuery:   map[string]string{"limit": "20"},
			wantContain: []string{`"id": "sub_001"`, `"total": 2`, `"plan_id": "plan_pro"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := newSubServer(t, tc.wantQuery, fixedSubsResponse)
			defer ts.Close()

			c := client.New(ts.URL, "test-key")
			var buf bytes.Buffer
			if err := runSubList(context.Background(), &buf, c, tc.params); err != nil {
				t.Fatalf("runSubList: %v", err)
			}
			out := buf.String()
			for _, want := range tc.wantContain {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q\nfull output:\n%s", want, out)
				}
			}

			// JSON output must round-trip cleanly.
			if tc.params.format == output.FormatJSON {
				var anyResp map[string]any
				if err := json.Unmarshal([]byte(out), &anyResp); err != nil {
					t.Errorf("json output not valid JSON: %v", err)
				}
			}
		})
	}
}

func TestSubList_Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": [], "total": 0}`))
	}))
	defer ts.Close()

	c := client.New(ts.URL, "test-key")
	var buf bytes.Buffer
	if err := runSubList(context.Background(), &buf, c, subListParams{format: output.FormatText, limit: 20}); err != nil {
		t.Fatalf("runSubList empty: %v", err)
	}
	if !strings.Contains(buf.String(), "(no subscriptions matched)") {
		t.Errorf("empty list should emit a friendly empty-state line; got:\n%s", buf.String())
	}
}

func TestSubList_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "bad key"}}`))
	}))
	defer ts.Close()

	c := client.New(ts.URL, "test-key")
	var buf bytes.Buffer
	err := runSubList(context.Background(), &buf, c, subListParams{format: output.FormatText, limit: 20})
	if err == nil {
		t.Fatal("expected error from 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401; got %v", err)
	}
}
