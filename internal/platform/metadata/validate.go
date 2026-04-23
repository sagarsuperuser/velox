// Package metadata validates the optional key/value blob that domain
// objects carry for tenant-side tagging. The constraints match Stripe's
// public metadata contract — 50 keys, 40-char key, 500-char value — so
// integrators who already code against Stripe reuse the same mental
// model without a fresh migration of their tag data.
//
// Rationale for enforcing these limits at the edge rather than at the DB:
//   - A JSONB column happily stores megabytes; accepting that silently
//     turns metadata into a free-form content column and kills index
//     / scan performance tenant-wide.
//   - Surfacing the limit as a DomainError gives the client a precise
//     field to highlight in the UI, which a Postgres-side check constraint
//     cannot.
package metadata

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/sagarsuperuser/velox/internal/errs"
)

// Stripe-parity limits. Keep as constants so callers can reference them
// in error messages without importing a regex.
const (
	MaxKeys        = 50
	MaxKeyLength   = 40
	MaxValueLength = 500
	// MaxBytes is the hard ceiling on the raw JSON payload. Chosen to
	// match Stripe's ~8 KiB soft cap so a tenant that exports metadata
	// through an integration and re-imports it here doesn't get a
	// surprise rejection at the boundary.
	MaxBytes = 8 * 1024
)

// Validate rejects a metadata blob that exceeds the shape Stripe-compatible
// integrations can round-trip. Empty/nil/"null"/"{}" are all valid (no
// metadata). Non-string values are rejected — embedding nested objects
// or arrays invites tenants to encode relational structure into a tag
// blob, and the resulting shape is then painful to query or export.
//
// Returns a *DomainError with field=metadata so the handler layer can
// surface a 400 with the same message every endpoint uses. The caller
// is responsible for wiring `raw` from the on-the-wire JSON — we take
// bytes rather than map[string]any so callers who persist as raw JSON
// (like coupon.Coupon.Metadata) don't have to decode + re-encode just
// to validate.
func Validate(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > MaxBytes {
		return errs.Invalid("metadata",
			fmt.Sprintf("must be at most %d bytes (got %d)", MaxBytes, len(raw)))
	}

	// "null" and "{}" both decode to an empty map — short-circuit to
	// avoid the allocation.
	if string(raw) == "null" || string(raw) == "{}" {
		return nil
	}

	var kv map[string]any
	if err := json.Unmarshal(raw, &kv); err != nil {
		return errs.Invalid("metadata", "must be a JSON object")
	}

	if len(kv) > MaxKeys {
		return errs.Invalid("metadata",
			fmt.Sprintf("must have at most %d keys (got %d)", MaxKeys, len(kv)))
	}

	for k, v := range kv {
		// UTF-8 rune count, not byte length — a two-byte character shouldn't
		// count double against the budget.
		if kl := utf8.RuneCountInString(k); kl == 0 || kl > MaxKeyLength {
			return errs.Invalid("metadata",
				fmt.Sprintf("key %q must be 1-%d characters", k, MaxKeyLength))
		}
		s, ok := v.(string)
		if !ok {
			return errs.Invalid("metadata",
				fmt.Sprintf("value for %q must be a string (nested objects and arrays are not supported)", k))
		}
		if utf8.RuneCountInString(s) > MaxValueLength {
			return errs.Invalid("metadata",
				fmt.Sprintf("value for %q must be at most %d characters", k, MaxValueLength))
		}
	}
	return nil
}
