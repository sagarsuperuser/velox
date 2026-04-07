package errs

import (
	"errors"
	"fmt"
)

// DomainError is a structured error with a machine-readable code and a
// human-readable message. The API layer translates these into HTTP responses
// by matching on Code rather than string content.
type DomainError struct {
	Code    string
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

// Sentinel errors for store layer.
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrDuplicateKey  = errors.New("duplicate key")
	ErrInvalidState  = errors.New("invalid state")
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
