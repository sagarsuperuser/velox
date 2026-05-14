package errs

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// SafeMessageError is the opt-in marker for error types that own their
// own operator-safe rendering. ADR-026.
//
// Any error type that wraps an upstream-categorized message already
// curated for the operator (Stripe card declines with decline_code,
// classified tax provider failures, etc.) implements this interface
// to bypass the default-generic sanitization. SanitizeForOperator
// detects via errors.As and surfaces OperatorSafeMessage() when
// present.
//
// NEVER implement this on errors that include raw provider strings,
// DB internals, panic stacks, or anything operator-unsafe — that's
// exactly what the boundary sanitizer is meant to suppress.
//
// This interface previously lived in internal/api/respond. Promoted
// to internal/errs (2026-05-08) so non-HTTP contexts — catchup
// orchestrator, scheduler error rollups, audit-log writers — can
// reuse the same dispatch shape. respond.SafeMessageError is now an
// alias of errs.SafeMessageError for backward compatibility.
type SafeMessageError interface {
	error
	OperatorSafeMessage() string
}

// genericInternalError is the operator-grade fallback when an error
// has no SafeMessageError implementation and contains internal-only
// markers. The request_id lets support trace the full diagnostic in
// slog. Capitalized first word is intentional — this string lands in
// dashboard banners and toast bodies.
func genericInternalError(requestID string) string {
	if requestID == "" {
		return "An internal error occurred. See server logs for details."
	}
	return fmt.Sprintf("An internal error occurred. Reference: %s", requestID)
}

// SanitizeForOperator returns an operator-safe rendering of err for
// any boundary where the error message will land in the dashboard,
// be persisted to a column the dashboard reads, or be returned in an
// HTTP body. ADR-026.
//
// Dispatch:
//
//  1. If err (or any wrapped error in its chain) implements
//     SafeMessageError → return OperatorSafeMessage(). The error
//     type opted into curated rendering.
//  2. Else, scan the message for internal-only markers — SQLSTATE
//     codes, Go file:line frames, panic markers, raw stack traces.
//     If any is present, return the generic "internal error
//     reference: REQ-XYZ" message; the full diagnostic stays in
//     slog (caller's responsibility) for support to trace.
//  3. Otherwise, the message is presumed operator-safe (Stripe text,
//     domain-validation errors with curated copy, etc.) — pass it
//     through after PII redaction via Scrub.
//
// The third branch is conservative on purpose: there's no whitelist
// of "known safe" patterns. The marker detection in step 2 is the
// load-bearing filter — anything matching a SQLSTATE / file:line /
// panic shape is internal by construction. Tests pin the marker set.
func SanitizeForOperator(err error, requestID string) string {
	if err == nil {
		return ""
	}
	var sme SafeMessageError
	if errors.As(err, &sme) {
		return Scrub(sme.OperatorSafeMessage())
	}
	msg := err.Error()
	if hasInternalMarkers(msg) {
		return genericInternalError(requestID)
	}
	return Scrub(msg)
}

// SummarizeForOperator builds a concise operator-grade summary of a
// batch of errors — typically the per-row errors from a multi-row
// catchup phase or scheduler tick. Returns "" when errs is empty.
//
// Shape of the output, by category:
//
//   - Each error implementing SafeMessageError: included verbatim
//     after Scrub, joined with "; ".
//   - All others: counted; appears as "N internal error(s)" with
//     the request_id reference.
//
// The full diagnostic for every error stays in slog (caller's job —
// SummarizeForOperator never logs; that's a separate concern).
//
// Examples:
//
//	SummarizeForOperator([err1_safe, err2_internal, err3_internal], "REQ-1")
//	  → "stripe tax: customer has no country on billing profile; 2 internal error(s). Reference: REQ-1"
//
//	SummarizeForOperator([err_internal, err_internal], "REQ-1")
//	  → "2 internal error(s). Reference: REQ-1"
//
//	SummarizeForOperator([err_safe], "")
//	  → "stripe tax: customer has no country on billing profile"
func SummarizeForOperator(errs []error, requestID string) string {
	if len(errs) == 0 {
		return ""
	}
	var safe []string
	internal := 0
	for _, e := range errs {
		if e == nil {
			continue
		}
		var sme SafeMessageError
		if errors.As(e, &sme) {
			safe = append(safe, Scrub(sme.OperatorSafeMessage()))
			continue
		}
		msg := e.Error()
		if hasInternalMarkers(msg) {
			internal++
			continue
		}
		// Plain message, no internal markers — operator-safe by
		// default, pass through after PII scrub.
		safe = append(safe, Scrub(msg))
	}
	parts := safe
	if internal > 0 {
		ref := ""
		if requestID != "" {
			ref = fmt.Sprintf(". Reference: %s", requestID)
		}
		parts = append(parts, fmt.Sprintf("%d internal error(s)%s", internal, ref))
	}
	return strings.Join(parts, "; ")
}

// hasInternalMarkers reports whether the message contains any pattern
// that's only meaningful to engineers / support, never to operators —
// SQLSTATE codes, Go panic markers, source file:line references,
// stack-trace frame markers. Centralized so SanitizeForOperator and
// SummarizeForOperator stay in sync.
func hasInternalMarkers(msg string) bool {
	return reSQLState.MatchString(msg) ||
		reGoFileLine.MatchString(msg) ||
		strings.Contains(msg, "runtime error:") ||
		strings.Contains(msg, "panic:") ||
		strings.Contains(msg, "goroutine ")
}

// reSQLState matches Postgres/database SQLSTATE codes embedded in error
// strings — "(SQLSTATE 42702)", "SQLSTATE: 23505", etc.
var reSQLState = regexp.MustCompile(`\bSQLSTATE\s*[:= ]?\s*[0-9A-Z]{5}\b`)

// reGoFileLine matches Go-style file:line frames (e.g.
// "/Users/.../engine.go:1234"). Indicates a stack frame leaked into
// an error string. Anchored on the .go: suffix so we don't false-
// match on UNIX paths in user-facing messages.
var reGoFileLine = regexp.MustCompile(`\.go:\d+\b`)
