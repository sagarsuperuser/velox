package respond

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/sagarsuperuser/velox/internal/errs"
)

// FromError translates a service/store error into the appropriate HTTP response.
// This is the single place where domain errors become API responses.
//
// Usage in handlers:
//
//	customer, err := h.svc.Create(ctx, tenantID, input)
//	if err != nil {
//	    respond.FromError(w, r, err, "customer")
//	    return
//	}
func FromError(w http.ResponseWriter, r *http.Request, err error, resource string) {
	if err == nil {
		return
	}

	switch {
	case errors.Is(err, errs.ErrNotFound):
		NotFound(w, r, resource)

	case errors.Is(err, errs.ErrAlreadyExists):
		Conflict(w, r, err.Error())

	case errors.Is(err, errs.ErrDuplicateKey):
		Conflict(w, r, err.Error())

	case errors.Is(err, errs.ErrInvalidState):
		Validation(w, r, err.Error())

	default:
		// Check for DomainError with a code
		code := errs.Code(err)
		if code != "" {
			Validation(w, r, err.Error())
			return
		}

		// Check if it's a validation-style error (contains common validation words)
		msg := err.Error()
		if isValidationError(msg) {
			Validation(w, r, msg)
			return
		}

		// Unknown error — log and return 500
		slog.Error("unhandled error",
			"error", err,
			"resource", resource,
			"request_id", requestID(r),
		)
		InternalError(w, r)
	}
}

func isValidationError(msg string) bool {
	// Errors that contain these words are validation errors, not internal errors.
	// This catches fmt.Errorf("customer_id is required") style errors from services.
	patterns := []string{
		"is required",
		"must be",
		"cannot",
		"can only",
		"invalid",
		"already",
		"same as",
		"at least",
		"maximum",
	}
	for _, p := range patterns {
		if containsCI(msg, p) {
			return true
		}
	}
	return false
}

func containsCI(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a != b && a != b+32 && a != b-32 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
