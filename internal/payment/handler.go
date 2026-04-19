package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

const (
	maxWebhookBodySize    = 65536 // 64KB
	signatureToleranceSec = 300   // 5 minutes
)

type Handler struct {
	stripe        *Stripe
	webhookSecret string // Stripe webhook signing secret
}

func NewHandler(stripe *Stripe, webhookSecret string) *Handler {
	return &Handler{stripe: stripe, webhookSecret: webhookSecret}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/stripe", h.handleStripeWebhook)
	return r
}

func (h *Handler) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodySize))
	if err != nil {
		respond.BadRequest(w, r, "failed to read body")
		return
	}

	// Verify Stripe signature
	sigHeader := r.Header.Get("Stripe-Signature")
	if h.webhookSecret != "" {
		if err := verifyStripeSignature(body, sigHeader, h.webhookSecret); err != nil {
			slog.Warn("stripe webhook signature verification failed", "error", err)
			respond.BadRequest(w, r, "invalid signature")
			return
		}
	}

	// Parse the event
	var raw struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Created int64  `json:"created"`
		Data    struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		respond.BadRequest(w, r, "invalid JSON")
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

	// Determine tenant from metadata
	tenantID := obj.Metadata["velox_tenant_id"]
	if tenantID == "" {
		// Not a Velox-originated payment intent — acknowledge but skip
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
	}

	if err := h.stripe.HandleWebhook(r.Context(), tenantID, event); err != nil {
		slog.Error("webhook processing failed",
			"stripe_event_id", raw.ID,
			"event_type", raw.Type,
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

