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
	"github.com/sagarsuperuser/velox/internal/coupon"
	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/creditnote"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/dunning"
	"github.com/sagarsuperuser/velox/internal/email"
	"github.com/sagarsuperuser/velox/internal/feature"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/payment"
	"github.com/sagarsuperuser/velox/internal/payment/breaker"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/tax"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/usage"
	"github.com/sagarsuperuser/velox/internal/webhook"
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
	BillingEngine     *billing.Engine
	DunningSvc        *dunning.Service
	SettingsStore     *tenant.SettingsStore
	WebhookOutSvc     *webhook.Service
	OutboxStore       *webhook.OutboxStore
	OutboxEnabled     bool
	CreditSvc         *credit.Service
	InvoiceSvc        *invoice.Service
	TokenSvc          *payment.TokenService
	PaymentReconciler *payment.Reconciler
}

func NewServer(db *postgres.DB, stripeWebhookSecret string, clk clock.Clock) *Server {
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
	tenantH := tenant.NewHandler(tenant.NewService(tenant.NewPostgresStore(db)))
	stripeKey := strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY"))
	payment.InitStripe(stripeKey)
	customerStore := customer.NewPostgresStore(db)

	// PII encryption at rest — encrypt customer fields in the DB
	if encKey := strings.TrimSpace(os.Getenv("VELOX_ENCRYPTION_KEY")); encKey != "" {
		enc, err := crypto.NewEncryptor(encKey)
		if err != nil {
			slog.Error("invalid VELOX_ENCRYPTION_KEY, PII encryption disabled", "error", err)
		} else {
			customerStore.SetEncryptor(enc)
			slog.Info("PII encryption enabled for customer data")
		}
	} else {
		slog.Warn("VELOX_ENCRYPTION_KEY not set — customer PII stored in plaintext")
	}

	pricingSvc := pricing.NewService(pricingStore)
	customerSvc := customer.NewService(customerStore)
	customerSvc.SetStripeSyncer(payment.NewStripeBillingSync(stripeKey), customerStore)
	customerH := customer.NewHandler(customerSvc)
	pricingH := pricing.NewHandler(pricingSvc)
	subSvc := subscription.NewService(subStore, clk)
	subH := subscription.NewHandler(subSvc)
	// Proration deps are wired below after creditSvc + invoiceStore are available
	usageH := usage.NewHandler(usage.NewService(usageStore), customerStore, pricingSvc)
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
	stripeRefunder := payment.NewStripeRefunder(stripeKey)
	creditNoteSvc := creditnote.NewService(creditNoteStore, invoiceStore, stripeRefunder, &creditGrantAdapter{svc: creditSvc})
	creditNoteSvc.SetNumberGenerator(settingsStore)
	creditNoteH := creditnote.NewHandler(creditNoteSvc)
	webhookOutSvc := webhook.NewService(webhook.NewPostgresStore(db), nil)
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
	auditLogger := audit.NewLogger(db)
	auditH := audit.NewHandler(auditLogger)
	settingsH := tenant.NewSettingsHandler(settingsStore)
	stripeClient := payment.NewLiveStripeClient(stripeKey)
	dunningStore := dunning.NewPostgresStore(db)
	dunningSvc := dunning.NewService(dunningStore, nil, clk) // retrier set below after stripeAdapter created
	dunningH := dunning.NewHandler(dunningSvc, dunning.HandlerDeps{
		Invoices:       invoiceStore,
		CreditReverser: creditSvc,
		PaymentCancel:  payment.NewLiveStripeClient(stripeKey),
	})

	// Per-tenant circuit breaker around Stripe calls. One tenant's broken
	// integration — or a Stripe regional incident — must not let retries
	// from the scheduler burn request budget for every other tenant. The
	// breaker opens after 5 consecutive Unknown (5xx/timeout/network)
	// failures, probes after 30s, and emits state transitions to the
	// velox_stripe_breaker_state gauge so operators can alert on it.
	stripeBreaker := breaker.New(breaker.Config{
		FailureThreshold: 5,
		Cooldown:         30 * time.Second,
		Interval:         60 * time.Second,
		Countable:        payment.IsUnknownPaymentFailure,
		OnStateChange: func(tenantID string, _, to breaker.State) {
			mw.RecordStripeBreakerState(tenantID, string(to))
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
	webhookH := payment.NewHandler(stripeAdapter, stripeWebhookSecret)

	invoiceSvc := invoice.NewService(invoiceStore, clk, settingsStore)
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
	})
	checkoutH := payment.NewCheckoutHandler(stripeKey,
		strings.TrimSpace(os.Getenv("STRIPE_CHECKOUT_SUCCESS_URL")),
		strings.TrimSpace(os.Getenv("STRIPE_CHECKOUT_CANCEL_URL")),
		customerStore)
	portalH := payment.NewPortalHandler(stripeKey, customerStore)

	// Token service for public payment update links
	tokenSvc := payment.NewTokenService(db)
	stripeAdapter.SetTokenService(tokenSvc)

	// Reconciler for PaymentUnknown invoices (ambiguous Stripe outcomes).
	// 60s cool-off lets webhooks resolve the state naturally before we
	// spend an extra API call.
	paymentReconciler := payment.NewReconciler(stripeClient, invoiceStore, 60*time.Second)
	paymentReconciler.SetBreaker(stripeBreaker)
	stripeBreakerH := payment.NewBreakerAdminHandler(stripeBreaker)
	publicPaymentH := payment.NewPublicPaymentHandler(tokenSvc, db, stripeKey,
		strings.TrimSpace(os.Getenv("PAYMENT_UPDATE_RETURN_URL")))

	// Email sender
	emailSender := email.NewSender()
	invoiceH.SetEmailSender(emailSender)
	customerEmailAdapter := &customerEmailFetcherAdapter{store: customerStore}
	dunningSvc.SetEmailNotifier(emailSender)
	dunningSvc.SetCustomerEmailFetcher(customerEmailAdapter)
	stripeAdapter.SetEmailReceipt(emailSender, customerEmailAdapter)
	paymentUpdateURL := strings.TrimSpace(os.Getenv("PAYMENT_UPDATE_URL"))
	if paymentUpdateURL != "" {
		stripeAdapter.SetEmailPaymentUpdate(emailSender, customerEmailAdapter, paymentUpdateURL)
	}

	// Audit logging for financial operations
	creditH.SetAuditLogger(auditLogger)
	invoiceH.SetAuditLogger(auditLogger)
	subH.SetAuditLogger(auditLogger)
	creditNoteH.SetAuditLogger(auditLogger)

	// Feature flags (created before billing engine to gate Stripe Tax)
	featureSvc := feature.NewService(feature.NewPostgresStore(db))
	featureH := feature.NewHandler(featureSvc)

	// Billing engine + manual trigger (with credit auto-application)
	engine := billing.NewEngine(subStore, usageStore, pricingSvc,
		&invoiceWriterAdapter{store: invoiceStore}, creditSvc, settingsStore, customerStore, stripeAdapter, clk, customerStore)

	// Tax calculator: use Stripe Tax when enabled via feature flag, otherwise manual
	manualTaxCalc := tax.NewManualCalculator(0, "") // rate resolved per-subscription at billing time
	if stripeKey != "" && featureSvc.IsEnabled(context.Background(), "billing.stripe_tax", "") {
		slog.Info("stripe tax enabled, using Stripe Tax calculator with manual fallback")
		engine.SetTaxCalculator(tax.NewStripeCalculator(stripeKey, manualTaxCalc))
	} else {
		// ManualCalculator with rate 0 is a no-op — the engine still reads
		// tenant/customer tax rates and passes them into the calculator.
		// We leave the taxCalc nil here so the engine uses its inline legacy path
		// which resolves rates from settings + billing profiles per subscription.
		slog.Info("using manual tax calculation (inline)")
	}

	// Coupon discount applier: billing engine consults redemptions at finalize time.
	engine.SetCouponApplier(couponSvc)

	// Proration invoices now share the billing engine's tax resolution path so
	// plan upgrades aren't silently tax-free. The adapter translates between
	// billing.TaxApplication and subscription.ProrationTaxResult.
	subH.SetProrationTaxApplier(&prorationTaxApplierAdapter{engine: engine})

	billingH := billing.NewHandler(engine, subStore)
	analyticsH := analytics.NewHandler(db)

	// GDPR data export + deletion — wired into customer handler
	gdprSvc := customer.NewGDPRService(customerStore, invoiceStore, creditStore, subStore, auditLogger)
	customerH.SetGDPR(customer.NewGDPRHandler(gdprSvc))

	s := &Server{
		BillingEngine:     engine,
		DunningSvc:        dunningSvc,
		SettingsStore:     settingsStore,
		WebhookOutSvc:     webhookOutSvc,
		OutboxStore:       outboxStore,
		OutboxEnabled:     outboxEnabled,
		CreditSvc:         creditSvc,
		InvoiceSvc:        invoiceSvc,
		TokenSvc:          tokenSvc,
		PaymentReconciler: paymentReconciler,
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

	// Stripe webhooks — no API key auth (verified by signature)
	r.Mount("/v1/webhooks", webhookH.Routes())

	// Public payment update — no auth (validated by token)
	if publicPaymentH != nil {
		r.Mount("/v1/public/payment-updates", publicPaymentH.Routes())
	}

	// Platform routes
	r.Route("/v1/tenants", func(r chi.Router) {
		r.Use(auth.Middleware(authSvc))
		r.Use(auth.Require(auth.PermTenantWrite))
		r.Mount("/", tenantH.Routes())
	})

	// Tenant-scoped routes
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Middleware(authSvc))
		r.Use(rateLimiter.Middleware()) // After auth so tenant ID is available for bucket key
		r.Use(mw.Idempotency(db))
		r.Use(mw.AuditLog(db, settingsStore))

		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/api-keys", authH.Routes())
		r.With(auth.Require(auth.PermCustomerRead)).Mount("/customers", customerH.Routes())
		r.With(auth.Require(auth.PermPricingRead)).Mount("/meters", pricingH.MeterRoutes())
		r.With(auth.Require(auth.PermPricingRead)).Mount("/plans", pricingH.PlanRoutes())
		r.With(auth.Require(auth.PermPricingRead)).Mount("/rating-rules", pricingH.RatingRuleRoutes())
		r.With(auth.Require(auth.PermSubscriptionRead)).Mount("/subscriptions", subH.Routes())
		r.With(auth.Require(auth.PermUsageRead)).Mount("/usage-events", usageH.Routes())
		r.With(auth.Require(auth.PermInvoiceRead)).Mount("/invoices", invoiceH.Routes())
		r.With(auth.Require(auth.PermInvoiceWrite)).Mount("/credit-notes", creditNoteH.Routes())
		r.With(auth.Require(auth.PermPricingWrite)).Mount("/price-overrides", pricingH.OverrideRoutes())
		r.With(auth.Require(auth.PermPricingWrite)).Mount("/coupons", couponH.Routes())
		r.With(auth.Require(auth.PermCustomerWrite)).Mount("/credits", creditH.Routes())
		r.With(auth.Require(auth.PermDunningRead)).Mount("/dunning", dunningH.Routes())
		r.With(auth.Require(auth.PermInvoiceWrite)).Mount("/billing", billingH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/webhook-endpoints", webhookOutH.Routes())
		r.With(auth.Require(auth.PermAPIKeyRead)).Mount("/audit-log", auditH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/settings", settingsH.Routes())
		r.With(auth.Require(auth.PermInvoiceRead)).Mount("/analytics", analyticsH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/feature-flags", featureH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/integrations/stripe/breaker", stripeBreakerH.Routes())
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
