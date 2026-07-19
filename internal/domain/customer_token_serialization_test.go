package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCustomer_CostDashboardTokenNeverSerializes locks the show-once
// contract the API docs promise: the ONLY place the plaintext
// cost-dashboard token leaves the system is the rotate endpoint's own
// response payload. Until the 2026-07-19 truth audit the field carried
// a json tag, so every authenticated customer GET/List re-disclosed
// the credential indefinitely while the spec said "shown ONCE — Velox
// never returns it again."
func TestCustomer_CostDashboardTokenNeverSerializes(t *testing.T) {
	c := Customer{
		ID:                 "cus_1",
		CostDashboardToken: "vlx_pcd_supersecret",
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "vlx_pcd_supersecret") || strings.Contains(string(b), "cost_dashboard_token") {
		t.Fatalf("cost-dashboard token serialized on the Customer struct — every customer read re-discloses the credential: %s", b)
	}
}
