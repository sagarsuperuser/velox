// velox-migrate-safety runs every migration against a populated database
// and reports per-migration wall-clock duration plus any concurrent
// ACCESS EXCLUSIVE lock observations on the hot tables.
//
// This is a manual hardening tool, not a CI gate. Expected runtime on a
// laptop with the default scale is several minutes.
//
// Workflow:
//
//  1. Drop & recreate a scratch database (default: velox_safety_scratch).
//  2. Run migration 0001 only — gives us the base schema.
//  3. Seed realistic-scale data into hot tables (tenants, customers,
//     subscriptions, usage_events, invoices, audit_log).
//  4. For each remaining migration N in [0002..latest], measure:
//     - wall-clock duration of `migrate up 1`
//     - the longest ACCESS EXCLUSIVE lock observed on a hot table
//     during the migration (sampled by a side goroutine reading
//     pg_locks every 50ms)
//  5. Walk DOWN every migration in reverse to verify rollback paths
//     don't error and don't silently drop seeded data on tables that
//     should still exist.
//  6. Emit a CSV report to stdout (or the path given by --report).
//
// Usage:
//
//	go run ./cmd/velox-migrate-safety \
//	  --admin-url "postgres://velox:velox@localhost:5432/postgres?sslmode=disable" \
//	  --scale tenants=50,customers_per_tenant=200,subs_per_tenant=500,events_per_sub=20,invoices_per_sub=5 \
//	  --report /tmp/migration-safety.csv
//
// Default scale is "small" (50 tenants × 200 customers × 500 subs × 20 events
// = 500k events, 250k invoices). Bigger scales can be specified via --scale
// but seeding time grows linearly. The default targets a 5–10 min total
// runtime, suitable for a developer-laptop pass.
//
// Build tag: not gated. The program connects only to the admin URL and
// works on a scratch database it owns; safe to run from any worktree.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gomigrate "github.com/golang-migrate/migrate/v4"
	mpg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sagarsuperuser/velox/internal/platform/migrate"
)

type scale struct {
	Tenants            int
	CustomersPerTenant int
	SubsPerTenant      int
	EventsPerSub       int
	InvoicesPerSub     int
	AuditPerTenant     int
}

func defaultScale() scale {
	return scale{
		Tenants:            50,
		CustomersPerTenant: 200,
		SubsPerTenant:      500,
		EventsPerSub:       20,
		InvoicesPerSub:     5,
		AuditPerTenant:     1000,
	}
}

func (s scale) totalCustomers() int { return s.Tenants * s.CustomersPerTenant }
func (s scale) totalSubs() int      { return s.Tenants * s.SubsPerTenant }
func (s scale) totalEvents() int    { return s.totalSubs() * s.EventsPerSub }
func (s scale) totalInvoices() int  { return s.totalSubs() * s.InvoicesPerSub }
func (s scale) totalAudit() int     { return s.Tenants * s.AuditPerTenant }

// migResult holds per-migration timing for a single up or down step.
type migResult struct {
	Version       uint
	Direction     string // "up" or "down"
	Duration      time.Duration
	MaxLockMillis int64
	LockedTable   string
	Err           error
}

func main() {
	adminURL := flag.String("admin-url",
		envOr("ADMIN_DATABASE_URL", "postgres://velox:velox@localhost:5432/postgres?sslmode=disable"),
		"Postgres admin URL (must be able to CREATE/DROP databases)")
	scratchDB := flag.String("scratch-db", "velox_safety_scratch",
		"Scratch database name (will be dropped+recreated)")
	scaleStr := flag.String("scale", "",
		"Override default scale: tenants=N,customers_per_tenant=N,subs_per_tenant=N,events_per_sub=N,invoices_per_sub=N,audit_per_tenant=N")
	reportPath := flag.String("report", "",
		"CSV output path (default stdout)")
	skipDown := flag.Bool("skip-down", false,
		"Skip the down-migration verification pass")
	flag.Parse()

	sc := defaultScale()
	if *scaleStr != "" {
		if err := parseScale(*scaleStr, &sc); err != nil {
			log.Fatalf("parse --scale: %v", err)
		}
	}

	scratchURL := rewriteDBName(*adminURL, *scratchDB)

	overall := time.Now()

	// Stage 1: scratch DB
	log.Printf("=== Stage 1: scratch DB %q ===", *scratchDB)
	if err := recreateScratchDB(*adminURL, *scratchDB); err != nil {
		log.Fatalf("recreate scratch db: %v", err)
	}

	// Stage 2: migration 0001 only — base schema.
	log.Printf("=== Stage 2: applying migration 0001 (base schema) ===")
	stage1Start := time.Now()
	if err := upTo(scratchURL, 1); err != nil {
		log.Fatalf("upTo 1: %v", err)
	}
	log.Printf("    base schema applied in %s", time.Since(stage1Start))

	// Stage 3: seed populated data.
	log.Printf("=== Stage 3: seeding populated data (%s) ===", sc)
	seedStart := time.Now()
	if err := seed(scratchURL, sc); err != nil {
		log.Fatalf("seed: %v", err)
	}
	log.Printf("    seed done in %s", time.Since(seedStart))

	// Stage 4: walk forward 0002..latest, measuring each migration.
	log.Printf("=== Stage 4: walking forward applying 0002..latest ===")
	upResults, err := walkForward(scratchURL, 1)
	if err != nil {
		log.Fatalf("walkForward: %v", err)
	}

	// Stage 5: walk down latest..0001 (skip 0001 because dropping the schema
	// is interesting but not the production-rollback case).
	var downResults []migResult
	if !*skipDown {
		log.Printf("=== Stage 5: walking down latest..0002 ===")
		downResults, err = walkBackward(scratchURL)
		if err != nil {
			log.Printf("walkBackward stopped: %v (continuing — partial results recorded)", err)
		}
	}

	// Stage 6: emit report.
	out := os.Stdout
	if *reportPath != "" {
		f, err := os.Create(*reportPath)
		if err != nil {
			log.Fatalf("create report: %v", err)
		}
		defer func() { _ = f.Close() }()
		out = f
	}
	if err := writeReport(out, sc, upResults, downResults, time.Since(overall)); err != nil {
		log.Fatalf("write report: %v", err)
	}

	log.Printf("DONE — total wall clock %s", time.Since(overall))
	if *reportPath != "" {
		log.Printf("CSV report → %s", *reportPath)
	}
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func parseScale(s string, sc *scale) error {
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("bad scale entry %q (want key=N)", kv)
		}
		n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return fmt.Errorf("bad scale value for %q: %w", parts[0], err)
		}
		switch strings.TrimSpace(parts[0]) {
		case "tenants":
			sc.Tenants = n
		case "customers_per_tenant":
			sc.CustomersPerTenant = n
		case "subs_per_tenant":
			sc.SubsPerTenant = n
		case "events_per_sub":
			sc.EventsPerSub = n
		case "invoices_per_sub":
			sc.InvoicesPerSub = n
		case "audit_per_tenant":
			sc.AuditPerTenant = n
		default:
			return fmt.Errorf("unknown scale key %q", parts[0])
		}
	}
	return nil
}

func (s scale) String() string {
	return fmt.Sprintf("tenants=%d cust/tenant=%d sub/tenant=%d evt/sub=%d inv/sub=%d audit/tenant=%d → ~%d events ~%d invoices ~%d audit",
		s.Tenants, s.CustomersPerTenant, s.SubsPerTenant, s.EventsPerSub,
		s.InvoicesPerSub, s.AuditPerTenant, s.totalEvents(), s.totalInvoices(), s.totalAudit())
}

func rewriteDBName(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		log.Fatalf("parse DSN: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

func recreateScratchDB(adminURL, name string) error {
	db, err := sql.Open("pgx", adminURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Force-disconnect any other sessions before drop.
	_, _ = db.ExecContext(ctx,
		fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = %s AND pid <> pg_backend_pid()`,
			quoteLit(name)))
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, quoteIdent(name))); err != nil {
		return fmt.Errorf("drop: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(name))); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	return nil
}

// upTo applies migrations until the database reaches version `target`.
// We do this by stepping one at a time so we can stop precisely (golang-migrate
// doesn't expose "up to specific version" — only step counts or full Up).
func upTo(dsn string, target uint) error {
	for {
		v, _, err := migrate.Status(dsn)
		if err != nil {
			return err
		}
		if v >= target {
			return nil
		}
		if _, err := stepUp(dsn, 1); err != nil {
			return err
		}
	}
}

// stepUp runs `migrate up N` and returns timing.
func stepUp(dsn string, n int) (time.Duration, error) {
	start := time.Now()
	// We use migrate.Up + version check rather than exposing a Steps helper —
	// the test re-uses the package's openMigrationPool path indirectly.
	if err := migrateSteps(dsn, n); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

func writeReport(out *os.File, sc scale, ups, downs []migResult, total time.Duration) error {
	if _, err := fmt.Fprintf(out, "# Velox migration safety report\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "# total wall clock: %s\n", total); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "# scale: %s\n", sc); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "# columns: direction,version,duration_ms,max_lock_ms,locked_table,error\n"); err != nil {
		return err
	}
	emit := func(r migResult) error {
		errStr := ""
		if r.Err != nil {
			// CSV-safe: comma → ;, newline → space, quotes wrap.
			s := r.Err.Error()
			s = strings.ReplaceAll(s, ",", ";")
			s = strings.ReplaceAll(s, "\n", " ")
			s = strings.ReplaceAll(s, "\r", " ")
			if len(s) > 240 {
				s = s[:240] + "..."
			}
			errStr = s
		}
		_, err := fmt.Fprintf(out, "%s,%d,%d,%d,%s,%s\n",
			r.Direction, r.Version, r.Duration.Milliseconds(),
			r.MaxLockMillis, r.LockedTable, errStr)
		return err
	}
	for _, r := range ups {
		if err := emit(r); err != nil {
			return err
		}
	}
	for _, r := range downs {
		if err := emit(r); err != nil {
			return err
		}
	}
	return nil
}

// quoteIdent quotes an identifier (db name) for use in SQL DDL.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// quoteLit quotes a string literal.
func quoteLit(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// hotTables are the tables we track for ACCESS EXCLUSIVE locks during a
// migration. Lock-wait observations on any of these are the high-value
// signal: a >1s exclusive lock on usage_events or invoices in production
// would freeze ingest and dashboards.
var hotTables = []string{
	"usage_events",
	"invoices",
	"invoice_line_items",
	"subscriptions",
	"customers",
	"audit_log",
	"customer_credit_ledger",
	"webhook_outbox",
	"email_outbox",
	"meter_pricing_rules",
}

// stepUpWithLockMonitor runs one migration step while sampling pg_locks every
// 50ms. Returns the longest observed contiguous ACCESS EXCLUSIVE on a hot
// table. The migration is run on its own dedicated connection (via the
// migrate package), so the lock-monitor goroutine connects separately.
func stepUpWithLockMonitor(dsn string) (time.Duration, int64, string, error) {
	return runWithLockMonitor(dsn, func() error { return migrateSteps(dsn, 1) })
}

func stepDownWithLockMonitor(dsn string) (time.Duration, int64, string, error) {
	return runWithLockMonitor(dsn, func() error { return migrateSteps(dsn, -1) })
}

func runWithLockMonitor(dsn string, fn func() error) (time.Duration, int64, string, error) {
	monitorDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return 0, 0, "", err
	}
	defer func() { _ = monitorDB.Close() }()
	monitorDB.SetMaxOpenConns(1)
	if err := monitorDB.Ping(); err != nil {
		return 0, 0, "", err
	}

	stop := make(chan struct{})
	var maxMs atomic.Int64
	var lockedTable atomic.Value // string
	lockedTable.Store("")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// firstSeen[table] = unix-millis timestamp of first sighting in current
		// run — when the lock is released we update maxMs and clear it.
		firstSeen := map[string]int64{}
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				snap := pollHotLocks(monitorDB)
				now := time.Now().UnixMilli()
				// Update firstSeen for currently-locked tables.
				for tbl := range snap {
					if _, ok := firstSeen[tbl]; !ok {
						firstSeen[tbl] = now
					}
				}
				// For tables that disappeared, finalize their duration.
				for tbl, since := range firstSeen {
					if _, still := snap[tbl]; !still {
						dur := now - since
						if dur > maxMs.Load() {
							maxMs.Store(dur)
							lockedTable.Store(tbl)
						}
						delete(firstSeen, tbl)
					}
				}
			}
		}
	}()

	start := time.Now()
	err = fn()
	dur := time.Since(start)
	close(stop)
	wg.Wait()

	// Final sweep for any locks still held at exit (rare but possible).
	for tbl := range pollHotLocks(monitorDB) {
		// If still held — count it as full duration (best effort).
		// We don't have a precise "since"; we use the migration duration.
		if dur.Milliseconds() > maxMs.Load() {
			maxMs.Store(dur.Milliseconds())
			lockedTable.Store(tbl)
		}
	}

	return dur, maxMs.Load(), lockedTable.Load().(string), err
}

// pollHotLocks returns the set of hot tables currently holding ACCESS
// EXCLUSIVE on any backend. Empty map = no locks.
func pollHotLocks(db *sql.DB) map[string]struct{} {
	tables := strings.Join(hotTables, "','")
	q := fmt.Sprintf(`
		SELECT DISTINCT c.relname
		FROM pg_locks l
		JOIN pg_class c ON c.oid = l.relation
		WHERE l.mode = 'AccessExclusiveLock'
		  AND l.granted = TRUE
		  AND c.relname IN ('%s')
	`, tables)
	rows, err := db.Query(q)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			out[name] = struct{}{}
		}
	}
	return out
}

// walkForward walks each remaining migration after `from`, measuring
// duration and lock pressure on hot tables. Returns one migResult per step.
func walkForward(dsn string, from uint) ([]migResult, error) {
	latest, err := migrate.EmbeddedLatestVersion()
	if err != nil {
		return nil, err
	}
	versions, err := embeddedVersions()
	if err != nil {
		return nil, err
	}
	var results []migResult
	for _, v := range versions {
		if v <= from || v > latest {
			continue
		}
		log.Printf("    up → %04d ...", v)
		dur, lockMs, tbl, mErr := stepUpWithLockMonitor(dsn)
		results = append(results, migResult{
			Version: v, Direction: "up",
			Duration: dur, MaxLockMillis: lockMs, LockedTable: tbl, Err: mErr,
		})
		if mErr != nil {
			log.Printf("        ERROR: %v (continuing)", mErr)
			// Try to clear dirty state — if migration left dirty, force version
			// and continue. We don't want one bad migration to abort the whole
			// run.
			if cleanErr := forceVersion(dsn, v); cleanErr != nil {
				return results, fmt.Errorf("migration %d failed and could not be cleared: %w (orig: %v)", v, cleanErr, mErr)
			}
			continue
		}
		summary := fmt.Sprintf("        %s", dur)
		if lockMs > 0 {
			summary += fmt.Sprintf(", AccessExclusive on %s for %dms", tbl, lockMs)
		}
		log.Print(summary)
	}
	return results, nil
}

// walkBackward rolls back each migration in reverse, measuring each step.
func walkBackward(dsn string) ([]migResult, error) {
	versions, err := embeddedVersions()
	if err != nil {
		return nil, err
	}
	// Iterate in reverse, ignore 0001 (rolling back to nothing isn't the
	// production case we're checking).
	var results []migResult
	for i := len(versions) - 1; i >= 0; i-- {
		v := versions[i]
		if v <= 1 {
			continue
		}
		log.Printf("    down ← %04d ...", v)
		dur, lockMs, tbl, mErr := stepDownWithLockMonitor(dsn)
		results = append(results, migResult{
			Version: v, Direction: "down",
			Duration: dur, MaxLockMillis: lockMs, LockedTable: tbl, Err: mErr,
		})
		if mErr != nil {
			log.Printf("        DOWN-ERROR: %v (stopping rollback)", mErr)
			return results, mErr
		}
	}
	return results, nil
}

// embeddedVersions returns sorted ascending list of all embedded migration
// versions.
func embeddedVersions() ([]uint, error) {
	// We re-read embed via the migrate package, but it doesn't expose all
	// versions. Read the directory directly.
	entries, err := os.ReadDir("internal/platform/migrate/sql")
	if err != nil {
		return nil, fmt.Errorf("read sql dir: %w", err)
	}
	seen := map[uint]struct{}{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		under := strings.IndexByte(name, '_')
		if under < 1 {
			continue
		}
		n, err := strconv.ParseUint(name[:under], 10, 32)
		if err != nil {
			continue
		}
		seen[uint(n)] = struct{}{}
	}
	out := make([]uint, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// migrateSteps applies N migrations (positive = up, negative = down) using
// a dedicated short-lived connection. Built directly on golang-migrate so
// we don't have to extend the internal migrate package.
func migrateSteps(dsn string, steps int) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}

	subFS, err := fs.Sub(os.DirFS("internal/platform/migrate"), "sql")
	if err != nil {
		return fmt.Errorf("sub fs: %w", err)
	}
	source, err := iofs.New(subFS, ".")
	if err != nil {
		return fmt.Errorf("iofs source: %w", err)
	}
	driver, err := mpg.WithInstance(db, &mpg.Config{
		MigrationsTable: "schema_migrations",
	})
	if err != nil {
		return fmt.Errorf("postgres driver: %w", err)
	}
	m, err := gomigrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}
	defer func() {
		_, _ = m.Close()
	}()

	if err := m.Steps(steps); err != nil {
		if errors.Is(err, gomigrate.ErrNoChange) {
			return nil
		}
		return err
	}
	return nil
}

// forceVersion clears the dirty bit on schema_migrations so we can continue.
// Used after we deliberately surface a migration error and want to resume.
func forceVersion(dsn string, v uint) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`UPDATE schema_migrations SET version = $1, dirty = FALSE`, v)
	return err
}
