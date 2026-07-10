package invoice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// outcomeNoPMNotifier returns a fixed NotifyOutcome so the endpoint tests can
// drive both dispositions without a real email path.
type outcomeNoPMNotifier struct {
	outcome domain.NotifyOutcome
	calls   int
}

func (f *outcomeNoPMNotifier) NotifyNoPaymentMethod(_ context.Context, _ string, _ domain.Invoice) (domain.NotifyOutcome, error) {
	f.calls++
	return f.outcome, nil
}

// TestResendSetupLink_HonestDispositions pins the typed-outcome contract on
// POST /{id}/resend-setup-link. Pre-fix the notifier's no-email skip was a
// silent nil, so the endpoint answered 200 {"status":"sent"} for a send that
// never happened — the operator got a success toast and waited on an email
// with no recipient. Now: sent -> 200; skipped_no_email -> typed 409 telling
// the operator what actually works (add an email / copy the link).
func TestResendSetupLink_HonestDispositions(t *testing.T) {
	ctx := context.Background()
	newReq := func(t *testing.T, invID string) (*http.Request, *httptest.ResponseRecorder) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/"+invID+"/resend-setup-link", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", invID)
		reqCtx := context.WithValue(req.Context(), auth.TestTenantIDKey(), "t1")
		reqCtx = context.WithValue(reqCtx, chi.RouteCtxKey, rctx)
		return req.WithContext(reqCtx), httptest.NewRecorder()
	}
	seed := func(t *testing.T, store *memStore) domain.Invoice {
		t.Helper()
		inv, err := store.Create(ctx, "t1", domain.Invoice{
			InvoiceNumber: "INV-100", Status: domain.InvoiceFinalized,
			PaymentStatus: domain.PaymentPending, AmountDueCents: 5000,
			CustomerID: "cus_1", Currency: "USD",
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		return inv
	}

	t.Run("sent -> 200", func(t *testing.T) {
		store := newMemStore()
		inv := seed(t, store)
		notifier := &outcomeNoPMNotifier{outcome: domain.NotifySent}
		h := &Handler{svc: NewService(store, nil, nil), noPMNotifier: notifier}
		req, rr := newReq(t, inv.ID)
		h.resendSetupLink(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
		}
		if notifier.calls != 1 {
			t.Errorf("notifier calls = %d, want 1", notifier.calls)
		}
	})

	t.Run("skipped_no_email -> typed 409, never a false success", func(t *testing.T) {
		store := newMemStore()
		inv := seed(t, store)
		notifier := &outcomeNoPMNotifier{outcome: domain.NotifySkippedNoEmail}
		h := &Handler{svc: NewService(store, nil, nil), noPMNotifier: notifier}
		req, rr := newReq(t, inv.ID)
		h.resendSetupLink(rr, req)
		if rr.Code != http.StatusConflict {
			t.Fatalf("status: got %d, want 409. body=%s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		if !strings.Contains(body, "no_email_on_file") {
			t.Errorf("expected typed code no_email_on_file, got %s", body)
		}
		if strings.Contains(body, `"status":"sent"`) {
			t.Errorf("must never claim sent on a skip, got %s", body)
		}
	})
}
