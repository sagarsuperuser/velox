// Bulk-seed populated data into a freshly-migrated (version 0001) database.
//
// Strategy: open a single connection, set app.bypass_rls=on so RLS is off,
// and use multi-row INSERT batches (faster than one row per statement, and
// portable; we don't use COPY because we want every row to take its
// DEFAULT-generated id from the column default rather than supplying ids
// from the client).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const batchSize = 1000

func seed(dsn string, sc scale) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	connClosed := false
	defer func() {
		if !connClosed {
			_ = conn.Close()
		}
	}()

	// Best-effort: tables in this branch don't define triggers, so this is a
	// no-op when it fails. Don't escalate.
	_, _ = conn.ExecContext(ctx, `SET LOCAL session_replication_role = 'replica'`)
	if _, err := conn.ExecContext(ctx, `SELECT set_config('app.bypass_rls', 'on', false)`); err != nil {
		return fmt.Errorf("bypass rls: %w", err)
	}

	// Deterministic IDs so re-runs are reproducible. We use 'tnt_NNNNNN'
	// (well within tenants.id varchar) and similar patterns.

	// 1. Tenants.
	tenantIDs := make([]string, sc.Tenants)
	for i := range tenantIDs {
		tenantIDs[i] = fmt.Sprintf("vlx_ten_safe%010d", i)
	}
	if err := bulkInsert(ctx, conn,
		"tenants(id, name, status)",
		tenantIDs, func(i int, id string) []any {
			return []any{id, fmt.Sprintf("Safety Tenant %d", i), "active"}
		}); err != nil {
		return fmt.Errorf("tenants: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO tenant_settings (tenant_id, default_currency, timezone)
		SELECT id, 'USD', 'UTC' FROM tenants WHERE id LIKE 'vlx_ten_safe%'`); err != nil {
		return fmt.Errorf("tenant_settings: %w", err)
	}

	// 2. Customers per tenant.
	totalCust := sc.totalCustomers()
	custIDs := make([]string, totalCust)
	custTenant := make([]string, totalCust)
	for i := 0; i < totalCust; i++ {
		t := i / sc.CustomersPerTenant
		custIDs[i] = fmt.Sprintf("vlx_cus_safe%012d", i)
		custTenant[i] = tenantIDs[t]
	}
	if err := bulkInsert(ctx, conn,
		"customers(id, tenant_id, external_id, display_name, email)",
		custIDs, func(i int, id string) []any {
			return []any{
				id,
				custTenant[i],
				fmt.Sprintf("ext_%d", i),
				fmt.Sprintf("Customer %d", i),
				fmt.Sprintf("cust+%d@test.invalid", i),
			}
		}); err != nil {
		return fmt.Errorf("customers: %w", err)
	}

	// 3. Plans per tenant (1 plan per tenant).
	planIDs := make([]string, sc.Tenants)
	for i := range planIDs {
		planIDs[i] = fmt.Sprintf("vlx_pln_safe%010d", i)
	}
	if err := bulkInsert(ctx, conn,
		"plans(id, tenant_id, code, name, currency, billing_interval, status, base_amount_cents, meter_ids)",
		planIDs, func(i int, id string) []any {
			return []any{
				id, tenantIDs[i],
				fmt.Sprintf("plan_%d", i),
				fmt.Sprintf("Plan %d", i),
				"USD", "monthly", "active", int64(2900),
				`["safety_meter"]`,
			}
		}); err != nil {
		return fmt.Errorf("plans: %w", err)
	}

	// 4. Rating rule versions per tenant.
	rrvIDs := make([]string, sc.Tenants)
	for i := range rrvIDs {
		rrvIDs[i] = fmt.Sprintf("vlx_rrv_safe%010d", i)
	}
	if err := bulkInsert(ctx, conn,
		"rating_rule_versions(id, tenant_id, rule_key, name, version, lifecycle_state, mode, currency, flat_amount_cents, graduated_tiers)",
		rrvIDs, func(i int, id string) []any {
			return []any{
				id, tenantIDs[i],
				fmt.Sprintf("rrv_%d", i),
				fmt.Sprintf("RRV %d", i),
				1, "active", "flat", "USD", int64(100), "[]",
			}
		}); err != nil {
		return fmt.Errorf("rating_rule_versions: %w", err)
	}

	// 5. Meters per tenant.
	meterIDs := make([]string, sc.Tenants)
	for i := range meterIDs {
		meterIDs[i] = fmt.Sprintf("vlx_mtr_safe%010d", i)
	}
	if err := bulkInsert(ctx, conn,
		"meters(id, tenant_id, key, name, unit, aggregation, rating_rule_version_id)",
		meterIDs, func(i int, id string) []any {
			return []any{
				id, tenantIDs[i],
				fmt.Sprintf("meter_%d", i),
				fmt.Sprintf("Meter %d", i),
				"unit", "sum", rrvIDs[i],
			}
		}); err != nil {
		return fmt.Errorf("meters: %w", err)
	}

	// 6. Subscriptions. We respect migration 0013's constraint by giving
	// each (tenant, customer, plan) at most one active subscription. Since
	// each tenant has 1 plan in the seed, that means subs_per_tenant must
	// be ≤ customers_per_tenant; if a caller asks for more subs we cap.
	subsPerTenant := sc.SubsPerTenant
	if subsPerTenant > sc.CustomersPerTenant {
		log_print("    NOTE: subs_per_tenant=%d > customers_per_tenant=%d; capping to %d to satisfy migration 0013's UNIQUE",
			subsPerTenant, sc.CustomersPerTenant, sc.CustomersPerTenant)
		subsPerTenant = sc.CustomersPerTenant
	}
	totalSubs := sc.Tenants * subsPerTenant
	subIDs := make([]string, totalSubs)
	subTenant := make([]string, totalSubs)
	subCustomer := make([]string, totalSubs)
	subPlan := make([]string, totalSubs)
	for i := 0; i < totalSubs; i++ {
		t := i / subsPerTenant
		custOff := i % subsPerTenant
		subIDs[i] = fmt.Sprintf("vlx_sub_safe%012d", i)
		subTenant[i] = tenantIDs[t]
		subCustomer[i] = custIDs[t*sc.CustomersPerTenant+custOff]
		subPlan[i] = planIDs[t]
	}
	if err := bulkInsert(ctx, conn,
		"subscriptions(id, tenant_id, code, display_name, customer_id, plan_id, status)",
		subIDs, func(i int, id string) []any {
			return []any{
				id, subTenant[i], fmt.Sprintf("sub_%d", i),
				fmt.Sprintf("Sub %d", i),
				subCustomer[i], subPlan[i], "active",
			}
		}); err != nil {
		return fmt.Errorf("subscriptions: %w", err)
	}

	// 7. Usage events. THIS is the hot one — we want a real volume here.
	totalEvents := sc.totalEvents()
	if totalEvents > 0 {
		log_print("    seeding %d usage_events ...", totalEvents)
		// Use INSERT ... SELECT generate_series for speed — beats client-side
		// row generation at this volume.
		// We pick a sub_id by mod (fast). DEFAULT id via column default.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO usage_events (id, tenant_id, customer_id, meter_id, subscription_id, quantity, properties, idempotency_key, timestamp)
			SELECT
			    'vlx_evt_safe' || lpad(rn::text, 14, '0'),
			    tenant_id,
			    customer_id,
			    (SELECT id FROM meters m WHERE m.tenant_id = x.tenant_id LIMIT 1),
			    sub_id,
			    1 + (rn %% 100),
			    jsonb_build_object('model', 'gpt-4', 'op', (rn %% 4)::text),
			    'idem-safe-' || rn,
			    now() - ((rn %% 86400) || ' seconds')::interval
			FROM (
			    SELECT
			        s.id AS sub_id, s.tenant_id, s.customer_id,
			        row_number() OVER () AS rn
			    FROM (SELECT id, tenant_id, customer_id FROM subscriptions WHERE id LIKE 'vlx_sub_safe%%') s
			    JOIN LATERAL (SELECT generate_series(1, %d)) g ON TRUE
			) x
		`, sc.EventsPerSub)); err != nil {
			return fmt.Errorf("usage_events: %w", err)
		}
	}

	// 8. Invoices per sub.
	totalInv := sc.totalInvoices()
	if totalInv > 0 {
		log_print("    seeding %d invoices ...", totalInv)
		// Each sub gets InvoicesPerSub invoices, one per consecutive month, so
		// the idempotency unique on (tenant_id, subscription_id, billing_period_start, billing_period_end)
		// is satisfied: each sub's invoices have distinct periods.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO invoices (
			    id, tenant_id, customer_id, subscription_id,
			    invoice_number, status, payment_status, currency,
			    subtotal_cents, tax_amount_cents, total_amount_cents,
			    billing_period_start, billing_period_end
			)
			SELECT
			    'vlx_inv_safe' || lpad((row_number() OVER ())::text, 14, '0'),
			    tenant_id, customer_id, sub_id,
			    'INV-safe-' || (row_number() OVER ())::text,
			    'finalized', 'succeeded', 'USD',
			    1000, 0, 1000,
			    now() - ((g + 1) || ' months')::interval,
			    now() - (g || ' months')::interval
			FROM (
			    SELECT s.id AS sub_id, s.tenant_id, s.customer_id, gs.g
			    FROM (SELECT id, tenant_id, customer_id FROM subscriptions WHERE id LIKE 'vlx_sub_safe%%') s
			    JOIN LATERAL (SELECT generate_series(1, %d) AS g) gs ON TRUE
			) x
		`, sc.InvoicesPerSub)); err != nil {
			return fmt.Errorf("invoices: %w", err)
		}
	}

	// 9. Audit log per tenant.
	if sc.AuditPerTenant > 0 {
		log_print("    seeding %d audit_log rows ...", sc.totalAudit())
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO audit_log (id, tenant_id, actor_type, actor_id, action, resource_type, resource_id, metadata)
			SELECT
			    'vlx_aud_safe' || lpad((row_number() OVER ())::text, 14, '0'),
			    t.id, 'system', 'safety-seeder', 'create', 'subscription',
			    'vlx_sub_safe' || lpad((row_number() OVER ())::text, 12, '0'),
			    '{}'::jsonb
			FROM (SELECT id FROM tenants WHERE id LIKE 'vlx_ten_safe%%') t
			JOIN LATERAL (SELECT generate_series(1, %d)) g ON TRUE
		`, sc.AuditPerTenant)); err != nil {
			return fmt.Errorf("audit_log: %w", err)
		}
	}

	// Reset session_replication_role on the held conn before releasing it.
	_, _ = conn.ExecContext(ctx, `SET LOCAL session_replication_role = 'origin'`)
	if err := conn.Close(); err != nil {
		return fmt.Errorf("close seed conn: %w", err)
	}
	connClosed = true

	// ANALYZE only — full VACUUM would update visibility map but require more
	// shared-memory than the default Docker postgres allots, and our goal is
	// just-good-enough planner stats for migration timing measurement, not
	// vacuum performance benchmarking.
	if _, err := db.ExecContext(ctx, `ANALYZE`); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	return nil
}

// bulkInsert inserts rows in batches via a multi-VALUES statement.
// `cols` is "table(col1, col2, ...)"; `mkRow(i, ids[i])` returns the values
// for row i, which must match the column count.
func bulkInsert(ctx context.Context, conn *sql.Conn, cols string, ids []string, mkRow func(int, string) []any) error {
	if len(ids) == 0 {
		return nil
	}
	// Determine column count from a sample row.
	sample := mkRow(0, ids[0])
	colCount := len(sample)

	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}

		rows := end - start
		args := make([]any, 0, rows*colCount)
		var sb strings.Builder
		sb.WriteString("INSERT INTO ")
		sb.WriteString(cols)
		sb.WriteString(" VALUES ")
		for r := 0; r < rows; r++ {
			if r > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('(')
			for c := 0; c < colCount; c++ {
				if c > 0 {
					sb.WriteByte(',')
				}
				fmt.Fprintf(&sb, "$%d", r*colCount+c+1)
			}
			sb.WriteByte(')')
			args = append(args, mkRow(start+r, ids[start+r])...)
		}
		sb.WriteString(" ON CONFLICT DO NOTHING")

		if _, err := conn.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("batch %d-%d: %w", start, end, err)
		}
	}
	return nil
}

// log_print is a thin shim so tests/scripts can swap output later if
// needed. Underscore-name keeps it visibly distinct from `log.Printf`.
func log_print(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
