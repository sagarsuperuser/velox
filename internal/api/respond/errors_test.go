package respond

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sagarsuperuser/velox/internal/errs"
)

func TestFromError_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	FromError(rec, req, errs.ErrNotFound, "customer")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
	body := decodeError(t, rec)
	if body.Code != "not_found" {
		t.Errorf("code: got %q", body.Code)
	}
}

func TestFromError_AlreadyExists(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", nil)

	FromError(rec, req, fmt.Errorf("%w: external_id dup", errs.ErrAlreadyExists), "customer")

	if rec.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", rec.Code)
	}
}

func TestFromError_DuplicateKey(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", nil)

	FromError(rec, req, fmt.Errorf("%w: idempotency_key", errs.ErrDuplicateKey), "usage_event")

	if rec.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", rec.Code)
	}
}

func TestFromError_ValidationMessage(t *testing.T) {
	cases := []string{
		"customer_id is required",
		"quantity must be positive",
		"can only activate draft subscriptions",
		"cannot void a paid invoice",
		"invalid billing_interval",
	}

	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/", nil)

			FromError(rec, req, fmt.Errorf("%s", msg), "resource")

			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("'%s' → status: got %d, want 422", msg, rec.Code)
			}
		})
	}
}

func TestFromError_DomainError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", nil)

	err := errs.New("billing_setup_incomplete", "stripe connection required")
	FromError(rec, req, err, "billing")

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want 422", rec.Code)
	}
}

func TestFromError_UnknownError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	FromError(rec, req, fmt.Errorf("database connection timed out"), "resource")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", rec.Code)
	}
}

func TestFromError_Nil(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	FromError(rec, req, nil, "resource")

	if rec.Code != http.StatusOK {
		t.Errorf("nil error should not write response, got %d", rec.Code)
	}
}

type errorResponse struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorResponse {
	t.Helper()
	var body struct {
		Error errorResponse `json:"error"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	return body.Error
}
