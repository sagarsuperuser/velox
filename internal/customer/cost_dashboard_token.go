package customer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// costDashboardTokenPrefix — visible in logs, distinguishes the public
// embeddable cost-dashboard credential from the magic-link
// (vlx_cpml_), portal session (vlx_cps_), and API keys (vlx_secret_,
// vlx_publishable_). 32 bytes of entropy → 64 hex chars after the
// prefix, matching the hosted-invoice public token shape.
const costDashboardTokenPrefix = "vlx_pcd_"

// NewCostDashboardToken mints a fresh public cost-dashboard token.
// Uniqueness is enforced at the DB layer via the partial UNIQUE index
// on customers.cost_dashboard_token (migration 0064). The raw token
// is the sole credential for GET /v1/public/cost-dashboard/{token} —
// the operator copies it once at rotate-time; rotation invalidates
// the old token immediately.
func NewCostDashboardToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("cost dashboard token: %w", err)
	}
	return costDashboardTokenPrefix + hex.EncodeToString(raw[:]), nil
}
