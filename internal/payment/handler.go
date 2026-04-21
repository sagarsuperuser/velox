package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/tenantstripe"
)

const (
	maxWebhookBodySize    = 65536 // 64KB
	signatureToleranceSec = 300   // 5 minutes
)

// EndpointResolver is the narrow surface the webhook handler needs from
// tenantstripe.Service. Exists so the handler can be tested without a DB.
type EndpointResolver interface {
	LookupEndpoint(ctx context.Context, endpointID string) (tenantstripe.EndpointLookup, error)
}

type Handler struct {
	stripe    *Stripe
	endpoints EndpointResolver
}

// NewHandler wires the Stripe webhook receiver. Credentials (signing secret,
// tenant id, livemode) are resolved per request via the endpoint id embedded
// in the URL path — there is no operator-level secret: each tenant registers
// their own webhook endpoint in their own Stripe dashboard pointing at
// /v1/webhooks/stripe/{endpoint_id}, where endpoint_id is the
// stripe_provider_credentials.id (vlx_spc_XXX) Velox handed them when they
// connected. Moving secrets off env vars is the whole point of the per-
// tenant model (see migration 0032).
func NewHandler(stripe *Stripe, endpoints EndpointResolver) *Handler {
	return &Handler{
		stripe:    stripe,
		endpoints: endpoints,
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/stripe/{endpoint_id}", h.handleStripeWebhook)
	return r
}

func (h *Handler) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	endpointID := chi.URLParam(r, "endpoint_id")
	if endpointID == "" {
		respond.NotFound(w, r, "webhook_endpoint")
		return
	}

	// Look up the tenant-owned endpoint BEFORE reading the body — a bogus URL
	// shouldn't even buffer the payload.
	lookup, err := h.endpoints.LookupEndpoint(r.Context(), endpointID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			// Tenant disconnected / never registered the webhook, or someone
			// is probing random endpoint ids. 404 either way — don't leak
			// the distinction.
			respond.NotFound(w, r, "webhook_endpoint")
			return
		}
		slog.ErrorContext(r.Context(), "webhook endpoint lookup failed",
			"endpoint_id", endpointID, "error", err)
		respond.InternalError(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodySize))
	if err != nil {
		respond.BadRequest(w, r, "failed to read body")
		return
	}

	sigHeader := r.Header.Get("Stripe-Signature")
	if err := verifyStripeSignature(body, sigHeader, lookup.WebhookSecret); err != nil {
		slog.WarnContext(r.Context(), "stripe webhook signature verification failed",
			"endpoint_id", endpointID,
			"tenant_id", lookup.TenantID,
			"reason", err.Error(),
		)
		respond.BadRequest(w, r, "invalid signature")
		return
	}

	// Parse the event
	var raw struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Created  int64  `json:"created"`
		Livemode bool   `json:"livemode"`
		Data     struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		respond.BadRequest(w, r, "invalid JSON")
		return
	}

	// The credentials row's livemode is authoritative — it's what the secret
	// was registered against. A tenant can't cross the streams by forging a
	// payload livemode because the HMAC above already rejected anything not
	// signed by the mode-specific secret.
	if raw.Livemode != lookup.Livemode {
		slog.WarnContext(r.Context(), "stripe webhook livemode mismatch",
			"endpoint_id", endpointID,
			"tenant_id", lookup.TenantID,
			"endpoint_mode", lookup.Livemode,
			"payload_mode", raw.Livemode,
			"stripe_event_id", raw.ID,
		)
		respond.BadRequest(w, r, "livemode mismatch")
		return
	}

	// Extract payment intent details from the event object
	var obj struct {
		ID        string `json:"id"`
		Object    string `json:"object"`
		Status    string `json:"status"`
		Amount    int64  `json:"amount"`
		Currency  string `json:"currency"`
		Customer  string `json:"customer"`
		LastError *struct {
			Message string `json:"message"`
		} `json:"last_payment_error"`
		Metadata map[string]string `json:"metadata"`
	}
	_ = json.Unmarshal(raw.Data.Object, &obj)

	// Defense in depth: velox_tenant_id in metadata should match the
	// endpoint's tenant. If they disagree, someone's Stripe account is
	// configured with the wrong endpoint — reject so we don't write one
	// tenant's webhook into another's invoice rows.
	metaTenant := obj.Metadata["velox_tenant_id"]
	if metaTenant != "" && metaTenant != lookup.TenantID {
		slog.WarnContext(r.Context(), "stripe webhook tenant mismatch",
			"endpoint_id", endpointID,
			"endpoint_tenant", lookup.TenantID,
			"metadata_tenant", metaTenant,
			"stripe_event_id", raw.ID,
		)
		respond.BadRequest(w, r, "tenant mismatch")
		return
	}

	if metaTenant == "" {
		// Not a Velox-originated payment intent (tenant fires their own
		// Stripe objects at the same endpoint). Acknowledge and skip.
		respond.JSON(w, r, http.StatusOK, map[string]string{"status": "skipped", "reason": "no velox metadata"})
		return
	}

	// Scrub Stripe's free-text failure message before it enters Velox data.
	// Persisted to stripe_webhook_events.failure_message and echoed into
	// invoices.last_payment_error via handlePaymentFailed — scrubbing at
	// ingress keeps PII (card last4, emails) out of both.
	failureMsg := ""
	if obj.LastError != nil {
		failureMsg = errs.Scrub(obj.LastError.Message)
	}

	amount := obj.Amount
	event := domain.StripeWebhookEvent{
		StripeEventID:      raw.ID,
		EventType:          raw.Type,
		ObjectType:         obj.Object,
		PaymentIntentID:    obj.ID,
		PaymentStatus:      obj.Status,
		AmountCents:        &amount,
		Currency:           obj.Currency,
		CustomerExternalID: obj.Customer,
		FailureMessage:     failureMsg,
		InvoiceID:          obj.Metadata["velox_invoice_id"],
		Payload:            map[string]any{"raw": string(body)},
		OccurredAt:         time.Unix(raw.Created, 0).UTC(),
		Livemode:           lookup.Livemode,
	}

	// Stage ctx with the endpoint's tenant + livemode before handing off —
	// adapter DB writes read ctx tenant/livemode to satisfy RLS policies.
	ctx := auth.WithTenantID(r.Context(), lookup.TenantID)
	ctx = postgres.WithLivemode(ctx, lookup.Livemode)

	if err := h.stripe.HandleWebhook(ctx, lookup.TenantID, event); err != nil {
		slog.ErrorContext(ctx, "webhook processing failed",
			"stripe_event_id", raw.ID,
			"event_type", raw.Type,
			"tenant_id", lookup.TenantID,
			"error", err,
		)
		// Return 200 anyway — Stripe will retry on 5xx and we don't want infinite retries
		// for events we can't process (e.g., missing invoice). Log the error server-side;
		// don't echo raw error text to the caller (may leak internal details).
		respond.JSON(w, r, http.StatusOK, map[string]string{"status": "error_logged"})
		return
	}

	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "processed"})
}

// verifyStripeSignature verifies the Stripe-Signature header using HMAC-SHA256.
// Stripe signature format: t=timestamp,v1=signature
func verifyStripeSignature(payload []byte, sigHeader, secret string) error {
	if sigHeader == "" {
		return fmt.Errorf("missing Stripe-Signature header")
	}

	var timestamp string
	var signatures []string

	for _, part := range strings.Split(sigHeader, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			signatures = append(signatures, kv[1])
		}
	}

	if timestamp == "" || len(signatures) == 0 {
		return fmt.Errorf("invalid signature format")
	}

	// Check timestamp tolerance
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp")
	}
	if abs(time.Now().Unix()-ts) > signatureToleranceSec {
		return fmt.Errorf("timestamp outside tolerance")
	}

	// Compute expected signature: HMAC-SHA256(timestamp + "." + payload)
	signedPayload := timestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	expected := hex.EncodeToString(mac.Sum(nil))

	for _, sig := range signatures {
		if hmac.Equal([]byte(sig), []byte(expected)) {
			return nil
		}
	}

	return fmt.Errorf("signature mismatch")
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
