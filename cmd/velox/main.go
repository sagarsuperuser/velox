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
		case "dry-run":
			runMigrateDryRun()
		case "rollback":
			if len(os.Args) < 4 {
				fmt.Fprintf(os.Stderr, "usage: velox migrate rollback <version>\n")
				fmt.Fprintf(os.Stderr, "example: velox migrate rollback 0019_enterprise_hardening\n")
				os.Exit(1)
			}
			runMigrateRollback(os.Args[3])
		case "":
			runMigrate()
		default:
			fmt.Fprintf(os.Stderr, "unknown migrate subcommand: %s\n", subcmd)
			fmt.Fprintf(os.Stderr, "available: status, dry-run, rollback\n")
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
	defer tracingShutdown(context.Background())

	slog.Info("velox starting", "env", cfg.Env, "port", cfg.Port)

	pool, err := config.OpenPostgres(cfg.DB)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if cfg.Migrate {
		// Migrations need DDL privileges — use DATABASE_URL (superuser) if
		// APP_DATABASE_URL is set (meaning DATABASE_URL is the admin connection).
		migrationPool := pool
		if appURL := strings.TrimSpace(os.Getenv("APP_DATABASE_URL")); appURL != "" {
			// DATABASE_URL is already the superuser connection — use it for migrations
			slog.Info("running migrations with admin connection")
		}
		if err := migrate.NewRunner(migrationPool).Run(context.Background()); err != nil {
			slog.Error("run migrations", "error", err)
			os.Exit(1)
		}
		slog.Info("migrations applied")
	}

	// If APP_DATABASE_URL is set, switch to it for the app (least-privilege)
	appPool := pool
	if appURL := strings.TrimSpace(os.Getenv("APP_DATABASE_URL")); appURL != "" {
		appCfg := cfg.DB
		appCfg.URL = appURL
		appPool, err = config.OpenPostgres(appCfg)
		if err != nil {
			slog.Error("open app database", "error", err)
			os.Exit(1)
		}
		defer appPool.Close()
		slog.Info("using separate app database connection (least-privilege)")
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
	defer pool.Close()
	if err := migrate.NewRunner(pool).Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("migrations applied successfully")
}

func runMigrateStatus() {
	pool := openDB()
	defer pool.Close()
	statuses, err := migrate.NewRunner(pool).Status(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "status failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%-45s %-10s %s\n", "MIGRATION", "STATUS", "APPLIED AT")
	fmt.Printf("%-45s %-10s %s\n", "---------", "------", "----------")
	for _, s := range statuses {
		status := "pending"
		appliedAt := ""
		if s.Applied {
			status = "applied"
			if !s.AppliedAt.IsZero() {
				appliedAt = s.AppliedAt.Format("2006-01-02 15:04:05")
			}
		}
		fmt.Printf("%-45s %-10s %s\n", s.Version, status, appliedAt)
	}
}

func runMigrateDryRun() {
	pool := openDB()
	defer pool.Close()
	pending, err := migrate.NewRunner(pool).DryRun(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "dry-run failed: %v\n", err)
		os.Exit(1)
	}
	if len(pending) == 0 {
		fmt.Println("no pending migrations")
		return
	}
	fmt.Printf("%d pending migration(s):\n", len(pending))
	for _, p := range pending {
		fmt.Printf("  - %s\n", p)
	}
}

func runMigrateRollback(version string) {
	pool := openDB()
	defer pool.Close()
	if err := migrate.NewRunner(pool).Rollback(context.Background(), version); err != nil {
		fmt.Fprintf(os.Stderr, "rollback failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("rolled back: %s\n", version)
}

func printUsage() {
	fmt.Println(`velox — usage-based billing engine

Commands:
  serve                    Start the API server (default)
  migrate                  Run pending database migrations
  migrate status           Show applied/pending migrations
  migrate dry-run          List pending migrations without applying
  migrate rollback <ver>   Rollback a specific migration (e.g. 0019_enterprise_hardening)
  version                  Print version
  help                     Show this help

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

