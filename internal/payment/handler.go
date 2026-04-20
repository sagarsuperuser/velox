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
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

const (
	maxWebhookBodySize    = 65536 // 64KB
	signatureToleranceSec = 300   // 5 minutes
)

type Handler struct {
	stripe            *Stripe
	webhookSecretLive string // Stripe live-mode signing secret
	webhookSecretTest string // Stripe test-mode signing secret (optional)
	// allowUnsigned lets the handler accept POSTs with NO configured secret.
	// Only set by callers who explicitly opted in via
	// ALLOW_UNSIGNED_STRIPE_WEBHOOKS=1 in local env. Production/staging
	// configs fail startup before we ever reach here when no secret is set,
	// so in those envs this field is always false.
	allowUnsigned bool
}

// NewHandler accepts both the live and test Stripe webhook signing secrets.
// Dispatch between modes happens per-event at verification time: we accept
// either secret, and the event's own livemode field decides which downstream
// context to process it under. Passing "" for the test secret disables
// test-mode webhook intake (live-only deployment).
//
// allowUnsigned opts this handler into the "no secrets configured" path —
// only honored when at least one caller (config.Load in local env with
// ALLOW_UNSIGNED_STRIPE_WEBHOOKS=1) has explicitly said "yes, accept
// unsigned". Any non-local deployment should pass false; config.validateFatal
// enforces that a secret is configured before NewHandler ever runs there.
func NewHandler(stripe *Stripe, webhookSecretLive, webhookSecretTest string, allowUnsigned bool) *Handler {
	return &Handler{
		stripe:            stripe,
		webhookSecretLive: webhookSecretLive,
		webhookSecretTest: webhookSecretTest,
		allowUnsigned:     allowUnsigned,
	}
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

	// Verify Stripe signature against both (live, test) secrets. The event
	// carries its own livemode field, but we must NOT trust untrusted payload
	// data — so we verify the signature first against whichever secret
	// accepts it, then use the matched secret's mode as the authoritative
	// classification. Falling back gracefully if the operator only has one
	// secret configured mirrors NewStripeClients' tolerance.
	sigHeader := r.Header.Get("Stripe-Signature")
	eventLivemode, ok := verifyWebhookDualSecret(body, sigHeader, h.webhookSecretLive, h.webhookSecretTest, h.allowUnsigned)
	if !ok {
		slog.Warn("stripe webhook signature verification failed",
			"live_secret_set", h.webhookSecretLive != "",
			"test_secret_set", h.webhookSecretTest != "",
			"allow_unsigned", h.allowUnsigned,
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

	// Signature-verified livemode takes precedence over the payload's self-
	// declared livemode. If they disagree, the event is malformed or was
	// signed with the wrong secret — reject to avoid processing a test event
	// under live tenancy or vice versa.
	if raw.Livemode != eventLivemode {
		slog.Warn("stripe webhook livemode mismatch",
			"signature_mode", eventLivemode,
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

	// Stage the ctx with the verified livemode before handing off to the
	// adapter — the adapter's DB writes go through BeginTx which reads
	// ctx livemode to set app.livemode on the session.
	ctx := postgres.WithLivemode(r.Context(), eventLivemode)
	event.Livemode = eventLivemode
	if err := h.stripe.HandleWebhook(ctx, tenantID, event); err != nil {
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

// verifyWebhookDualSecret tries to verify the signature against each provided
// secret in turn. Returns the livemode implied by the matched secret (true
// for live, false for test) and ok=true on any match. When both secrets are
// empty and allowUnsigned=false (the default / non-local deployment path)
// the verifier refuses to match — a deployment that forgot to configure
// STRIPE_WEBHOOK_SECRET must never silently accept unsigned events. Only
// local-dev callers who explicitly opted in (ALLOW_UNSIGNED_STRIPE_WEBHOOKS=1)
// reach the permissive branch.
func verifyWebhookDualSecret(payload []byte, sigHeader, liveSecret, testSecret string, allowUnsigned bool) (bool, bool) {
	if liveSecret == "" && testSecret == "" {
		if allowUnsigned {
			// Local-dev opt-in: accept and default to live for downstream
			// processing. Production/staging validators refuse startup before
			// this branch is reachable.
			return true, true
		}
		return false, false
	}
	if liveSecret != "" {
		if err := verifyStripeSignature(payload, sigHeader, liveSecret); err == nil {
			return true, true
		}
	}
	if testSecret != "" {
		if err := verifyStripeSignature(payload, sigHeader, testSecret); err == nil {
			return false, true
		}
	}
	return false, false
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

