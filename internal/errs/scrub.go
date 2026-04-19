package errs

import "regexp"

// Scrub removes PII from free-text error messages before they're persisted
// (invoices.last_payment_error, stripe_webhook_events.failure_message) or
// logged. Scrubbing at the ingress means every downstream surface — DB
// columns, slog fields, HTTP error bodies — is automatically safe without
// each callsite having to remember.
//
// What we redact, and why:
//
//   - Card last4 embedded in free text ("ending in 4242") → the last4 is
//     stored structurally on customer_payment_setups; leaving it inline in
//     error text turns every error row into a second copy outside the
//     encryption-at-rest path and outside the operator's access-control
//     model.
//   - Raw card-number-like digit runs (13–19 contiguous digits) → PCI.
//     Should never reach us, but a belt-and-braces redaction costs nothing.
//   - Email addresses → PII. We store emails on customers/billing_profiles
//     where access is audited; an email in an error string bypasses that.
//
// What we intentionally do NOT redact:
//
//   - Stripe object IDs (pi_*, cus_*, pm_*, ch_*, seti_*, in_*) — these
//     are opaque correlation identifiers, not PII. Operators need them to
//     trace an incident in the Stripe dashboard. Stripe itself logs them
//     in every webhook and API response.
//   - Decline codes ("card_declined", "insufficient_funds") — machine-
//     readable, no PII, and critical for triage.
//
// Scrub is idempotent: Scrub(Scrub(s)) == Scrub(s).
func Scrub(s string) string {
	if s == "" {
		return s
	}
	s = reCardLast4.ReplaceAllString(s, "$1****")
	s = reCardNumber.ReplaceAllString(s, "[REDACTED_CARD]")
	s = reEmail.ReplaceAllString(s, "[REDACTED_EMAIL]")
	return s
}

// reCardLast4 matches "ending in 4242" / "ending 4242" / "card 4242" with
// the digits captured separately so we preserve the surrounding phrase for
// operator readability. Case-insensitive; the prefix is kept verbatim.
var reCardLast4 = regexp.MustCompile(`(?i)((?:ending(?:\s+in)?|card)\s+)\d{4}\b`)

// reCardNumber matches a raw 13–19 digit run that isn't part of a longer
// digit sequence. Bounded by non-digit (or start/end) to avoid chewing up
// Stripe IDs that happen to contain digits (pi_3M… etc. — the underscore
// separates the prefix so this regex won't match them anyway, but the
// word boundary is the belt to the suspenders).
var reCardNumber = regexp.MustCompile(`\b\d{13,19}\b`)

// reEmail matches RFC-5322-ish addresses. Deliberately simple: we're
// redacting, not parsing for delivery, so we'd rather over-match than
// leave anything behind. Anchored by non-word boundaries so "x@y.z"
// inside a sentence still gets caught.
var reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
