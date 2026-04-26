package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	neturl "net/url"

	"github.com/sagarsuperuser/velox/internal/api"
	mw "github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/config"
	"github.com/sagarsuperuser/velox/internal/email"
	"github.com/sagarsuperuser/velox/internal/platform/migrate"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/platform/telemetry"
	"github.com/sagarsuperuser/velox/internal/webhook"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	subcmd := ""
	if len(os.Args) > 2 {
		subcmd = os.Args[2]
	}

	switch cmd {
	case "serve":
		serve()
	case "migrate":
		switch subcmd {
		case "status":
			runMigrateStatus()
		case "rollback":
			runMigrateRollback(subcmd)
		case "":
			runMigrate()
		default:
			fmt.Fprintf(os.Stderr, "unknown migrate subcommand: %s\navailable: status, rollback\n", subcmd)
			os.Exit(1)
		}
	case "version":
		fmt.Println("velox 2026-04-07")
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func serve() {
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(telemetry.NewContextHandler(jsonHandler)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	// Initialize OpenTelemetry tracing (noop if OTEL_EXPORTER_OTLP_ENDPOINT not set)
	tracingShutdown, err := telemetry.Init(context.Background())
	if err != nil {
		slog.Error("init tracing", "error", err)
		os.Exit(1)
	}
	defer func() { _ = tracingShutdown(context.Background()) }()

	slog.Info("velox starting", "env", cfg.Env, "port", cfg.Port)

	pool, err := config.OpenPostgres(cfg.DB)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer func() { _ = pool.Close() }()

	if cfg.Migrate {
		if cfg.Env == "production" {
			slog.Warn("RUN_MIGRATIONS_ON_BOOT=true in production — prefer a dedicated migration Job before rollout")
		}
		if err := migrate.Up(cfg.DB.URL); err != nil {
			slog.Error("run migrations", "error", err)
			os.Exit(1)
		}
	}

	// Refuse to start if schema is behind what this binary expects — catches
	// missed migration Jobs, races during rolling deploys, and dirty migrations.
	if err := migrate.CheckSchemaReady(pool); err != nil {
		slog.Error("schema check failed", "error", err)
		os.Exit(1)
	}

	appPool, closeAppPool := openAppPool(cfg, pool)
	defer closeAppPool()

	db := postgres.NewDB(appPool, cfg.DB.QueryTimeout)

	server := api.NewServer(db, nil)

	billingInterval := 1 * time.Hour
	if cfg.Env == "local" {
		billingInterval = 5 * time.Minute
	}
	scheduler := billing.NewScheduler(server.BillingEngine, billingInterval, 50, server.DunningSvc, server.SettingsStore, nil, server.CreditSvc)
	scheduler.SetReminders(server.InvoiceSvc)
	if server.TokenSvc != nil {
		scheduler.SetTokenCleaner(server.TokenSvc)
	}
	scheduler.SetIdempotencyCleaner(mw.NewIdempotencyCleaner(db))
	if server.PaymentReconciler != nil {
		scheduler.SetPaymentReconciler(server.PaymentReconciler)
	}
	// Leader gating: each replica tries the billing / dunning advisory locks
	// per tick; the winner runs the work, the losers skip. On crash the TCP
	// session drops and Postgres auto-releases — no zombie locks.
	scheduler.SetLocker(billing.NewPostgresLocker(db), postgres.LockKeyBillingScheduler, postgres.LockKeyDunningScheduler)

	// Wire scheduler health tracking so /health/ready can detect stalled schedulers
	api.SetSchedulerInterval(billingInterval)
	scheduler.SetOnRun(api.RecordSchedulerRun)

	srv := &http.Server{
		Addr:           fmt.Sprintf(":%s", cfg.Port),
		Handler:        server,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 13, // 8 KB
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Track background workers so shutdown can wait for them to finish their
	// current tick before the process exits. Without this, a SIGTERM during a
	// billing run could leave partial work — half-generated invoices, ledger
	// entries without matching invoice updates, in-flight webhook deliveries.
	var workers sync.WaitGroup

	workers.Add(2)
	go func() {
		defer workers.Done()
		scheduler.Start(ctx)
	}()
	go func() {
		defer workers.Done()
		server.WebhookOutSvc.StartRetryWorker(ctx, 30*time.Second)
	}()

	// Outbox dispatcher: drains webhook_outbox → Service.Dispatch. Only runs
	// when the outbox producer path is enabled (VELOX_WEBHOOK_OUTBOX_ENABLED
	// is unset or "true"); when legacy direct-dispatch is selected, rows are
	// never enqueued so the worker would poll against an empty table forever.
	if server.OutboxEnabled {
		workers.Add(1)
		go func() {
			defer workers.Done()
			dispatcher := webhook.NewDispatcher(server.OutboxStore, server.WebhookOutSvc, webhook.DispatcherConfig{})
			dispatcher.SetLocker(server.OutboxStore)
			dispatcher.Start(ctx)
		}()
	}

	// Email dispatcher: drains email_outbox → *email.Sender. Mirrors the
	// webhook outbox worker. Only runs when the email outbox producer path
	// is enabled (VELOX_EMAIL_OUTBOX_ENABLED is unset or "true").
	if server.EmailOutboxEnabled {
		workers.Add(1)
		go func() {
			defer workers.Done()
			dispatcher := email.NewDispatcher(server.EmailOutboxStore, server.EmailSender, email.DispatcherConfig{})
			dispatcher.SetLocker(server.EmailOutboxStore)
			dispatcher.Start(ctx)
		}()
	}

	// Billing alerts evaluator: scans armed alerts on a tick, fires
	// `billing.alert.triggered` via the webhook outbox atomically with
	// the alert state mutation. Leader-gated by
	// LockKeyBillingAlertEvaluator so multi-replica deploys don't
	// double-emit. See docs/design-billing-alerts.md.
	if server.BillingAlertEvaluator != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			server.BillingAlertEvaluator.Start(ctx)
		}()
	}

	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down — draining workers and in-flight requests")

	// Stop accepting new HTTP requests first, then drain in-flight ones.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown error", "error", err)
	}

	// Wait for scheduler + webhook worker to return. Both exit cleanly when
	// their context (ctx above, now cancelled) is done, but a mid-tick run
	// might still be completing. Bound the wait so a hung worker doesn't
	// prevent exit indefinitely.
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		slog.Info("workers drained cleanly")
	case <-time.After(30 * time.Second):
		slog.Warn("workers did not drain within timeout — forcing exit")
	}
}

// loadDSN loads just the database URL for migrate subcommands. Skips
// Stripe/Redis/encryption validation that's irrelevant for migrate work.
func loadDSN() string {
	cfg, err := config.LoadDBOnly()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	return cfg.URL
}

func runMigrate() {
	if err := migrate.Up(loadDSN()); err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("migrations applied successfully")
}

func runMigrateStatus() {
	v, dirty, err := migrate.Status(loadDSN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "status failed: %v\n", err)
		os.Exit(1)
	}
	if v == 0 {
		fmt.Println("no migrations applied")
		return
	}
	fmt.Printf("version: %d, dirty: %v\n", v, dirty)
}

func runMigrateRollback(_ string) {
	v, err := migrate.Rollback(loadDSN(), 1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rollback failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("rolled back to version %d\n", v)
}

func printUsage() {
	fmt.Println(`velox — usage-based billing engine

Commands:
  serve                Start the API server (default)
  migrate              Apply pending migrations
  migrate status       Show current migration version
  migrate rollback     Rollback the latest migration
  version              Print version
  help                 Show this help

Environment:
  DATABASE_URL              PostgreSQL connection string (required, used for migrations if APP_DATABASE_URL set)
  APP_DATABASE_URL          App database connection (least-privilege, used at runtime)
  PORT                      HTTP port (default: 8080)
  APP_ENV                   Environment: local, staging, production (default: local)
  RUN_MIGRATIONS_ON_BOOT    Run migrations on server start (default: false)
  STRIPE_WEBHOOK_SECRET     Stripe webhook signing secret
  VELOX_BOOTSTRAP_TOKEN     Token for POST /v1/bootstrap endpoint
  PAYMENT_UPDATE_URL        Base URL for payment update page (e.g. https://app.example.com/update-payment)
  VELOX_ENCRYPTION_KEY      64-char hex key for PII encryption at rest
  REDIS_URL                 Redis URL for distributed rate limiting`)
}

// deriveAppURL replaces the user:password in a DATABASE_URL with velox_app:velox_app.
// Returns "" if the URL can't be parsed.
func deriveAppURL(adminURL string) string {
	u, err := neturl.Parse(adminURL)
	if err != nil {
		return ""
	}
	u.User = neturl.UserPassword("velox_app", "velox_app")
	return u.String()
}

// openAppPool returns the non-superuser connection used for request-time
// queries (where RLS must be enforced). The app-role URL is derived from
// DATABASE_URL by swapping its credentials with velox_app/velox_app — so
// operators only configure one URL, and the velox_app role must exist in
// the database. If derivation fails, or the app-role pool can't be opened,
// falls back to the admin pool with a loud warning — RLS NOT enforced in
// that mode. The returned cleanup is a noop when falling back.
func openAppPool(cfg config.Config, adminPool *sql.DB) (*sql.DB, func()) {
	noop := func() {}

	appURL := deriveAppURL(cfg.DB.URL)
	if appURL == "" || appURL == cfg.DB.URL {
		slog.Warn("running with admin database connection — RLS NOT enforced. Create the velox_app role.")
		return adminPool, noop
	}

	appCfg := cfg.DB
	appCfg.URL = appURL
	appPool, err := config.OpenPostgres(appCfg)
	if err != nil {
		slog.Warn("could not open app database connection, falling back to admin", "error", err)
		return adminPool, noop
	}
	slog.Info("using app database connection (RLS enforced)")
	return appPool, func() { _ = appPool.Close() }
}
