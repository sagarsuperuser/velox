package dunning

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// fakeInvoiceUpdater is the dunning handler's InvoiceUpdater (Get is the only
// method resolveRun's manual-resolve branch reads — for the inline
// credit-reversal/PI-cancel inputs).
type fakeInvoiceUpdater struct{ inv domain.Invoice }

func (f *fakeInvoiceUpdater) Get(_ context.Context, _, _ string) (domain.Invoice, error) {
	return f.inv, nil
}
func (f *fakeInvoiceUpdater) UpdateStatus(_ context.Context, _, _ string, _ domain.InvoiceStatus) (domain.Invoice, error) {
	return f.inv, nil
}
func (f *fakeInvoiceUpdater) UpdatePayment(_ context.Context, _, _ string, _ domain.InvoicePaymentStatus, _, _ string, _ *time.Time) (domain.Invoice, error) {
	return f.inv, nil
}
func (f *fakeInvoiceUpdater) MarkPaid(_ context.Context, _, _, _ string, _ time.Time) (domain.Invoice, error) {
	return f.inv, nil
}

type recordingVoider struct {
	calls int
	err   error
}

func (v *recordingVoider) Void(_ context.Context, tenantID, id string) (domain.Invoice, error) {
	v.calls++
	if v.err != nil {
		return domain.Invoice{}, v.err
	}
	return domain.Invoice{ID: id, TenantID: tenantID, Status: domain.InvoiceVoided}, nil
}

type recordingReverser struct{ calls int }

func (r *recordingReverser) ReverseForInvoice(_ context.Context, _, _, _, _ string) (int64, error) {
	r.calls++
	return 0, nil
}

type recordingCanceler struct{ calls int }

func (c *recordingCanceler) CancelPaymentIntent(_ context.Context, _ string) error {
	c.calls++
	return nil
}

func resolveManually(t *testing.T, h *Handler, runID string) {
	t.Helper()
	body, _ := json.Marshal(resolveInput{Resolution: string(domain.ResolutionManuallyResolved)})
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID)
	ctx := context.WithValue(auth.WithTenantID(r.Context(), "t1"), chi.RouteCtxKey, rctx)
	r = r.WithContext(ctx)
	h.resolveRun(httptest.NewRecorder(), r)
}

// TestResolveRun_ManualVoid_RoutesThroughServiceAndGatesSideEffects pins D5
// (single void writer, ADR-059): a manually-resolved dunning run voids the
// invoice through invoice.Service.Void (which reverses tax + emits the
// single-writer invoice.voided event + enforces the in-flight guard) instead of
// the raw store. The inline credit-reversal + PI-cancel must run ONLY when the
// void SUCCEEDS — otherwise an in-flight invoice (whose void the service refuses)
// would still get its live PaymentIntent canceled and its credits reversed,
// defeating the in-flight guard.
func TestResolveRun_ManualVoid_RoutesThroughServiceAndGatesSideEffects(t *testing.T) {
	ctx := context.Background()
	newHandler := func(voider *recordingVoider) (*Handler, *recordingReverser, *recordingCanceler, string) {
		svc := NewService(newMemStore(), &noopRetrier{}, nil)
		run, err := svc.StartDunning(ctx, "t1", "inv_1", "cus_1", time.Now())
		if err != nil {
			t.Fatalf("StartDunning: %v", err)
		}
		reverser := &recordingReverser{}
		canceler := &recordingCanceler{}
		h := NewHandler(svc, HandlerDeps{
			Invoices: &fakeInvoiceUpdater{inv: domain.Invoice{
				ID: "inv_1", TenantID: "t1", CustomerID: "cus_1", StripePaymentIntentID: "pi_1",
			}},
			CreditReverser: reverser,
			PaymentCancel:  canceler,
		})
		h.SetInvoiceVoider(voider)
		return h, reverser, canceler, run.ID
	}

	t.Run("void succeeds → routes through service voider + runs inline side-effects", func(t *testing.T) {
		voider := &recordingVoider{}
		h, reverser, canceler, runID := newHandler(voider)
		resolveManually(t, h, runID)
		if voider.calls != 1 {
			t.Errorf("void must route through the invoice service voider; calls=%d, want 1", voider.calls)
		}
		if reverser.calls != 1 {
			t.Errorf("credit reversal should run after a successful void; calls=%d, want 1", reverser.calls)
		}
		if canceler.calls != 1 {
			t.Errorf("PI cancel should run after a successful void; calls=%d, want 1", canceler.calls)
		}
	})

	t.Run("void refused (in-flight) → inline PI-cancel + credit-reversal SKIPPED", func(t *testing.T) {
		voider := &recordingVoider{err: errs.InvalidState("a charge is in flight on this invoice")}
		h, reverser, canceler, runID := newHandler(voider)
		resolveManually(t, h, runID)
		if voider.calls != 1 {
			t.Errorf("voider should be attempted once; calls=%d", voider.calls)
		}
		if reverser.calls != 0 {
			t.Errorf("credit reversal MUST NOT run when the void was refused (in-flight guard); calls=%d, want 0", reverser.calls)
		}
		if canceler.calls != 0 {
			t.Errorf("PI cancel MUST NOT run when the void was refused — would cancel a live in-flight charge; calls=%d, want 0", canceler.calls)
		}
	})
}
