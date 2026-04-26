// Command velox-import migrates Stripe data into a Velox tenant.
//
// Phase 0 supports `--resource=customers` only. The CLI surface is forward-
// compatible with later phases (subscriptions, products+prices, invoices)
// — see docs/design-stripe-importer.md.
//
// Typical usage:
//
//	DATABASE_URL=postgres://... velox-import \
//	  --tenant=ten_xxxx \
//	  --api-key=sk_test_xxxx \
//	  --resource=customers \
//	  --dry-run
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sagarsuperuser/velox/internal/config"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/importstripe"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	tenantID := flag.String("tenant", "", "target Velox tenant id (required)")
	apiKey := flag.String("api-key", "", "source Stripe secret key (required)")
	since := flag.String("since", "", "import customers created on/after this RFC3339 or YYYY-MM-DD timestamp (optional)")
	resources := flag.String("resource", "customers", "comma-separated list of resources to import (Phase 0: customers only)")
	output := flag.String("output", "", "CSV report output path (default: ./velox-import-<timestamp>.csv)")
	dryRun := flag.Bool("dry-run", false, "skip DB writes; report what would happen")
	livemodeFlag := flag.String("livemode-default", "", "true/false override for livemode (default: derived from api-key prefix)")
	flag.Parse()

	if *tenantID == "" {
		fatal("--tenant is required")
	}
	if *apiKey == "" {
		fatal("--api-key is required")
	}

	livemode, err := resolveLivemode(*apiKey, *livemodeFlag)
	if err != nil {
		fatal("%v", err)
	}

	resourceSet, err := parseResources(*resources)
	if err != nil {
		fatal("%v", err)
	}
	if !resourceSet["customers"] {
		fatal("--resource must include 'customers' (Phase 0 only supports customers)")
	}
	for r := range resourceSet {
		if r != "customers" {
			fmt.Fprintf(os.Stderr, "warning: resource %q is recognised but not implemented in Phase 0; skipping\n", r)
		}
	}

	sinceUnix, err := parseSince(*since)
	if err != nil {
		fatal("--since: %v", err)
	}

	outputPath := *output
	if outputPath == "" {
		outputPath = fmt.Sprintf("./velox-import-%s.csv", time.Now().UTC().Format("20060102-150405"))
	}

	dbCfg, err := config.LoadDBOnly()
	if err != nil {
		fatal("load db config: %v", err)
	}
	pool, err := config.OpenPostgres(dbCfg)
	if err != nil {
		fatal("open db: %v", err)
	}
	defer func() { _ = pool.Close() }()

	db := postgres.NewDB(pool, 10*time.Second)

	store := customer.NewPostgresStore(db)
	svc := customer.NewService(store)

	out, err := os.Create(outputPath)
	if err != nil {
		fatal("create report file: %v", err)
	}
	defer func() { _ = out.Close() }()

	report, err := importstripe.NewReport(out)
	if err != nil {
		fatal("init report: %v", err)
	}

	source := importstripe.NewStripeSource(*apiKey, sinceUnix)

	importer := &importstripe.CustomerImporter{
		Source:   source,
		Service:  svc,
		Lookup:   store,
		Report:   report,
		TenantID: *tenantID,
		Livemode: livemode,
		DryRun:   *dryRun,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Velox's RLS policy reads livemode from app.livemode; tag the ctx so
	// every customer.Service call lands under the right mode.
	ctx = postgres.WithLivemode(ctx, livemode)

	runErr := importer.Run(ctx)

	if err := report.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to flush report: %v\n", err)
	}

	fmt.Println("========================================")
	fmt.Println("  Velox Stripe importer — summary")
	fmt.Println("========================================")
	fmt.Printf("  Tenant:           %s\n", *tenantID)
	fmt.Printf("  Livemode:         %t\n", livemode)
	fmt.Printf("  Dry run:          %t\n", *dryRun)
	fmt.Printf("  Report file:      %s\n", outputPath)
	fmt.Println()
	fmt.Printf("  inserted:         %d\n", report.Inserted)
	fmt.Printf("  skip-equivalent:  %d\n", report.SkippedEquiv)
	fmt.Printf("  skip-divergent:   %d\n", report.SkippedDivergent)
	fmt.Printf("  errored:          %d\n", report.Errored)
	fmt.Printf("  total:            %d\n", report.Total())
	fmt.Println("========================================")

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "ERROR: import aborted: %v\n", runErr)
		os.Exit(1)
	}
	if report.Errored > 0 {
		// Per-row errors don't abort the run, but cron/CI should treat any
		// non-zero `errored` count as a failure.
		os.Exit(1)
	}
}

// resolveLivemode picks the importer's effective mode. Explicit override
// wins; otherwise derive from the api-key prefix; otherwise refuse.
func resolveLivemode(apiKey, override string) (bool, error) {
	if override != "" {
		switch strings.ToLower(strings.TrimSpace(override)) {
		case "true", "yes", "1":
			return true, nil
		case "false", "no", "0":
			return false, nil
		default:
			return false, fmt.Errorf("--livemode-default must be true or false, got %q", override)
		}
	}
	switch {
	case strings.HasPrefix(apiKey, "sk_live_"), strings.HasPrefix(apiKey, "rk_live_"):
		return true, nil
	case strings.HasPrefix(apiKey, "sk_test_"), strings.HasPrefix(apiKey, "rk_test_"):
		return false, nil
	default:
		return false, errors.New("could not derive livemode from api-key prefix; pass --livemode-default=true|false explicitly")
	}
}

func parseResources(s string) (map[string]bool, error) {
	out := make(map[string]bool)
	for _, r := range strings.Split(s, ",") {
		r = strings.TrimSpace(strings.ToLower(r))
		if r == "" {
			continue
		}
		switch r {
		case "customers", "subscriptions", "products", "prices", "invoices":
			out[r] = true
		default:
			return nil, fmt.Errorf("unknown resource %q (valid: customers, subscriptions, products, prices, invoices)", r)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("--resource must list at least one resource")
	}
	return out, nil
}

// parseSince accepts RFC3339 or YYYY-MM-DD. Returns 0 for empty input.
func parseSince(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Unix(), nil
	}
	return 0, fmt.Errorf("could not parse %q as RFC3339 or YYYY-MM-DD", s)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
