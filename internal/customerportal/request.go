package customerportal

import (
	"context"
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
// in (P5). It never logs the full raw token — even at debug level that
// would land in log aggregators with a 15-minute window of reusability.
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
