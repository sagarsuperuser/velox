package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ValidationError represents a Stripe-style field validation error.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// ValidationErrors collects multiple field errors.
type ValidationErrors struct {
	Errors []ValidationError `json:"errors"`
}

func (v *ValidationErrors) Add(field, message, code string) {
	v.Errors = append(v.Errors, ValidationError{
		Field:   field,
		Message: message,
		Code:    code,
	})
}

func (v *ValidationErrors) HasErrors() bool {
	return len(v.Errors) > 0
}

// WriteResponse writes validation errors as a Stripe-style error response.
func (v *ValidationErrors) WriteResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"type":    "validation_error",
			"message": v.summary(),
			"errors":  v.Errors,
		},
	})
}

func (v *ValidationErrors) summary() string {
	if len(v.Errors) == 1 {
		return v.Errors[0].Message
	}
	return fmt.Sprintf("%d validation errors", len(v.Errors))
}

// Common validators

func RequireString(v *ValidationErrors, field, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		v.Add(field, field+" is required", "required")
	}
	return value
}

func RequirePositive(v *ValidationErrors, field string, value int64) {
	if value <= 0 {
		v.Add(field, field+" must be positive", "invalid")
	}
}

func RequireOneOf(v *ValidationErrors, field, value string, allowed []string) {
	for _, a := range allowed {
		if value == a {
			return
		}
	}
	v.Add(field, fmt.Sprintf("%s must be one of: %s", field, strings.Join(allowed, ", ")), "invalid")
}
