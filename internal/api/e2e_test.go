package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestE2E_FullBillingCycle tests the complete flow via HTTP:
// bootstrap → create pricing → create customer → create subscription →
// ingest usage → trigger billing → verify invoice → download PDF
func TestE2E_FullBillingCycle(t *testing.T) {
	db := testutil.SetupTestDB(t)
	srv := NewServer(db, "")

	tenantID := testutil.CreateTestTenant(t, db, "E2E Test Corp")
	apiKey := createTestAPIKey(t, db, tenantID)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	auth := "Bearer " + apiKey

	// 1. Health check
	t.Run("health", func(t *testing.T) {
		resp := doGet(t, ts, "/health", "")
		assertStatus(t, resp, 200)
		body := readJSON(t, resp)
		if body["status"] != "ok" {
			t.Errorf("health: got %v", body)
		}
		if resp.Header.Get("Velox-Version") == "" {
			t.Error("missing Velox-Version header")
		}
	})

	// 2. Create rating rule
	var ruleID string
	t.Run("create rating rule", func(t *testing.T) {
		resp := doPost(t, ts, "/v1/rating-rules", auth, map[string]any{
			"rule_key":          "api_calls",
			"name":              "API Call Pricing",
			"mode":              "flat",
			"currency":          "USD",
			"flat_amount_cents": 1000,
		})
		assertStatus(t, resp, 201)
		body := readJSON(t, resp)
		ruleID = body["id"].(string)
	})

	// 3. Create meter
	var meterID string
	t.Run("create meter", func(t *testing.T) {
		resp := doPost(t, ts, "/v1/meters", auth, map[string]any{
			"key":                    "api_calls",
			"name":                   "API Calls",
			"unit":                   "calls",
			"aggregation":            "sum",
			"rating_rule_version_id": ruleID,
		})
		assertStatus(t, resp, 201)
		body := readJSON(t, resp)
		meterID = body["id"].(string)
	})

	// 4. Create plan
	var planID string
	t.Run("create plan", func(t *testing.T) {
		resp := doPost(t, ts, "/v1/plans", auth, map[string]any{
			"code":              "starter",
			"name":              "Starter Plan",
			"currency":          "USD",
			"billing_interval":  "monthly",
			"base_amount_cents": 2900,
			"meter_ids":         []string{meterID},
		})
		assertStatus(t, resp, 201)
		body := readJSON(t, resp)
		planID = body["id"].(string)
	})

	// 5. Create customer
	var customerID string
	t.Run("create customer", func(t *testing.T) {
		resp := doPost(t, ts, "/v1/customers", auth, map[string]any{
			"external_id":  "e2e_cust",
			"display_name": "E2E Test Customer",
			"email":        "test@e2e.com",
		})
		assertStatus(t, resp, 201)
		body := readJSON(t, resp)
		customerID = body["id"].(string)
	})

	// 6. Create subscription (start immediately — billing period auto-set)
	t.Run("create subscription", func(t *testing.T) {
		resp := doPost(t, ts, "/v1/subscriptions", auth, map[string]any{
			"code":         "e2e-sub",
			"display_name": "E2E Subscription",
			"customer_id":  customerID,
			"plan_id":      planID,
			"start_now":    true,
		})
		assertStatus(t, resp, 201)
		body := readJSON(t, resp)
		_ = body["id"].(string)
		if body["status"] != "active" {
			t.Errorf("status: got %v, want active", body["status"])
		}
		if body["current_billing_period_start"] == nil {
			t.Error("billing period should be auto-set")
		}
	})

	// 7. Ingest usage
	t.Run("ingest usage", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			resp := doPost(t, ts, "/v1/usage-events", auth, map[string]any{
				"external_customer_id": "e2e_cust",
				"event_name":           "api_calls",
				"quantity":             100,
			})
			assertStatus(t, resp, 201)
		}
	})

	// 8. Usage summary
	t.Run("usage summary", func(t *testing.T) {
		resp := doGet(t, ts, "/v1/usage-summary/"+customerID, auth)
		assertStatus(t, resp, 200)
		body := readJSON(t, resp)
		if body["total_events"].(float64) != 3 {
			t.Errorf("total_events: got %v, want 3", body["total_events"])
		}
	})

	// 9. Trigger billing
	t.Run("trigger billing", func(t *testing.T) {
		resp := doPost(t, ts, "/v1/billing/run", auth, nil)
		assertStatus(t, resp, 200)
		body := readJSON(t, resp)
		if body["invoices_generated"].(float64) != 1 {
			t.Errorf("invoices_generated: got %v, want 1", body["invoices_generated"])
		}
	})

	// 10. List invoices
	var invoiceID string
	t.Run("list invoices", func(t *testing.T) {
		resp := doGet(t, ts, "/v1/invoices", auth)
		assertStatus(t, resp, 200)
		body := readJSON(t, resp)
		data := body["data"].([]any)
		if len(data) != 1 {
			t.Fatalf("expected 1 invoice, got %d", len(data))
		}
		inv := data[0].(map[string]any)
		invoiceID = inv["id"].(string)
		// Base $29 + API flat $10 = $39
		if inv["total_amount_cents"].(float64) != 3900 {
			t.Errorf("total: got %v, want 3900", inv["total_amount_cents"])
		}
	})

	// 11. Invoice detail with line items
	t.Run("invoice detail", func(t *testing.T) {
		resp := doGet(t, ts, "/v1/invoices/"+invoiceID, auth)
		assertStatus(t, resp, 200)
		body := readJSON(t, resp)
		items := body["line_items"].([]any)
		if len(items) != 2 {
			t.Errorf("expected 2 line items, got %d", len(items))
		}
	})

	// 12. Download PDF
	t.Run("invoice pdf", func(t *testing.T) {
		resp := doGet(t, ts, "/v1/invoices/"+invoiceID+"/pdf", auth)
		assertStatus(t, resp, 200)
		if resp.Header.Get("Content-Type") != "application/pdf" {
			t.Errorf("content-type: got %q", resp.Header.Get("Content-Type"))
		}
	})

	// 13. Auth required
	t.Run("no auth 401", func(t *testing.T) {
		resp := doGet(t, ts, "/v1/customers", "")
		assertStatus(t, resp, 401)
	})

	// 14. Credits
	t.Run("grant credits", func(t *testing.T) {
		resp := doPost(t, ts, "/v1/credits/grant", auth, map[string]any{
			"customer_id":  customerID,
			"amount_cents": 5000,
			"description":  "Welcome credit",
		})
		assertStatus(t, resp, 201)

		resp = doGet(t, ts, "/v1/credits/balance/"+customerID, auth)
		assertStatus(t, resp, 200)
		body := readJSON(t, resp)
		if body["balance_cents"].(float64) != 5000 {
			t.Errorf("balance: got %v, want 5000", body["balance_cents"])
		}
	})

	t.Logf("E2E passed: invoice %s, total $39.00, 2 line items, PDF downloaded", invoiceID)
}

// --- helpers ---

func doGet(t *testing.T, ts *httptest.Server, path, auth string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func doPost(t *testing.T, ts *httptest.Server, path, auth string, body map[string]any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest("POST", ts.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		t.Fatalf("status: got %d, want %d. body: %v", resp.StatusCode, want, body)
	}
}

func readJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return body
}

func createTestAPIKey(t *testing.T, db *postgres.DB, tenantID string) string {
	t.Helper()

	secret := make([]byte, 32)
	rand.Read(secret)
	secretHex := hex.EncodeToString(secret)
	rawKey := "vlx_secret_" + secretHex
	prefix := "vlx_secret_" + secretHex[:12]
	hash := sha256.Sum256([]byte(rawKey))
	hashHex := hex.EncodeToString(hash[:])
	keyID := "vlx_key_e2e_" + secretHex[:8]

	tx, err := db.BeginTx(t.Context(), postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	_, err = tx.ExecContext(t.Context(),
		`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, name, tenant_id)
		VALUES ($1, $2, $3, 'secret', 'E2E Test Key', $4)`,
		keyID, prefix, hashHex, tenantID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("create key: %v", err)
	}
	_ = tx.Commit()

	return rawKey
}
