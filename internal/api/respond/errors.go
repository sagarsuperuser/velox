package respond

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

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

	case errors.Is(err, errs.ErrInvalidState), errors.Is(err, errs.ErrValidation):
		Validation(w, r, err.Error())

	default:
		// DomainError with an explicit code — treat as validation (these are
		// business-rule rejections like billing_setup_incomplete).
		if errs.Code(err) != "" {
			Validation(w, r, err.Error())
			return
		}

		// Legacy fallback: services that still return fmt.Errorf("... is
		// required") without wrapping errs.ErrValidation. This heuristic is
		// intentionally conservative (anchored prefixes, not substring) so
		// DB errors containing "invalid" don't get mis-mapped to 422 with
		// their raw message leaked to the client.
		//
		// TODO: migrate all service-layer validation sites to wrap
		// errs.ErrValidation, then delete this fallback. See
		// internal/errs/domain.go for the pattern.
		if looksLikeValidationError(err.Error()) {
			Validation(w, r, err.Error())
			return
		}

		slog.Error("unhandled error",
			"error", err,
			"resource", resource,
			"request_id", requestID(r),
		)
		InternalError(w, r)
	}
}

// looksLikeValidationError uses anchored prefixes (not substrings) to classify
// service-layer validation messages. The old substring match would catch DB
// errors like `pq: invalid input syntax for type uuid` and leak them as 422
// bodies. Prefix-anchoring keeps false positives to services that start their
// error message with these phrases — a narrow, intentional pattern.
func looksLikeValidationError(msg string) bool {
	prefixes := []string{
		"invalid ",
		"missing ",
		"cannot ",
		"can only ",
	}
	msgLower := strings.ToLower(msg)
	for _, p := range prefixes {
		if strings.HasPrefix(msgLower, p) {
			return true
		}
	}
	// Common "<field> is required" / "<field> must be ..." shape from Go services.
	substrings := []string{
		" is required",
		" must be ",
		" can only ",
		" at least ",
	}
	for _, s := range substrings {
		if strings.Contains(msgLower, s) {
			return true
		}
	}
	return false
}
