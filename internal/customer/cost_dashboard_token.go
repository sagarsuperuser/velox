package customer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// CostDashboardTokenPrefix is the vlx_pcd_ identifier that tags
// cost-dashboard URL tokens. Kept as a package constant so the rotate
// endpoint and any future backfill job use the same format. Mirrors
// invoice.PublicTokenPrefix (vlx_pinv_) so partners see a consistent
// "what kind of public-token is this" pattern across surfaces.
const CostDashboardTokenPrefix = "vlx_pcd_"

// GenerateCostDashboardToken creates a 256-bit random token for the
// public cost-dashboard iframe URL. The token is the URL — non-guessable
// by design and shareable via the operator's own product surface.
// Livemode is NOT encoded in the token because the server resolves it
// from the underlying customer row.
func GenerateCostDashboardToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate cost dashboard token: %w", err)
	}
	return CostDashboardTokenPrefix + hex.EncodeToString(buf), nil
}
