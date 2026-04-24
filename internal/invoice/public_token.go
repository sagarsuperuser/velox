package invoice

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// PublicTokenPrefix is the vlx_pinv_ identifier that tags hosted-invoice URL
// tokens. Kept as a package constant so the rotate endpoint and any future
// backfill job use the same format.
const PublicTokenPrefix = "vlx_pinv_"

// GeneratePublicToken creates a 256-bit random token for the hosted invoice
// page. The token is the URL — non-guessable by design and shareable via
// email or any public channel. Prefix mirrors the vlx_pt_ style used by
// payment-update tokens; livemode is NOT encoded in the token because the
// server resolves it from the underlying invoice row.
func GeneratePublicToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate public token: %w", err)
	}
	return PublicTokenPrefix + hex.EncodeToString(buf), nil
}
