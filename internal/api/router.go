package api

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	mw "github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/creditnote"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/dunning"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/payment"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/usage"
	"github.com/sagarsuperuser/velox/internal/webhook"
)

type Server struct {
	router chi.Router

	// Exported for main.go to wire the billing scheduler
	BillingEngine *billing.Engine
}

func NewServer(db *postgres.DB, stripeWebhookSecret string) *Server {
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
	customerH := customer.NewHandler(customer.NewService(customer.NewPostgresStore(db)))
	pricingH := pricing.NewHandler(pricing.NewService(pricingStore))
	subH := subscription.NewHandler(subscription.NewService(subStore))
	usageH := usage.NewHandler(usage.NewService(usageStore))
	invoiceH := invoice.NewHandler(invoice.NewService(invoiceStore))
	dunningH := dunning.NewHandler(dunning.NewService(dunning.NewPostgresStore(db), nil))
	creditNoteH := creditnote.NewHandler(creditnote.NewService(creditnote.NewPostgresStore(db), invoiceStore))
	creditH := credit.NewHandler(credit.NewService(credit.NewPostgresStore(db)))
	webhookOutH := webhook.NewHandler(webhook.NewService(webhook.NewPostgresStore(db), nil))
	auditLogger := audit.NewLogger(db)
	auditH := audit.NewHandler(auditLogger)
	settingsH := tenant.NewSettingsHandler(tenant.NewSettingsStore(db))

	// Payment / webhook / checkout handlers
	stripeKey := strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY"))
	stripeClient := payment.NewLiveStripeClient(stripeKey)
	stripeAdapter := payment.NewStripe(stripeClient, invoiceStore, webhookStore)
	webhookH := payment.NewHandler(stripeAdapter, stripeWebhookSecret)
	checkoutH := payment.NewCheckoutHandler(stripeKey,
		strings.TrimSpace(os.Getenv("STRIPE_CHECKOUT_SUCCESS_URL")),
		strings.TrimSpace(os.Getenv("STRIPE_CHECKOUT_CANCEL_URL")))

	// Billing engine + manual trigger
	engine := billing.NewEngine(subStore, usageStore, pricingStore,
		&invoiceWriterAdapter{store: invoiceStore})
	billingH := billing.NewHandler(engine, subStore)

	s := &Server{
		BillingEngine: engine,
	}

	rateLimiter := mw.NewRateLimiter(100, time.Minute)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(mw.CORS([]string{"*"})) // Configure per-environment in production
	r.Use(mw.Metrics())
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(rateLimiter.Middleware())

	// Public
	r.Get("/health", handleHealth)
	r.Get("/health/ready", handleDeepHealth(db))
	r.Handle("/metrics", mw.MetricsHandler())

	// Bootstrap — one-time setup (no auth, only works when no tenants exist)
	bootstrapH := tenant.NewBootstrapHandler(db)
	r.Mount("/v1/bootstrap", bootstrapH.Routes())

	// Stripe webhooks — no API key auth (verified by signature)
	r.Mount("/v1/webhooks", webhookH.Routes())

	// Platform routes
	r.Route("/v1/tenants", func(r chi.Router) {
		r.Use(auth.Middleware(authSvc))
		r.Use(auth.Require(auth.PermTenantWrite))
		r.Mount("/", tenantH.Routes())
	})

	// Tenant-scoped routes
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Middleware(authSvc))
		r.Use(mw.Idempotency(db))

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
		r.With(auth.Require(auth.PermCustomerWrite)).Mount("/credits", creditH.Routes())
		r.With(auth.Require(auth.PermDunningRead)).Mount("/dunning", dunningH.Routes())
		r.With(auth.Require(auth.PermInvoiceWrite)).Mount("/billing", billingH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/webhook-endpoints", webhookOutH.Routes())
		r.With(auth.Require(auth.PermAPIKeyRead)).Mount("/audit-log", auditH.Routes())
		r.With(auth.Require(auth.PermAPIKeyWrite)).Mount("/settings", settingsH.Routes())
		r.With(auth.Require(auth.PermUsageRead)).Mount("/usage-summary", usageH.SummaryRoutes())
		if checkoutH != nil {
			r.With(auth.Require(auth.PermCustomerWrite)).Mount("/checkout", checkoutH.Routes())
		}

		// Customer portal — consolidated views across domains
		portal := newCustomerPortalHandler(subStore, invoiceStore, usageStore)
		r.With(auth.Require(auth.PermCustomerRead)).Mount("/customer-portal", portal.Routes())
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

		checks := map[string]string{"api": "ok"}

		if err := db.Pool.PingContext(ctx); err != nil {
			checks["database"] = "error: " + err.Error()
			respond.JSON(w, r, http.StatusServiceUnavailable, map[string]any{
				"status": "degraded",
				"checks": checks,
			})
			return
		}
		checks["database"] = "ok"

		respond.JSON(w, r, http.StatusOK, map[string]any{
			"status": "ok",
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
