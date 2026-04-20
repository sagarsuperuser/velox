package customerportal

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sagarsuperuser/velox/internal/platform/crypto"
)

// CustomerMatch is the narrow identity the request service needs per hit on
// the email blind index. Defined here — not imported from customer — so
// customerportal stays below customer in the dep graph. The wiring layer
// translates customer.EmailMatch → this.
type CustomerMatch struct {
	TenantID   string
	CustomerID string
	Livemode   bool
}

// CustomerLookup resolves an email blind index to zero or more active
// customers across tenants. Runs under TxBypass (the caller is
// unauthenticated), so correctness depends on HMAC(key, email) being
// unguessable — see internal/platform/crypto.Blinder.
type CustomerLookup interface {
	FindByEmailBlindIndex(ctx context.Context, blind string, limit int) ([]CustomerMatch, error)
}

// MagicLinkDelivery is the seam the request service uses to hand the
// freshly-minted token to whatever ships it (email outbox in production,
// a logger in tests/early wiring). Kept tiny so P5 can swap the logger
// stub for the real email sender without touching request.go.
type MagicLinkDelivery interface {
	DeliverMagicLink(ctx context.Context, tenantID, customerID, rawToken string) error
}

// maxEmailMatches bounds the fan-out from a single request. If the same
// email is registered across many tenants, we still mint at most this
// many links — a cheap guardrail against an unknown colliding index
// exploding work per request.
const maxEmailMatches = 10

// MagicLinkRequestService drives the public "I forgot my portal link"
// flow. Input is an email; output is always a 202 from the handler.
// Match-or-miss, malformed-or-clean, the service takes the same code path
// and same (bounded) time so an attacker can't learn anything from
// differential response behavior.
type MagicLinkRequestService struct {
	magic    *MagicLinkService
	lookup   CustomerLookup
	blinder  *crypto.Blinder
	delivery MagicLinkDelivery
	logger   *slog.Logger
}

func NewMagicLinkRequestService(
	magic *MagicLinkService,
	lookup CustomerLookup,
	blinder *crypto.Blinder,
	delivery MagicLinkDelivery,
	logger *slog.Logger,
) *MagicLinkRequestService {
	if logger == nil {
		logger = slog.Default()
	}
	return &MagicLinkRequestService{
		magic:    magic,
		lookup:   lookup,
		blinder:  blinder,
		delivery: delivery,
		logger:   logger,
	}
}

// RequestByEmail normalises the email, blinds it, looks up matches, and
// fires a delivery per match. Always returns nil unless an internal
// failure makes it unsafe to pretend success — callers should NOT branch
// on the match count, and the handler ignores everything but internal
// errors so no "did it match?" signal leaks to the client.
func (s *MagicLinkRequestService) RequestByEmail(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		// Nothing to do — return nil so the handler still responds 202.
		return nil
	}
	// Blinder not configured → FindByEmailBlindIndex returns nothing. The
	// response still looks identical to a genuine no-match. Router logs
	// a startup warning when VELOX_EMAIL_BIDX_KEY is unset; we stay silent
	// per request so the endpoint can't be used to amplify log volume.
	if s.blinder == nil || !s.blinder.IsEnabled() {
		return nil
	}
	blind := s.blinder.Blind(email)
	if blind == "" {
		return nil
	}

	matches, err := s.lookup.FindByEmailBlindIndex(ctx, blind, maxEmailMatches)
	if err != nil {
		// Real DB/infra failure — surface so the handler returns 500 and
		// we don't silently drop legitimate requests.
		return err
	}

	// One link per (tenant, customer). We continue past per-match failures
	// on purpose: one tenant's broken delivery must not starve another
	// tenant's legitimate customer of their link.
	for _, m := range matches {
		mint, err := s.magic.Mint(ctx, m.TenantID, m.CustomerID)
		if err != nil {
			s.logger.Error("magic-link mint failed",
				"tenant_id", m.TenantID, "customer_id", m.CustomerID, "error", err)
			continue
		}
		if err := s.delivery.DeliverMagicLink(ctx, m.TenantID, m.CustomerID, mint.RawToken); err != nil {
			s.logger.Error("magic-link delivery failed",
				"tenant_id", m.TenantID, "customer_id", m.CustomerID, "error", err)
			continue
		}
	}
	return nil
}

// LogMagicLinkDelivery is a stub delivery that writes the token prefix to
// slog. Used in early wiring and tests before the email outbox is wired
// in. It never logs the full raw token — even at debug level that would
// land in log aggregators with a 15-minute window of reusability.
type LogMagicLinkDelivery struct {
	logger *slog.Logger
}

func NewLogMagicLinkDelivery(logger *slog.Logger) *LogMagicLinkDelivery {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogMagicLinkDelivery{logger: logger}
}

func (d *LogMagicLinkDelivery) DeliverMagicLink(_ context.Context, tenantID, customerID, rawToken string) error {
	prefix := rawToken
	if len(prefix) > 12 {
		prefix = prefix[:12] + "…"
	}
	d.logger.Info("magic-link delivery (stub)",
		"tenant_id", tenantID,
		"customer_id", customerID,
		"token_prefix", prefix,
	)
	return nil
}

// MagicLinkEmailSender is the narrow email surface the delivery needs.
// Satisfied by both *email.Sender (direct SMTP) and *email.OutboxSender
// (enqueue into email_outbox). Defined here — not imported — so
// customerportal stays independent of the email package.
type MagicLinkEmailSender interface {
	SendPortalMagicLink(tenantID, to, customerName, magicLinkURL string) error
}

// CustomerEmailResolver looks up a customer's email + display name for
// the delivery adapter. The production implementation is
// customer.PostgresStore (via an adapter) and decrypts PII on the way
// out. Defined here to avoid a customerportal→customer import.
type CustomerEmailResolver interface {
	GetCustomerEmail(ctx context.Context, tenantID, customerID string) (email, name string, err error)
}

// EmailMagicLinkDelivery is the production MagicLinkDelivery — resolves
// the customer's email, composes the login URL, and hands it to the
// email sender (which in production is the outbox-backed implementation
// so an SMTP outage doesn't drop the email).
type EmailMagicLinkDelivery struct {
	sender   MagicLinkEmailSender
	resolver CustomerEmailResolver
	baseURL  string // e.g. https://portal.velox.dev
	logger   *slog.Logger
}

func NewEmailMagicLinkDelivery(sender MagicLinkEmailSender, resolver CustomerEmailResolver, baseURL string, logger *slog.Logger) *EmailMagicLinkDelivery {
	if logger == nil {
		logger = slog.Default()
	}
	return &EmailMagicLinkDelivery{
		sender:   sender,
		resolver: resolver,
		baseURL:  baseURL,
		logger:   logger,
	}
}

// DeliverMagicLink resolves the customer's email, builds the magic-link
// URL, and enqueues the email. A missing email is logged and skipped —
// it's legitimate (not every customer has an email on file) and the
// caller must NOT surface it as a failure, or the shape of the /magic-
// link endpoint's response could leak "is there an email on file?".
func (d *EmailMagicLinkDelivery) DeliverMagicLink(ctx context.Context, tenantID, customerID, rawToken string) error {
	email, name, err := d.resolver.GetCustomerEmail(ctx, tenantID, customerID)
	if err != nil {
		return fmt.Errorf("resolve customer email: %w", err)
	}
	if email == "" {
		d.logger.Warn("magic-link delivery skipped: customer has no email",
			"tenant_id", tenantID, "customer_id", customerID)
		return nil
	}
	url := buildMagicLinkURL(d.baseURL, rawToken)
	return d.sender.SendPortalMagicLink(tenantID, email, name, url)
}

// buildMagicLinkURL assembles the URL the email points at. The frontend
// route is /customer-portal/login — it reads ?magic_token=... from the
// URL, POSTs to /v1/public/customer-portal/magic/consume, then
// redirects into /customer-portal with the returned session token.
//
// The namespaced path (/customer-portal/login) keeps it from colliding
// with operator auth at /login when the same domain hosts both.
// magic_token is a distinct query-string key from `token` (reusable
// session) so the page can't conflate the two storage slots.
func buildMagicLinkURL(base, rawToken string) string {
	if base == "" {
		// No portal URL configured — return just the token so the email
		// still has something useful. Ops has to fix CUSTOMER_PORTAL_URL
		// for this to be a clickable link.
		return rawToken
	}
	base = strings.TrimRight(base, "/")
	return base + "/customer-portal/login?magic_token=" + rawToken
}
