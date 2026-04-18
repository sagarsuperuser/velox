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
	"strings"
	"syscall"
	"time"

	neturl "net/url"

	"github.com/sagarsuperuser/velox/internal/api"
	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/config"
	"github.com/sagarsuperuser/velox/internal/platform/migrate"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/platform/telemetry"
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

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
		// Migrations need DDL privileges — use DATABASE_URL (superuser) if
		// APP_DATABASE_URL is set (meaning DATABASE_URL is the admin connection).
		migrationPool := pool
		if appURL := strings.TrimSpace(os.Getenv("APP_DATABASE_URL")); appURL != "" {
			// DATABASE_URL is already the superuser connection — use it for migrations
			slog.Info("running migrations with admin connection")
		}
		if err := migrate.Up(migrationPool); err != nil {
			slog.Error("run migrations", "error", err)
			os.Exit(1)
		}
		slog.Info("migrations applied")
	}

	// Use a non-superuser connection for the app so RLS policies are enforced.
	// APP_DATABASE_URL takes priority; otherwise auto-derive from DATABASE_URL
	// by replacing the credentials with velox_app/velox_app.
	appPool := pool
	appURL := strings.TrimSpace(os.Getenv("APP_DATABASE_URL"))
	if appURL == "" {
		appURL = deriveAppURL(cfg.DB.URL)
	}
	if appURL != "" && appURL != cfg.DB.URL {
		appCfg := cfg.DB
		appCfg.URL = appURL
		appPool, err = config.OpenPostgres(appCfg)
		if err != nil {
			slog.Warn("could not open app database connection, falling back to admin connection",
				"error", err)
			appPool = pool
		} else {
			defer func() { _ = appPool.Close() }()
			slog.Info("using app database connection (RLS enforced)")
		}
	} else {
		slog.Warn("running with admin database connection — RLS policies will NOT be enforced. Set APP_DATABASE_URL or create the velox_app role.")
	}

	db := postgres.NewDB(appPool, cfg.DB.QueryTimeout)
	webhookSecret := strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET")) // set by injectSecret above

	server := api.NewServer(db, webhookSecret)

	billingInterval := 1 * time.Hour
	if cfg.Env == "local" {
		billingInterval = 5 * time.Minute
	}
	scheduler := billing.NewScheduler(server.BillingEngine, billingInterval, 50, server.DunningSvc, server.SettingsStore, server.CreditSvc)
	scheduler.SetReminders(server.InvoiceSvc)
	if server.TokenSvc != nil {
		scheduler.SetTokenCleaner(server.TokenSvc)
	}

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

	go scheduler.Start(ctx)
	go server.WebhookOutSvc.StartRetryWorker(ctx, 30*time.Second)

	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

// openDB loads only the database config (skips Stripe/Redis/encryption
// validation that's irrelevant for migrate commands).
func openDB() *sql.DB {
	cfg, err := config.LoadDBOnly()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	pool, err := config.OpenPostgres(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open database: %v\n", err)
		os.Exit(1)
	}
	return pool
}

func runMigrate() {
	pool := openDB()
	defer func() { _ = pool.Close() }()
	if err := migrate.Up(pool); err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("migrations applied successfully")
}

func runMigrateStatus() {
	pool := openDB()
	defer func() { _ = pool.Close() }()
	m, err := migrate.New(pool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status failed: %v\n", err)
		os.Exit(1)
	}
	v, dirty, err := m.Version()
	if err != nil {
		fmt.Println("no migrations applied")
		return
	}
	fmt.Printf("version: %d, dirty: %v\n", v, dirty)
}

func runMigrateRollback(_ string) {
	pool := openDB()
	defer func() { _ = pool.Close() }()
	m, err := migrate.New(pool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rollback failed: %v\n", err)
		os.Exit(1)
	}
	if err := m.Steps(-1); err != nil {
		fmt.Fprintf(os.Stderr, "rollback failed: %v\n", err)
		os.Exit(1)
	}
	v, _, _ := m.Version()
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
