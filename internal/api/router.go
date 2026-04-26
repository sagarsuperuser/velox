package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/sagarsuperuser/velox/internal/analytics"
	mw "github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/billingalert"
	"github.com/sagarsuperuser/velox/internal/coupon"
	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/creditnote"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/customerportal"
	"github.com/sagarsuperuser/velox/internal/dashauth"
	"github.com/sagarsuperuser/velox/internal/dashmembers"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/dunning"
	"github.com/sagarsuperuser/velox/internal/email"
	"github.com/sagarsuperuser/velox/internal/feature"
	"github.com/sagarsuperuser/velox/internal/hostedinvoice"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/payment"
	"github.com/sagarsuperuser/velox/internal/payment/breaker"
	"github.com/sagarsuperuser/velox/internal/paymentmethods"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/portalapi"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/recipe"
	"github.com/sagarsuperuser/velox/internal/session"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/tax"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/tenantstripe"
	"github.com/sagarsuperuser/velox/internal/testclock"
	"github.com/sagarsuperuser/velox/internal/usage"
	"github.com/sagarsuperuser/velox/internal/user"
	"github.com/sagarsuperuser/velox/internal/webhook"

	"github.com/stripe/stripe-go/v82"
)

// --- Scheduler health tracking ---

var (
	schedulerMu       sync.RWMutex
	schedulerLastRun  time.Time
	schedulerInterval time.Duration
)

// RecordSchedulerRun is called by the billing scheduler after each cycle
// so the health check can determine whether the scheduler is alive.
func RecordSchedulerRun() {
	schedulerMu.Lock()
	schedulerLastRun = time.Now()
	schedulerMu.Unlock()
}

// SetSchedulerInterval stores the configured scheduler interval so the
// health check knows when to flag it as degraded (>2x interval).
func SetSchedulerInterval(d time.Duration) {
	schedulerMu.Lock()
	schedulerInterval = d
	schedulerMu.Unlock()
}

type Server struct {
	router chi.Router

	// Exported for main.go to wire the billing scheduler + dunning
	BillingEngine      *billing.Engine
	DunningSvc         *dunning.Service
	SettingsStore      *tenant.SettingsStore
	WebhookOutSvc      *webhook.Service
	OutboxStore        *webhook.OutboxStore
	OutboxEnabled      bool
	EmailOutboxStore   *email.OutboxStore
	EmailOutboxEnabled bool
	EmailSender        *email.Sender
	CreditSvc          *credit.Service
	InvoiceSvc         *invoice.Service
	TokenSvc           *payment.TokenService
	PaymentReconciler  *payment.Reconciler
	// BillingAlertEvaluator scans active billing alerts on a tick and
	// fires `billing.alert.triggered` events via the webhook outbox.
	// Started by main.go alongside the billing scheduler so leader
	// gating is consistent across all background workers.
	BillingAlertEvaluator *billingalert.Evaluator
}

func NewServer(db *postgres.DB, clk clock.Clock) *Server {
	if clk == nil {
		clk = clock.Real()
	}

	// Stores
	invoiceStore := invoice.NewPostgresStore(db)
	subStore := subscription.NewPostgresStore(db)
	usageStore := usage.NewPostgresStore(db)
	pricingStore := pricing.NewPostgresStore(db)
	webhookStore := payment.NewPostgresWebhookStore(db)

	// Auth
	authSvc := auth.NewService(auth.NewPostgresStore(db))
	authH := auth.NewHandler(authSvc)

	// Domain handlers
	tenantSvc := tenant.NewService(tenant.NewPostgresStore(db))
	tenantH := tenant.NewHandler(tenantSvc)
	customerStore := customer.NewPostgresStore(db)

	// PII + webhook-secret encryption at rest — AES-256-GCM via VELOX_ENCRYPTION_KEY.
	// Customer PII (email, names, phone, tax IDs), webhook signing secrets,
	// and per-tenant Stripe credentials all share the same encryptor so a
	// key rotation flows through uniformly. Without a key, they fall back
	// to plaintext — config.go already requires the key in production.
	var sharedEnc *crypto.Encryptor
	if encKey := strings.TrimSpace(os.Getenv("VELOX_ENCRYPTION_KEY")); encKey != "" {
		enc, err := crypto.NewEncryptor(encKey)
		if err != nil {
			slog.Error("invalid VELOX_ENCRYPTION_KEY, encryption at rest disabled", "error", err)
		} else {
			sharedEnc = enc
			customerStore.SetEncryptor(enc)
			slog.Info("encryption at rest enabled for customer PII, webhook secrets, and Stripe credentials")
		}
	} else {
		slog.Warn("VELOX_ENCRYPTION_KEY not set — customer PII, webhook secrets, and Stripe credentials stored in plaintext")
	}

	// Per-tenant Stripe credentials. Each tenant connects their own Stripe
	// account via POST /v1/settings/stripe; the service looks up the right
	// keys per request. There are no platform-level STRIPE_SECRET_KEY env
	// vars anymore — Velox is a billing engine, not a merchant of record.
	tenantStripeStore := tenantstripe.NewStore(db)
	if sharedEnc != nil {
		tenantStripeStore.SetEncryptor(sharedEnc)
	}
	tenantStripeSvc := tenantstripe.NewService(tenantStripeStore, func(secretKey string) *stripe.Client {
		return stripe.NewClient(secretKey)
	})
	tenantStripeH := tenantstripe.NewHandler(tenantStripeSvc)
	stripeClients := payment.NewStripeClients(tenantStripeSvc)

	pricingSvc := pricing.NewService(pricingStore)
	customerSvc := customer.NewService(customerStore)
	customerSvc.SetStripeSyncer(payment.NewStripeBillingSync(stripeClients), customerStore)
	customerH := customer.NewHandler(customerSvc)
	pricingH := pricing.NewHandler(pricingSvc)
	subSvc := subscription.NewService(subStore, clk)
	subH := subscription.NewHandler(subSvc)
	// Proration deps are wired below after creditSvc + invoiceStore are available
	usageSvc := usage.NewService(usageStore)
	usageH := usage.NewHandler(usageSvc, customerStore, pricingSvc)
	customerUsageH := usage.NewCustomerUsageHandler(
		usage.NewCustomerUsageService(usageSvc, customerStore, subStore, pricingSvc),
	)
	settingsStore := tenant.NewSettingsStore(db)
	creditStore := credit.NewPostgresStore(db)
	creditSvc := credit.NewService(creditStore)
	creditH := credit.NewHandler(creditSvc)
	couponSvc := coupon.NewService(coupon.NewPostgresStore(db))
	couponH := coupon.NewHandler(couponSvc)
	creditNoteStore := creditnote.NewPostgresStore(db)

	// Wire proration dependencies for plan change invoicing
	subH.SetProrationDeps(pricingSvc, &prorationInvoiceCreatorAdapter{store: invoiceStore, numberer: settingsStore}, &prorationCreditGranterAdapter{svc: creditSvc})
	subH.SetProrationCouponApplier(couponSvc)

	// Payment / webhook / checkout / refund handlers
	stripeRefunder := payment.NewStripeRefunder(stripeClients)
	creditNoteSvc := creditnote.NewService(creditNoteStore, invoiceStore, stripeRefunder, &creditGrantAdapter{svc: creditSvc})
	creditNoteSvc.SetNumberGenerator(settingsStore)
	creditNoteH := creditnote.NewHandler(creditNoteSvc, creditnote.HandlerDeps{
		Customers: customerStore,
		Settings:  settingsStore,
		Invoices:  invoiceStore,
	})
	webhookOutStore := webhook.NewPostgresStore(db)
	if sharedEnc != nil {
		webhookOutStore.SetEncryptor(sharedEnc)
	}
	webhookOutSvc := webhook.NewService(webhookOutStore, nil)
	webhookOutH := webhook.NewHandler(webhookOutSvc)

	// Transactional outbox for outbound events (RES-1). When enabled, producers
	// persist an event intent to webhook_outbox before returning; a background
	// Dispatcher drains the queue and calls Service.Dispatch for each row.
	// Crashes between business-op commit and event emission can no longer
	// silently lose events. Disable via VELOX_WEBHOOK_OUTBOX_ENABLED=false for
	// emergency rollback to the legacy direct-dispatch path.
	outboxStore := webhook.NewOutboxStore(db)
	outboxEnabled := strings.ToLower(strings.TrimSpace(os.Getenv("VELOX_WEBHOOK_OUTBOX_ENABLED"))) != "false"
	var eventDispatcher domain.EventDispatcher = webhookOutSvc
	if outboxEnabled {
		eventDispatcher = webhook.NewOutboxDispatcher(outboxStore)
		slog.Info("webhook outbox enabled — producers will enqueue events via webhook_outbox")
	} else {
		slog.Warn("webhook outbox DISABLED — using legacy direct-dispatch path (set VELOX_WEBHOOK_OUTBOX_ENABLED=true to re-enable)")
	}
	subH.SetEventDispatcher(eventDispatcher)
	auditLogger := audit.NewLogger(db)
	auditH := audit.NewHandler(auditLogger)
	settingsH := tenant.NewSettingsHandler(settingsStore)
	stripeClient := payment.NewLiveStripeClient(stripeClients)
	dunningStore := dunning.NewPostgresStore(db)
	dunningSvc := dunning.NewService(dunningStore, nil, clk) // retrier set below after stripeAdapter created
	dunningH := dunning.NewHandler(dunningSvc, dunning.HandlerDeps{
		Invoices:       invoiceStore,
		CreditReverser: creditSvc,
		PaymentCancel:  payment.NewLiveStripeClient(stripeClients),
	})

	// Recipe registry — built-in pricing recipes loaded once at boot from
	// the embedded YAML. Failure here is fatal: a malformed recipe would
	// crash on first instantiate, and TestLoad already gates this in CI,
	// so reaching this panic implies a broken build that should not run.
	recipeRegistry, err := recipe.Load()
	if err != nil {
		panic(fmt.Sprintf("load recipe registry: %v", err))
	}
	recipeStore := recipe.NewPostgresStore(db)
	recipeSvc := recipe.NewService(db, recipeStore, recipeRegistry, pricingSvc, dunningSvc, webhookOutSvc)
	recipeH := recipe.NewHandler(recipeSvc)

	// Global circuit breaker around Stripe calls. When Stripe is unhealthy
	// (5xx/timeout/network), every caller backs off together so we don't
	// pile retries onto a struggling dependency. Opens after 5 consecutive
	// Unknown failures, probes after 30s, and emits state transitions to
	// the velox_stripe_breaker_state gauge so operators can alert on it.
	stripeBreaker := breaker.New(breaker.Config{
		FailureThreshold: 5,
		Cooldown:         30 * time.Second,
		Interval:         60 * time.Second,
		Countable:        payment.IsUnknownPaymentFailure,
		OnStateChange: func(_, to breaker.State) {
			mw.RecordStripeBreakerState(string(to))
		},
	})

	stripeAdapter := payment.NewStripe(stripeClient, invoiceStore, webhookStore, customerStore, dunningSvc)
	stripeAdapter.SetCardFetcher(stripeClient)
	stripeAdapter.SetEventDispatcher(eventDispatcher)
	stripeAdapter.SetBreaker(stripeBreaker)

	// Wire payment retrier now that stripeAdapter exists
	dunningSvc.SetRetrier(&paymentRetrierAdapter{
		charger:       stripeAdapter,
		invoiceStore:  invoiceStore,
		paymentSetups: customerStore,
	})
	dunningSvc.SetSubscriptionPauser(&subscriptionPauserAdapter{svc: subSvc}, invoiceStore)
	dunningSvc.SetEventDispatcher(eventDispatcher)
	// customer service emits customer.email_bounced when T0-20 bounce
	// reporting fires; needs the same webhook dispatcher as the other
	// domain services.
	customerSvc.SetEventDispatcher(eventDispatcher)
	webhookH := payment.NewHandler(stripeAdapter, tenantStripeSvc)

	invoiceSvc := invoice.NewService(invoiceStore, clk, settingsStore)
	couponSvc.SetCustomerHistoryLookup(invoiceSvc)
	invoiceH := invoice.NewHandler(invoiceSvc, customerStore, settingsStore, invoice.HandlerDeps{
		CreditNotes:     &creditNoteListerAdapter{svc: creditNoteSvc},
		Charger:         stripeAdapter,
		PaymentSetups:   customerStore,
		CreditReverser:  creditSvc,
		PaymentCancel:   stripeClient,
		Dunning:         dunningSvc,
		WebhookEvents:   webhookStore,
		DunningTimeline: &dunningTimelineAdapter{store: dunningStore},
		Events:          eventDispatcher,
		RefundIssuer:    &refundIssuerAdapter{svc: creditNoteSvc},
	})
	checkoutH := payment.NewCheckoutHandler(stripeClients,
		strings.TrimSpace(os.Getenv("STRIPE_CHECKOUT_SUCCESS_URL")),
		strings.TrimSpace(os.Getenv("STRIPE_CHECKOUT_CANCEL_URL")),
		customerStore)
	portalH := payment.NewPortalHandler(stripeClients, customerStore)

	// Token service for public payment update links
	tokenSvc := payment.NewTokenService(db)
	stripeAdapter.SetTokenService(tokenSvc)

	// Reconciler for PaymentUnknown invoices (ambiguous Stripe outcomes).
	// 60s cool-off lets webhooks resolve the state naturally before we
	// spend an extra API call.
	paymentReconciler := payment.NewReconciler(stripeClient, invoiceStore, 60*time.Second)
	paymentReconciler.SetBreaker(stripeBreaker)
	publicPaymentH := payment.NewPublicPaymentHandler(tokenSvc, db, stripeClients,
		strings.TrimSpace(os.Getenv("PAYMENT_UPDATE_RETURN_URL")))

	// Hosted invoice page — Stripe-equivalent hosted_invoice_url surface.
	// Public tokenized routes that the partner's end customer visits from
	// an email (T0-17). The Stripe adapter picks live/test keys based on
	// the invoice row's livemode; HOSTED_INVOICE_BASE_URL is the public
	// SPA origin the Checkout success/cancel URLs redirect back to. In
	// dev, leave empty and the handler falls back to http://localhost:5173
	// so `make dev` works without extra config.
	hostedInvoiceH := hostedinvoice.New(hostedinvoice.Deps{
		Invoices:    invoiceSvc,
		Customers:   customerStore,
		Settings:    settingsStore,
		CreditNotes: &creditNoteListerAdapter{svc: creditNoteSvc},
		Stripe:      &hostedInvoiceStripeAdapter{clients: stripeClients, db: db},
		BaseURL:     strings.TrimSpace(os.Getenv("HOSTED_INVOICE_BASE_URL")),
	})

	// Email sender. When the email outbox is enabled (default), producers
	// enqueue into email_outbox via OutboxSender instead of calling SMTP
	// directly; a background Dispatcher drains the queue. This makes email
	// delivery durable across crashes and transient SMTP failures, and gives
	// operators a DLQ to inspect. Set VELOX_EMAIL_OUTBOX_ENABLED=false to
	// fall back to the direct-SMTP path for emergency rollback.
	emailSender := email.NewSender()
	// Wire tenant settings so customer-facing emails pull per-tenant
	// branding (logo, brand color, company name, support URL). Cold-start
	// tenants without settings gracefully fall back to Velox defaults.
	emailSender.SetSettingsGetter(settingsStore)
	// bounceReporterAdapter wires Sender SMTP 5xx errors to
	// customer.MarkEmailBounced. Requires an email blinder (configured
	// later from VELOX_EMAIL_BIDX_KEY); if the blinder is missing, the
	// adapter short-circuits and the partner stays on pre-T0-20 behavior
	// (bounces logged, customer.email_status stays 'unknown').
	// Attached inside the blinder branch below once emailBlinder exists.
	emailOutboxStore := email.NewOutboxStore(db)
	emailOutboxEnabled := strings.ToLower(strings.TrimSpace(os.Getenv("VELOX_EMAIL_OUTBOX_ENABLED"))) != "false"

	// Any one of the six domain email interfaces; all are satisfied by
	// both *email.Sender and *email.OutboxSender, so we pick once and wire
	// the same value everywhere.
	var (
		invoiceEmail       invoice.EmailSender
		dunningEmail       dunning.EmailNotifier
		receiptEmail       payment.EmailReceipt
		paymentUpdate      payment.EmailPaymentUpdate
		magicLinkEmail     customerportal.MagicLinkEmailSender
		passwordResetEmail dashauth.EmailNotifier
	)
	if emailOutboxEnabled {
		outboxSender := email.NewOutboxSender(emailOutboxStore)
		invoiceEmail, dunningEmail, receiptEmail, paymentUpdate, magicLinkEmail, passwordResetEmail = outboxSender, outboxSender, outboxSender, outboxSender, outboxSender, outboxSender
		slog.Info("email outbox enabled — producers will enqueue emails via email_outbox")
	} else {
		invoiceEmail, dunningEmail, receiptEmail, paymentUpdate, magicLinkEmail, passwordResetEmail = emailSender, emailSender, emailSender, emailSender, emailSender, emailSender
		slog.Warn("email outbox DISABLED — using direct-SMTP path (set VELOX_EMAIL_OUTBOX_ENABLED=true to re-enable)")
	}
	invoiceH.SetEmailSender(invoiceEmail)
	customerEmailAdapter := &customerEmailFetcherAdapter{store: customerStore}
	dunningSvc.SetEmailNotifier(dunningEmail)
	dunningSvc.SetCustomerEmailFetcher(customerEmailAdapter)
	stripeAdapter.SetEmailReceipt(receiptEmail, customerEmailAdapter)
	paymentUpdateURL := strings.TrimSpace(os.Getenv("PAYMENT_UPDATE_URL"))
	if paymentUpdateURL != "" {
		stripeAdapter.SetEmailPaymentUpdate(paymentUpdate, customerEmailAdapter, paymentUpdateURL)
	}

	// Audit logging for financial operations
	creditH.SetAuditLogger(auditLogger)
	invoiceH.SetAuditLogger(auditLogger)
	subH.SetAuditLogger(auditLogger)
	creditNoteH.SetAuditLogger(auditLogger)
	couponH.SetAuditLogger(auditLogger)
	couponH.SetEventDispatcher(eventDispatcher)

	// Feature flags (created before billing engine to gate Stripe Tax)
	featureSvc := feature.NewService(feature.NewPostgresStore(db))
	featureH := feature.NewHandler(featureSvc)

	// Test clocks (FEAT-8 P5) — test-mode-only frozen-time simulator.
	// Constructed before the billing engine so the engine can read clock
	// state; the service then receives the engine as its billing runner via
	// SetBillingRunner below, breaking the circular dep.
	testClockStore := testclock.NewPostgresStore(db)
	testClockSvc := testclock.NewService(testClockStore)
	testClockH := testclock.NewHandler(testClockSvc)

	// Billing engine + manual trigger (with credit auto-application)
	engine := billing.NewEngine(subStore, usageStore, pricingSvc,
		&invoiceWriterAdapter{store: invoiceStore}, creditSvc, settingsStore, customerStore, stripeAdapter, clk, customerStore)
	engine.SetTestClockReader(testClockStore)
	engine.SetEventDispatcher(eventDispatcher)
	testClockSvc.SetBillingRunner(engine)

	// Tax: per-tenant provider resolution (none|manual|stripe_tax) + durable
	// audit trail in tax_calculations. Resolver reads tenant_settings and
	// hands back the concrete Provider; the store persists request/response
	// payloads so we can reconstruct tax decisions after Stripe's 24h
	// calculation expiry window.
	engine.SetTaxProviderResolver(tax.NewResolver(stripeClients))
	engine.SetTaxCalculationStore(tax.NewPostgresStore(db))

	// Invoice finalize commits the upstream Stripe Tax calculation into a
	// tax_transaction so the tenant's Stripe Tax reports reflect the final
	// invoice. Manual/none providers receive the call but no-op.
	invoiceSvc.SetTaxCommitter(engine)

	// Operator-initiated apply-coupon-to-draft-invoice routes through the
	// billing engine, which owns redeem → tax recompute → atomic persist
	// → mark-periods orchestration. Keeps the HTTP surface on the invoice
	// resource while the engine does the coordination.
	invoiceSvc.SetCouponApplier(engine)

	// Credit note issue reverses the invoice's tax_transaction so the
	// tenant's upstream tax liability is reduced alongside the refund.
	// Required for EU VAT, UK VAT, India GST compliance — without this
	// the credit note refunds the customer's money but leaves the
	// tenant over-remitting tax. Manual/none providers receive the call
	// but no-op.
	creditNoteSvc.SetTaxReverser(engine)
	// Full-credit/full-refund credit notes reverse coupon usage on the
	// underlying invoice: voids the redemption rows, rolls back
	// times_redeemed and periods_applied so "once" / "repeating" coupons
	// aren't permanently burned by a refunded invoice.
	creditNoteSvc.SetCouponRedemptionVoider(couponSvc)

	// Coupon discount applier: billing engine consults redemptions at finalize time.
	engine.SetCouponApplier(couponSvc)

	// Proration invoices now share the billing engine's tax resolution path so
	// plan upgrades aren't silently tax-free. The adapter translates between
	// billing.TaxApplication and subscription.ProrationTaxResult.
	subH.SetProrationTaxApplier(&prorationTaxApplierAdapter{engine: engine})

	billingH := billing.NewHandler(engine, subStore)
	// create_preview composes customer / subscription resolution on top of
	// the engine's preview path. Mounted at /v1/invoices/create_preview
	// (Stripe-equivalent), gated by PermInvoiceRead — read-only, no DB
	// writes. Sibling-mounted ahead of /invoices below so chi picks the
	// more-specific pattern (otherwise /{id} captures "create_preview").
	createPreviewH := billing.NewCreatePreviewHandler(
		billing.NewPreviewService(engine, customerStore, subStore),
	)

	// Billing alerts: operator-defined spend/usage thresholds with a
	// background evaluator that fires `billing.alert.triggered` via the
	// webhook outbox atomically with the alert state mutation. Mounted
	// at /v1/billing/alerts BELOW so the route is sibling to /billing
	// (the more-specific /billing/alerts pattern is registered before
	// /billing/{id} to avoid the param capture). See
	// docs/design-billing-alerts.md.
	billingAlertStore := billingalert.NewPostgresStore(db)
	billingAlertSvc := billingalert.NewService(billingAlertStore, customerStore, pricingSvc)
	billingAlertH := billingalert.NewHandler(billingAlertSvc)
	billingAlertEvaluator := billingalert.NewEvaluator(
		billingAlertStore,
		customerStore,
		&billingAlertSubscriptionListerAdapter{store: subStore},
		pricingSvc,
		usageSvc,
		outboxStore,
		clk,
	)
	billingAlertEvaluator.SetLocker(&billingAlertLockerAdapter{db: db}, postgres.LockKeyBillingAlertEvaluator)
	if intervalStr := strings.TrimSpace(os.Getenv("VELOX_BILLING_ALERTS_INTERVAL")); intervalStr != "" {
		if d, err := time.ParseDuration(intervalStr); err == nil && d > 0 {
			billingAlertEvaluator.SetInterval(d)
		} else {
			slog.Warn("VELOX_BILLING_ALERTS_INTERVAL invalid — using default", "value", intervalStr, "error", err)
		}
	}

	analyticsH := analytics.NewHandler(db)

	// Customer portal sessions — operators mint a session for a customer
	// via POST /v1/customer-portal-sessions. The returned token is what
	// authenticates the customer against /v1/me/* (see Middleware below).
	portalSvc := customerportal.NewService(customerportal.NewPostgresStore(db))
	portalOperatorH := customerportal.NewOperatorHandler(
		portalSvc,
		strings.TrimSpace(os.Getenv("CUSTOMER_PORTAL_URL")),
	)

	// Customer-initiated magic-link flow: the customer enters their email
	// at /login, we look up matches via the keyed blind index (separate
	// HMAC key from VELOX_ENCRYPTION_KEY so one compromise doesn't
	// reveal the other), mint a short-lived token per match, and deliver
	// via the email outbox. Blinder is required; without it the email
	// lookup silently fails closed and no links can be minted.
	var emailBlinder *crypto.Blinder
	if bidxKey := strings.TrimSpace(os.Getenv("VELOX_EMAIL_BIDX_KEY")); bidxKey != "" {
		b, err := crypto.NewBlinder(bidxKey)
		if err != nil {
			slog.Error("invalid VELOX_EMAIL_BIDX_KEY, magic-link requests will fail closed", "error", err)
		} else {
			emailBlinder = b
			customerStore.SetBlinder(b)
			// Wire bounce reporting now that we have the blinder.
			emailSender.SetBounceReporter(&bounceReporterAdapter{
				blinder: b,
				store:   customerStore,
				svc:     customerSvc,
			})
			slog.Info("email blind index enabled for customer-portal magic-link lookup + bounce reporting")
		}
	} else {
		slog.Warn("VELOX_EMAIL_BIDX_KEY not set — magic-link requests will fail closed (no customers findable by email)")
	}
	magicLinkStore := customerportal.NewPostgresMagicLinkStore(db)
	magicLinkSvc := customerportal.NewMagicLinkService(magicLinkStore, portalSvc)
	// Email delivery: when CUSTOMER_PORTAL_URL is configured, wire the
	// real email path so customers receive a clickable /login URL. Without
	// that URL the delivery would email only the raw token, which is
	// worse than no email — fall back to the logger stub so ops notices
	// and configures the var rather than silently shipping broken emails.
	var magicLinkDelivery customerportal.MagicLinkDelivery
	portalURL := strings.TrimSpace(os.Getenv("CUSTOMER_PORTAL_URL"))
	if portalURL != "" {
		magicLinkDelivery = customerportal.NewEmailMagicLinkDelivery(
			magicLinkEmail, customerEmailAdapter, portalURL, slog.Default(),
		)
	} else {
		slog.Warn("CUSTOMER_PORTAL_URL not set — magic-link emails disabled, logging tokens instead")
		magicLinkDelivery = customerportal.NewLogMagicLinkDelivery(slog.Default())
	}
	magicLinkRequestSvc := customerportal.NewMagicLinkRequestService(
		magicLinkSvc,
		&customerLookupAdapter{store: customerStore},
		emailBlinder,
		magicLinkDelivery,
		slog.Default(),
	)
	publicPortalH := customerportal.NewPublicHandler(magicLinkRequestSvc, magicLinkSvc)

	// Customer self-service payment methods — the customer-facing half of
	// the portal. Writes to payment_methods (multi-row) and keeps the
	// 1:1 customer_payment_setups summary in sync via Service.syncSummary.
	paymentMethodsStore := paymentmethods.NewPostgresStore(db)
	paymentMethodsStripe := paymentmethods.NewStripeAdapter(stripeClients, customerStore)
	paymentMethodsSvc := paymentmethods.NewService(paymentMethodsStore, paymentMethodsStripe, customerStore)
	paymentMethodsH := paymentmethods.NewHandler(paymentMethodsSvc)

	// setup_intent.succeeded webhooks write payment_methods rows via the
	// service. Wired here because stripeAdapter (payment/) must not import
	// paymentmethods/ — the interface lives in payment/ and the method
	// name is AttachForWebhook so the two AttachFromSetupIntent callers
	// (tests, webhook) keep their own signatures.
	stripeAdapter.SetPaymentMethodAttacher(paymentMethodsSvc)

	// Dashboard (email+password) auth — embedded user + session services
	// backing the UI login flow. Deliberately separate from API-key auth
	// (which protects /v1/* for machine traffic); session cookies only
	// authenticate the /v1/auth and /v1/session surface plus whichever
	// UI-facing endpoints mount session.Middleware. Password-reset emails
	// flow through the same outbox/SMTP selector as every other domain email.
	userSvc := user.NewService(user.NewPostgresStore(db))
	sessionSvc := session.NewService(session.NewPostgresStore(db))
	dashauthH := dashauth.NewHandler(
		userSvc,
		sessionSvc,
		tenantNameLookup{svc: tenantSvc},
		passwordResetEmail,
		strings.TrimSpace(os.Getenv("VELOX_DASHBOARD_PASSWORD_RESET_URL")),
		strings.TrimSpace(os.Getenv("VELOX_DASHBOARD_INVITE_URL")),
		dashauth.DefaultCookieConfig(),
	)

	// GDPR data export + deletion — wired into customer handler
	gdprSvc := customer.NewGDPRService(customerStore, invoiceStore, creditStore, subStore, auditLogger)
	customerH.SetGDPR(customer.NewGDPRHandler(gdprSvc))

	s := &Server{
		BillingEngine:         engine,
		DunningSvc:            dunningSvc,
		SettingsStore:         settingsStore,
		WebhookOutSvc:         webhookOutSvc,
		OutboxStore:           outboxStore,
		OutboxEnabled:         outboxEnabled,
		EmailOutboxStore:      emailOutboxStore,
		EmailOutboxEnabled:    emailOutboxEnabled,
		EmailSender:           emailSender,
		CreditSvc:             creditSvc,
		InvoiceSvc:            invoiceSvc,
		TokenSvc:              tokenSvc,
		PaymentReconciler:     paymentReconciler,
		BillingAlertEvaluator: billingAlertEvaluator,
	}

	// Redis for distributed rate limiting (fail-open if not configured)
	var rdb *redis.Client
	if redisURL := strings.TrimSpace(os.Getenv("REDIS_URL")); redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			slog.Warn("invalid REDIS_URL, rate limiting will fail open", "error", err)
		} else {
			rdb = redis.NewClient(opt)
			if err := rdb.Ping(context.Background()).Err(); err != nil {
				slog.Warn("redis not reachable, rate limiting will fail open", "error", err)
			} else {
				slog.Info("redis connected for rate limiting")
			}
		}
	} else {
		slog.Info("REDIS_URL not set, rate limiting will fail open")
	}
	rateLimiter := mw.NewRateLimiter(rdb, 100, time.Minute)
	// In production, refuse requests when Redis is unreachable rather than
	// silently disabling rate limiting (DDoS vector).
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production") {
		rateLimiter.SetFailClosed(true)
	}

	// Tighter bucket for the public hosted-invoice surface. Industry
	// practice (Stripe, Paddle) is to rate-limit payment pages more
	// aggressively than the general API because: (a) the URL is publicly
	// shareable so abuse potential is higher, (b) each Pay click creates a
	// Stripe Checkout Session which has its own upstream rate limit, and
	// (c) genuine customer traffic from a single NAT is small — ~5-10
	// requests per visit. 60/min per IP leaves headroom for ~10
	// simultaneous customers behind a single corporate NAT while keeping
	// enumerations and scraping bounded.
	hostedInvoiceRL := mw.NewRateLimiter(rdb, 60, time.Minute)
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production") {
		hostedInvoiceRL.SetFailClosed(true)
	}

	r := chi.NewRouter()
	r.Use(mw.Tracing())
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	corsEnv := os.Getenv("CORS_ALLOWED_ORIGINS")
	if corsEnv == "" {
		corsEnv = "http://localhost:3000,http://localhost:5173,http://localhost:5174"
	}
	r.Use(mw.CORS(strings.Split(corsEnv, ",")))
	r.Use(mw.Metrics())
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)

	// Limit request body to 1 MB to prevent DoS via oversized payloads.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
			next.ServeHTTP(w, r)
		})
	})

	r.Use(mw.SecurityHeaders())
	r.Use(middleware.Timeout(30 * time.Second))

	// Public
	r.Get("/health", handleHealth)
	r.Get("/health/ready", handleDeepHealth(db))
	r.Handle("/metrics", mw.MetricsAuth(mw.MetricsHandler()))

	// Bootstrap — one-time setup (no auth, only works when no tenants exist)
	bootstrapH := tenant.NewBootstrapHandler(db)
	r.Mount("/v1/bootstrap", bootstrapH.Routes())

	// Dashboard auth — email+password login, logout, password reset. Public
	// because the caller is pre-session; rate-limited to slow credential
	// stuffing. /v1/session is the session-scoped counterpart (whoami, mode
	// toggle) and requires the cookie issued by /v1/auth/login.
	r.Route("/v1/auth", func(r chi.Router) {
		r.Use(rateLimiter.Middleware())
		r.Mount("/", dashauthH.Routes())
	})
	r.Route("/v1/session", func(r chi.Router) {
		r.Use(session.Middleware(sessionSvc))
		r.Use(rateLimiter.Middleware())
		r.Mount("/", dashauthH.SessionRoutes())
	})

	// Members management — session-scoped. Lists active members + pending
	// invitations, issues new invitations (dashauth dispatches the email),
	// and handles revoke + remove with last-owner / self-removal guards.
	membersH := dashmembers.NewHandler(userSvc, dashauthH)
	r.Route("/v1/members", func(r chi.Router) {
		r.Use(session.Middleware(sessionSvc))
		r.Use(rateLimiter.Middleware())
		r.Mount("/", membersH.Routes())
	})

	// Stripe webhooks — no API key auth (verified by signature)
	r.Mount("/v1/webhooks", webhookH.Routes())

	// Public payment update — no auth (validated by token)
	if publicPaymentH != nil {
		r.Mount("/v1/public/payment-updates", publicPaymentH.Routes())
	}

	// Public hosted invoice — Stripe-equivalent hosted_invoice_url.
	// Unauthenticated: the 256-bit public_token in the URL is the sole
	// credential. Wrapped in its own rate-limit bucket (60/min per IP)
	// because payment surfaces deserve tighter limits than the general
	// API — see hostedInvoiceRL above.
	r.Route("/v1/public/invoices", func(r chi.Router) {
		r.Use(hostedInvoiceRL.Middleware())
		r.Mount("/", hostedInvoiceH.Routes())
	})

	// Public customer-portal routes (magic-link request + consume). No
	// API-key auth: the caller supplies an email (request) or a token
	// (consume) and that's the only credential. Rate-limited by IP via
	// the same middleware that limits authenticated traffic — unauthed
	// callers fall through to ip:<addr> buckets, so a single host
	// probing emails hits the same 100/min ceiling as any other caller.
	r.Route("/v1/public/customer-portal", func(r chi.Router) {
		r.Use(rateLimiter.Middleware())
		r.Mount("/", publicPortalH.Routes())
	})

	// Platform routes
	r.Route("/v1/tenants", func(r chi.Router) {
		r.Use(auth.Middleware(authSvc))
		r.Use(auth.Require(auth.PermTenantWrite))
		r.Mount("/", tenantH.Routes())
	})

	// Tenant-scoped routes — accept either dashboard session cookie OR
	// Authorization: Bearer API key. Session takes precedence when the cookie
	// is present; external integrations (no cookie) fall through to API-key
	// auth. Both paths set the same ctx keys so handlers don't branch.
	r.Route("/v1", func(r chi.Router) {
		r.Use(session.MiddlewareOrAPIKey(sessionSvc, authSvc))
		r.Use(rateLimiter.Middleware()) // After auth so tenant ID is available for bucket key
		r.Use(mw.Idempotency(db))
		r.Use(mw.AuditLog(db, settingsStore))

		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/api-keys", authH.Routes())
		r.With(auth.Require(auth.PermCustomerRead)).Mount("/customers", customerH.Routes())
		// Customer-scoped coupon assignment. Mounted as a sibling of
		// /customers so GET/POST/DELETE can carry independent permission
		// guards (attach/revoke need write, get needs read) without
		// threading the coupon handler into the customer package.
		r.Mount("/customers/{id}/coupon", couponH.CustomerAssignmentRoutes(
			auth.Require(auth.PermCustomerRead),
			auth.Require(auth.PermCustomerWrite),
		))
		// Customer-scoped usage view — composes usage aggregation with
		// customer / subscription / pricing reads. PermUsageRead so the
		// dashboard's read-only secret-tier key can call it without
		// inheriting customer write capability.
		r.Mount("/customers/{id}/usage", customerUsageH.CustomerUsageRoutes(
			auth.Require(auth.PermUsageRead),
		))
		r.With(auth.Require(auth.PermPricingRead)).Mount("/meters", pricingH.MeterRoutes())
		// Meter-scoped pricing rule subtree. Mounted as a sibling of
		// /meters so reads (PermPricingRead) and writes (PermPricingWrite)
		// can carry independent guards without nesting permission middleware.
		r.Mount("/meters/{meter_id}/pricing-rules", pricingH.MeterPricingRuleRoutes(
			auth.Require(auth.PermPricingRead),
			auth.Require(auth.PermPricingWrite),
		))
		r.With(auth.Require(auth.PermPricingRead)).Mount("/plans", pricingH.PlanRoutes())
		r.With(auth.Require(auth.PermPricingRead)).Mount("/rating-rules", pricingH.RatingRuleRoutes())
		r.With(auth.Require(auth.PermPricingWrite)).Mount("/recipes", recipeH.Routes())
		r.With(auth.Require(auth.PermSubscriptionRead)).Mount("/subscriptions", subH.Routes())
		// Backfill is mounted ahead of the /usage-events subtree so chi picks
		// the more-specific pattern; PermUsageWrite gates it to secret-tier
		// keys (publishable keys are read-only).
		r.With(auth.Require(auth.PermUsageWrite)).Post("/usage-events/backfill", usageH.Backfill)
		r.With(auth.Require(auth.PermUsageRead)).Mount("/usage-events", usageH.Routes())
		// create_preview must mount BEFORE /invoices because chi tries
		// patterns in registration order — once /invoices is mounted with
		// /{id}/... children, "create_preview" would be claimed as an
		// invoice ID. See docs/design-create-preview.md.
		r.With(auth.Require(auth.PermInvoiceRead)).Mount("/invoices/create_preview", createPreviewH.Routes())
		r.With(auth.Require(auth.PermInvoiceRead)).Mount("/invoices", invoiceH.Routes())
		r.With(auth.Require(auth.PermInvoiceWrite)).Mount("/credit-notes", creditNoteH.Routes())
		r.With(auth.Require(auth.PermPricingWrite)).Mount("/price-overrides", pricingH.OverrideRoutes())
		r.With(auth.Require(auth.PermPricingWrite)).Mount("/coupons", couponH.Routes())
		r.With(auth.Require(auth.PermCustomerWrite)).Mount("/credits", creditH.Routes())
		r.With(auth.Require(auth.PermDunningRead)).Mount("/dunning", dunningH.Routes())
		// /billing/alerts must mount BEFORE /billing because chi tries
		// patterns in registration order — once /billing is mounted with
		// /{id}/... children, "alerts" would be claimed as a billing-job
		// ID. See docs/design-billing-alerts.md.
		// Per-route auth: read (list, get) → PermInvoiceRead; write
		// (create, archive) → PermInvoiceWrite — same level as invoice-
		// related surfaces since alerts are an invoice-adjacent operator
		// capability.
		r.Mount("/billing/alerts", billingAlertH.Routes(
			auth.Require(auth.PermInvoiceRead),
			auth.Require(auth.PermInvoiceWrite),
		))
		r.With(auth.Require(auth.PermInvoiceWrite)).Mount("/billing", billingH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/webhook-endpoints", webhookOutH.Routes())
		r.With(auth.Require(auth.PermAPIKeyRead)).Mount("/audit-log", auditH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/settings", settingsH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/settings/stripe", tenantStripeH.Routes())
		r.With(auth.Require(auth.PermInvoiceRead)).Mount("/analytics", analyticsH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/feature-flags", featureH.Routes())
		r.With(auth.Require(auth.PermTestClockWrite)).Mount("/test-clocks", testClockH.Routes())
		r.With(auth.Require(auth.PermUsageRead)).Mount("/usage-summary", usageH.SummaryRoutes())
		if checkoutH != nil {
			r.With(auth.Require(auth.PermCustomerWrite)).Mount("/checkout", checkoutH.Routes())
		}

		// Customer portal — consolidated views across domains
		portal := newCustomerPortalHandler(subStore, invoiceStore, usageStore)
		r.With(auth.Require(auth.PermCustomerRead)).Mount("/customer-portal", portal.Routes())
		if portalH != nil {
			r.With(auth.Require(auth.PermCustomerWrite)).Mount("/payment-portal", portalH.Routes())
		}

		// Operator endpoint that mints portal bearer tokens for customers
		// to use against /v1/me/* below. Customer-side routes deliberately
		// live OUTSIDE this /v1 block because they're gated by the portal
		// session middleware, not by API-key auth.
		r.With(auth.Require(auth.PermCustomerWrite)).Mount("/customer-portal-sessions", portalOperatorH.Routes())
	})

	// Customer self-service surface — authenticated by a portal bearer
	// token (vlx_cps_...) rather than a tenant API key. See customerportal
	// package. Idempotency lives here because /me writes (setup-intent,
	// setup-session, detach) hit Stripe through our service — a double
	// click from a retry-happy mobile client must not create two payment
	// methods for the same card.
	portalAPI := portalapi.New(portalapi.Deps{
		Invoices:      invoiceSvc,
		Subscriptions: subSvc,
		Customers:     customerStore,
		Settings:      settingsStore,
		CreditNotes:   &creditNoteListerAdapter{svc: creditNoteSvc},
		Events:        eventDispatcher,
	})
	r.Route("/v1/me", func(r chi.Router) {
		r.Use(portalSvc.Middleware())
		r.Use(rateLimiter.Middleware())
		r.Use(mw.Idempotency(db))
		r.Mount("/payment-methods", paymentMethodsH.Routes())
		r.Mount("/", portalAPI.Routes())
	})

	s.router = r
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func handleDeepHealth(db *postgres.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		overallStatus := "ok"
		checks := map[string]string{"api": "ok"}

		// Database check
		if err := db.Pool.PingContext(ctx); err != nil {
			checks["database"] = "error: " + err.Error()
			overallStatus = "degraded"
		} else {
			checks["database"] = "ok"
		}

		// Scheduler check — degraded if no run recorded within 2x the interval.
		// Before the first run completes the scheduler is considered ok (just started).
		schedulerMu.RLock()
		lastRun := schedulerLastRun
		interval := schedulerInterval
		schedulerMu.RUnlock()

		if interval > 0 && !lastRun.IsZero() {
			sinceLastRun := time.Since(lastRun)
			if sinceLastRun > 2*interval {
				checks["scheduler"] = fmt.Sprintf("degraded: last run %s ago (interval %s)", sinceLastRun.Truncate(time.Second), interval)
				overallStatus = "degraded"
			} else {
				checks["scheduler"] = fmt.Sprintf("ok: last run %s ago", sinceLastRun.Truncate(time.Second))
			}
		} else if interval > 0 && lastRun.IsZero() {
			checks["scheduler"] = "ok: awaiting first run"
		} else {
			checks["scheduler"] = "ok: not configured"
		}

		status := http.StatusOK
		if overallStatus != "ok" {
			status = http.StatusServiceUnavailable
		}

		respond.JSON(w, r, status, map[string]any{
			"status": overallStatus,
			"checks": checks,
		})
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// tenantNameLookup adapts tenant.Service to dashauth.TenantLookup. Kept
// local to router.go because it's pure composition plumbing — dashauth
// only needs the display name, not the full tenant surface, so the
// narrow interface lives in dashauth and the adapter lives here.
type tenantNameLookup struct {
	svc *tenant.Service
}

func (t tenantNameLookup) Name(ctx context.Context, tenantID string) (string, error) {
	v, err := t.svc.Get(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return v.Name, nil
}
