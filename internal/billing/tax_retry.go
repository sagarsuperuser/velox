package billing

import (
	"math/rand"
	"time"

	"github.com/sagarsuperuser/velox/internal/tax"
)

// Tax-retry policy for the background reconciler (ADR-017).
//
// Industry references walked before settling on these values:
//
//   Stripe:    payment retries cap at 8 attempts over ~3 weeks
//              (Smart Retries). Tax internally retried by their
//              backend; surface contract is "draft until resolved".
//   Recurly:   tax cron retries on transient errors with 5min →
//              30min → 1h → 6h → 24h backoff, max ~10 attempts.
//   Chargebee: retries provider_outage / timeout codes only.
//   Lago:      no automatic retry; manual operator action only.
//
// Velox lands on Recurly's curve (which is itself the Stripe-Smart-
// Retries shape adapted to tax). 8 attempts cover transient outages
// over ~10 days; longer than that and an operator should know.

// maxTaxRetryAttempts is the hard cap on automatic retries before
// the reconciler stops re-scheduling. After the cap, the invoice
// stays at tax_status=pending|failed with its last tax_error_code
// — the operator-facing attention surface remains live so a human
// can act, but the worker stops burning provider quota.
const maxTaxRetryAttempts = 8

// taxRetryBackoff returns the wait duration for the Nth attempt.
// `attempts` is the number of attempts already made (so 0 → first
// retry, 1 → second, etc.).
//
// Schedule:
//
//	1st retry → +5  min
//	2nd       → +15 min
//	3rd       → +1  hour
//	4th       → +4  hours
//	5th       → +12 hours
//	6th       → +1  day
//	7th       → +2  days
//	8th       → +4  days
//
// ±10% jitter is added to each interval so a Stripe Tax outage
// recovering at T+5min doesn't produce a thundering herd of every
// stuck invoice retrying within the same second.
func taxRetryBackoff(attempts int) time.Duration {
	schedule := []time.Duration{
		5 * time.Minute,
		15 * time.Minute,
		1 * time.Hour,
		4 * time.Hour,
		12 * time.Hour,
		24 * time.Hour,
		48 * time.Hour,
		96 * time.Hour,
	}
	idx := attempts
	if idx < 0 {
		idx = 0
	}
	if idx >= len(schedule) {
		idx = len(schedule) - 1
	}
	base := schedule[idx]
	// ±10% jitter. rand.Float64() is fine for this — we don't need
	// crypto randomness for backoff timing.
	jitter := time.Duration(float64(base) * (rand.Float64()*0.2 - 0.1))
	return base + jitter
}

// taxRetryableCodes lists the tax_error_code values the reconciler
// will retry. Codes outside this list are deliberately operator-
// action-required (auth, bad customer data, jurisdiction not
// registered, provider not connected) — automatic retry would
// burn provider quota with no chance of success.
//
// Kept as a function (not a const slice) so callers always pass a
// fresh slice into the SQL query — share-by-reference would let a
// pathological caller mutate the global.
func taxRetryableCodes() []string {
	return []string{
		string(tax.ErrCodeProviderOutage),
		string(tax.ErrCodeUnknown),
	}
}
