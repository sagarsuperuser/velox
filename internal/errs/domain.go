package errs

import (
	"errors"
	"fmt"
)

// DomainError is a structured error carrying both machine-readable metadata
// (Kind for HTTP status routing, Code for business-rule codes, Field for the
// offending form input) and a human-readable Message. The API layer translates
// these into HTTP responses by matching on Kind first, then pulling Field onto
// the response envelope so the frontend can route the message to the right
// input. Cause is optional and used only for internal/log context.
type DomainError struct {
	Kind    error
	Code    string
	Field   string
	Message string
	Cause   error
}

func (e *DomainError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *DomainError) Unwrap() error { return e.Cause }

// Is lets errors.Is match against this error's Kind sentinel. This is how
// respond.FromError routes DomainError{Kind: ErrValidation} to a 422 response
// without the caller needing to wrap manually.
func (e *DomainError) Is(target error) bool {
	if e.Kind == nil {
		return false
	}
	return e.Kind == target
}

func New(code, message string) *DomainError {
	return &DomainError{Code: code, Message: message}
}

func Wrap(cause error, code, message string) *DomainError {
	return &DomainError{Code: code, Message: message, Cause: cause}
}

func Code(err error) string {
	var de *DomainError
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// Field extracts the offending field name from a DomainError anywhere in the
// error chain, or returns "". Used by respond.FromError to populate
// ErrorDetail.Param on the response envelope.
func Field(err error) string {
	var de *DomainError
	if errors.As(err, &de) {
		return de.Field
	}
	return ""
}

// Required returns a validation error indicating a required field is missing.
// Maps to HTTP 422 with Param=field so the client can highlight the input.
//
//	return errs.Required("external_id")
func Required(field string) *DomainError {
	return &DomainError{
		Kind:    ErrValidation,
		Field:   field,
		Message: field + " is required",
	}
}

// Invalid returns a validation error for a field that was provided but failed
// a semantic check (bad format, out of range, etc.). The reason is shown to
// the user verbatim — phrase it as a complete sentence fragment that reads
// naturally under the field, e.g. "must be a valid email address".
//
//	return errs.Invalid("company_email", "must contain @ and a domain")
func Invalid(field, reason string) *DomainError {
	return &DomainError{
		Kind:    ErrValidation,
		Field:   field,
		Message: reason,
	}
}

// AlreadyExists marks a duplicate resource, optionally naming the offending
// field. Maps to HTTP 409. Use when a user-submitted value collides with an
// existing record (customer.external_id, plan.code, coupon.code, etc.).
//
//	return errs.AlreadyExists("code", fmt.Sprintf("plan code %q already exists", code))
func AlreadyExists(field, message string) *DomainError {
	return &DomainError{
		Kind:    ErrAlreadyExists,
		Field:   field,
		Message: message,
	}
}

// InvalidState rejects an operation because the target resource is in a state
// that disallows it (e.g. "can only activate draft subscriptions"). Maps to
// HTTP 422. These errors are not about a specific form field — the user
// usually needs a toast or inline banner, not a field highlight.
//
//	return errs.InvalidState("can only activate draft subscriptions")
func InvalidState(message string) *DomainError {
	return &DomainError{
		Kind:    ErrInvalidState,
		Message: message,
	}
}

// Sentinel errors for store and service layers.
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrDuplicateKey  = errors.New("duplicate key")
	ErrInvalidState  = errors.New("invalid state")
	// ErrValidation marks an error caused by bad caller input (missing field,
	// malformed value). New code should prefer errs.Required / errs.Invalid /
	// errs.AlreadyExists which set Field metadata for the UI. The legacy
	// wrapper form still works:
	//
	//   return fmt.Errorf("%w: customer_id is required", errs.ErrValidation)
	//
	// but leaves the frontend with only a toast — no field highlight.
	ErrValidation = errors.New("validation error")
)

// Error code constants.
const (
	CodeBillingSetupIncomplete = "billing_setup_incomplete"
	CodeStripeRequired         = "stripe_credentials_required"
	CodeInvoiceFetchFailed     = "invoice_fetch_failed"
	CodePaymentRetryFailed     = "payment_retry_failed"
	CodeSubscriptionInvalid    = "subscription_invalid"
	CodePricingUnsupported     = "pricing_unsupported"
	CodeUsageSyncFailed        = "usage_sync_failed"
	CodeAdapterNotConfigured   = "adapter_not_configured"
)
