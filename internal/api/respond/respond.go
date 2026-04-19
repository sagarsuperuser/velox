package respond

import (
	"encoding/json"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"
)

const apiVersion = "2026-04-07"

// JSON writes a success response with standard headers.
func JSON(w http.ResponseWriter, r *http.Request, status int, data any) {
	setHeaders(w, r)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// List writes a paginated list response.
func List(w http.ResponseWriter, r *http.Request, data any, total int) {
	JSON(w, r, http.StatusOK, map[string]any{
		"data":  data,
		"total": total,
	})
}

// Error writes a Stripe-style error response.
// Format: {"error": {"type": "...", "message": "...", "code": "...", "param": "..."}}
func Error(w http.ResponseWriter, r *http.Request, status int, errType, code, message string) {
	errorField(w, r, status, errType, code, "", message)
}

// errorField is the underlying writer that includes the optional field name in
// the envelope's Param slot. All shortcut helpers funnel through here.
func errorField(w http.ResponseWriter, r *http.Request, status int, errType, code, field, message string) {
	setHeaders(w, r)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorBody{
		Error: ErrorDetail{
			Type:      errType,
			Code:      code,
			Message:   message,
			Param:     field,
			RequestID: requestID(r),
		},
	})
}

// Common error shortcuts

func BadRequest(w http.ResponseWriter, r *http.Request, message string) {
	Error(w, r, http.StatusBadRequest, "invalid_request_error", "invalid_request", message)
}

func NotFound(w http.ResponseWriter, r *http.Request, resource string) {
	Error(w, r, http.StatusNotFound, "invalid_request_error", "not_found", resource+" not found")
}

func Conflict(w http.ResponseWriter, r *http.Request, message string) {
	errorField(w, r, http.StatusConflict, "invalid_request_error", "already_exists", "", message)
}

// ConflictField writes a 409 response with the offending field name in the
// envelope's Param slot so the frontend can attach the message to the right
// input. Use when a create/update fails because a user-supplied value
// collides with an existing record (plan.code, coupon.code, etc.).
func ConflictField(w http.ResponseWriter, r *http.Request, field, message string) {
	errorField(w, r, http.StatusConflict, "invalid_request_error", "already_exists", field, message)
}

func Validation(w http.ResponseWriter, r *http.Request, message string) {
	errorField(w, r, http.StatusUnprocessableEntity, "invalid_request_error", "validation_error", "", message)
}

// ValidationField writes a 422 response with the offending field name in the
// envelope's Param slot. Use for inline handler validation where the field is
// known at the call site (service-layer validation is routed via FromError,
// which pulls the field off DomainError automatically).
func ValidationField(w http.ResponseWriter, r *http.Request, field, message string) {
	errorField(w, r, http.StatusUnprocessableEntity, "invalid_request_error", "validation_error", field, message)
}

func InternalError(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusInternalServerError, "api_error", "internal_error", "an internal error occurred")
}

func Unauthorized(w http.ResponseWriter, r *http.Request, message string) {
	Error(w, r, http.StatusUnauthorized, "authentication_error", "unauthorized", message)
}

func Forbidden(w http.ResponseWriter, r *http.Request, message string) {
	Error(w, r, http.StatusForbidden, "authentication_error", "forbidden", message)
}

func RateLimited(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusTooManyRequests, "rate_limit_error", "rate_limited", "too many requests — please retry after the rate limit resets")
}

// Types

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Type      string `json:"type"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Param     string `json:"param,omitempty"`
}

func setHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Velox-Version", apiVersion)
	if id := requestID(r); id != "" {
		w.Header().Set("Velox-Request-Id", id)
	}
}

func requestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	return chimw.GetReqID(r.Context())
}
