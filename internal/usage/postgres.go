package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Ingest(ctx context.Context, tenantID string, event domain.UsageEvent) (domain.UsageEvent, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.UsageEvent{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_evt")

	origin := string(event.Origin)
	if origin == "" {
		origin = string(domain.UsageOriginAPI)
	}

	props, err := propertiesJSON(event.Dimensions)
	if err != nil {
		return domain.UsageEvent{}, err
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO usage_events (id, tenant_id, customer_id, meter_id, subscription_id,
			quantity, properties, idempotency_key, timestamp, origin)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, tenant_id, customer_id, meter_id, COALESCE(subscription_id,''),
			quantity, properties, COALESCE(idempotency_key,''), timestamp, origin
	`, id, tenantID, event.CustomerID, event.MeterID,
		postgres.NullableString(event.SubscriptionID), event.Quantity,
		props, postgres.NullableString(event.IdempotencyKey),
		event.Timestamp, origin,
	).Scan(&event.ID, &event.TenantID, &event.CustomerID, &event.MeterID,
		&event.SubscriptionID, &event.Quantity, propertiesScanner{&event.Dimensions},
		&event.IdempotencyKey, &event.Timestamp, &event.Origin)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.UsageEvent{}, fmt.Errorf("%w: idempotency_key %q", errs.ErrDuplicateKey, event.IdempotencyKey)
		}
		return domain.UsageEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.UsageEvent{}, err
	}
	return event, nil
}

func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]domain.UsageEvent, int, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, 0, err
	}
	defer postgres.Rollback(tx)

	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	where, args := buildUsageWhere(filter)

	var total int
	countQuery := `SELECT COUNT(*) FROM usage_events` + where
	if err := tx.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT id, tenant_id, customer_id, meter_id, COALESCE(subscription_id,''),
		quantity, properties, COALESCE(idempotency_key,''), timestamp
		FROM usage_events` + where + ` ORDER BY timestamp DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1) + ` OFFSET $` + fmt.Sprintf("%d", len(args)+2)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var events []domain.UsageEvent
	for rows.Next() {
		var e domain.UsageEvent
		if err := rows.Scan(&e.ID, &e.TenantID, &e.CustomerID, &e.MeterID,
			&e.SubscriptionID, &e.Quantity, propertiesScanner{&e.Dimensions},
			&e.IdempotencyKey, &e.Timestamp); err != nil {
			return nil, 0, err
		}
		events = append(events, e)
	}
	return events, total, rows.Err()
}

// Aggregate returns the totals + per-meter breakdown that powers the
// /usage page's stat cards + "Usage by Meter" card. It honours the same
// filter as List (customer / meter / from / to) but ignores limit/offset —
// the whole point is to surface filtered totals, not page slices.
//
// SQL strategy: one row-of-totals query for COUNT/SUM/distinct-counts,
// plus a GROUP BY meter_id query for the per-meter breakdown. Both run
// inside the same tenant tx so RLS scopes them identically. SUM is
// decimal-precision (NUMERIC(38,12)) so 0.5 + 0.5 + 0.0001 round-trips
// to "1.0001" without floating-point drift.
func (s *PostgresStore) Aggregate(ctx context.Context, filter ListFilter) (Aggregate, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return Aggregate{}, err
	}
	defer postgres.Rollback(tx)

	where, args := buildUsageWhere(filter)

	var agg Aggregate
	err = tx.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(quantity), 0),
			COUNT(DISTINCT meter_id),
			COUNT(DISTINCT customer_id)
		FROM usage_events`+where, args...,
	).Scan(&agg.TotalEvents, &agg.TotalUnits, &agg.ActiveMeters, &agg.ActiveCustomers)
	if err != nil {
		return Aggregate{}, fmt.Errorf("aggregate totals: %w", err)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT meter_id, COALESCE(SUM(quantity), 0)
		FROM usage_events`+where+`
		GROUP BY meter_id
		ORDER BY 2 DESC`, args...)
	if err != nil {
		return Aggregate{}, fmt.Errorf("aggregate by_meter: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var m MeterTotal
		if err := rows.Scan(&m.MeterID, &m.Total); err != nil {
			return Aggregate{}, err
		}
		agg.ByMeter = append(agg.ByMeter, m)
	}
	if err := rows.Err(); err != nil {
		return Aggregate{}, err
	}
	if agg.ByMeter == nil {
		agg.ByMeter = []MeterTotal{}
	}
	return agg, nil
}

func (s *PostgresStore) AggregateForBillingPeriodByAgg(ctx context.Context, tenantID, customerID string, meters map[string]string, from, to time.Time) (map[string]decimal.Decimal, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	result := make(map[string]decimal.Decimal)
	if len(meters) == 0 {
		return result, nil
	}

	for meterID, agg := range meters {
		aggFunc := "SUM"
		switch agg {
		case "max":
			aggFunc = "MAX"
		case "count":
			aggFunc = "COUNT"
		case "last":
			var val decimal.Decimal
			err := tx.QueryRowContext(ctx, `
				SELECT COALESCE(quantity, 0) FROM usage_events
				WHERE customer_id = $1 AND meter_id = $2 AND timestamp >= $3 AND timestamp < $4
				ORDER BY timestamp DESC LIMIT 1
			`, customerID, meterID, from, to).Scan(&val)
			if err == nil && val.IsPositive() {
				result[meterID] = val
			}
			continue
		}

		var val decimal.Decimal
		err := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COALESCE(%s(quantity), 0) FROM usage_events
				WHERE customer_id = $1 AND meter_id = $2 AND timestamp >= $3 AND timestamp < $4`,
				aggFunc),
			customerID, meterID, from, to).Scan(&val)
		if err == nil && val.IsPositive() {
			result[meterID] = val
		}
	}

	return result, nil
}

// AggregateDailyBuckets — see Store interface for contract. UTC-day
// granularity matches every reference platform (Datadog, OpenAI, AWS
// Cost Explorer); finer grain (hour) lives in a future bucket-grain
// param when an operator needs it. NULL meter_ids → empty result with
// no DB roundtrip. The result is unsorted; the service fills gaps and
// sorts by (bucket_start, meter_id) before serving.
func (s *PostgresStore) AggregateDailyBuckets(ctx context.Context, tenantID, customerID string, meterIDs []string, from, to time.Time) ([]DailyBucketRow, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	if len(meterIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(meterIDs))
	args := []any{customerID, from, to}
	for i, id := range meterIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+4)
		args = append(args, id)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT date_trunc('day', timestamp AT TIME ZONE 'UTC') AS bucket_start,
		       meter_id,
		       COALESCE(SUM(quantity), 0) AS qty
		FROM usage_events
		WHERE customer_id = $1
		  AND timestamp >= $2
		  AND timestamp < $3
		  AND meter_id IN (`+strings.Join(placeholders, ",")+`)
		GROUP BY 1, 2
	`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []DailyBucketRow
	for rows.Next() {
		var row DailyBucketRow
		if err := rows.Scan(&row.BucketStart, &row.MeterID, &row.Quantity); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *PostgresStore) AggregateForBillingPeriod(ctx context.Context, tenantID, customerID string, meterIDs []string, from, to time.Time) (map[string]decimal.Decimal, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	result := make(map[string]decimal.Decimal)
	if len(meterIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(meterIDs))
	args := []any{customerID, from, to}
	for i, id := range meterIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+4)
		args = append(args, id)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT meter_id, COALESCE(SUM(quantity), 0)
		FROM usage_events
		WHERE customer_id = $1 AND timestamp >= $2 AND timestamp < $3
			AND meter_id IN (`+strings.Join(placeholders, ",")+`)
		GROUP BY meter_id
	`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var meterID string
		var total decimal.Decimal
		if err := rows.Scan(&meterID, &total); err != nil {
			return nil, err
		}
		result[meterID] = total
	}
	return result, rows.Err()
}

// AggregateByPricingRules implements the priority+claim resolution path
// described in docs/design-multi-dim-meters.md. Strategy:
//
//  1. Rank meter_pricing_rules by (priority DESC, created_at ASC) so the
//     deterministic ROW_NUMBER becomes the rule_rank we order claims by.
//  2. LEFT JOIN LATERAL each in-period event against the ranked rules,
//     keeping only the top-priority rule whose dimension_match is a
//     subset of the event's properties. NULL rule means unclaimed.
//  3. Aggregate per rule. The CASE inside the SELECT is safe because
//     every event in a given (rule_id) group shares the rule's mode —
//     we GROUP BY (rule_id, mode, rrv) so the CASE evaluates a constant
//     within each group.
//
// last_ever needs a separate query because it ignores the period bounds.
// We run it only if any of the meter's rules is last_ever, then merge.
func (s *PostgresStore) AggregateByPricingRules(
	ctx context.Context,
	tenantID, customerID, meterID string,
	defaultMode domain.AggregationMode,
	from, to time.Time,
) ([]domain.RuleAggregation, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	// Period-bounded modes: sum, count, last_during_period, max, plus
	// the unclaimed bucket (defaultMode is one of these — last_ever on
	// the meter default is rejected at the service layer).
	//
	// last_during_period uses (array_agg ORDER BY ts DESC)[1] to pick
	// the latest event's quantity per group — a standard Postgres trick
	// that compiles down to a single sort over the group.
	rows, err := tx.QueryContext(ctx, `
		WITH ranked_rules AS (
			SELECT id, dimension_match, aggregation_mode, rating_rule_version_id,
			       ROW_NUMBER() OVER (ORDER BY priority DESC, created_at ASC) AS rule_rank
			FROM meter_pricing_rules
			WHERE tenant_id = $1 AND meter_id = $2
		),
		claims AS (
			SELECT
				e.id,
				e.quantity,
				e.timestamp,
				rr.id                     AS rule_id,
				rr.aggregation_mode       AS aggregation_mode,
				rr.rating_rule_version_id AS rating_rule_version_id
			FROM usage_events e
			LEFT JOIN LATERAL (
				SELECT id, aggregation_mode, rating_rule_version_id, rule_rank
				FROM ranked_rules
				WHERE e.properties @> ranked_rules.dimension_match
				  AND ranked_rules.aggregation_mode <> 'last_ever'
				ORDER BY rule_rank ASC
				LIMIT 1
			) rr ON TRUE
			WHERE e.tenant_id  = $1
			  AND e.meter_id   = $2
			  AND e.customer_id = $3
			  AND e.timestamp >= $4
			  AND e.timestamp <  $5
		)
		SELECT
			COALESCE(rule_id, '')                AS rule_id,
			COALESCE(rating_rule_version_id, '') AS rating_rule_version_id,
			COALESCE(aggregation_mode, $6)       AS aggregation_mode,
			CASE COALESCE(aggregation_mode, $6)
				WHEN 'sum'                THEN COALESCE(SUM(quantity), 0)
				WHEN 'count'              THEN COUNT(*)::numeric
				WHEN 'max'                THEN COALESCE(MAX(quantity), 0)
				WHEN 'last_during_period' THEN (array_agg(quantity ORDER BY timestamp DESC))[1]
				ELSE 0
			END AS quantity
		FROM claims
		GROUP BY rule_id, rating_rule_version_id, aggregation_mode
	`, tenantID, meterID, customerID, from, to, string(defaultMode))
	if err != nil {
		return nil, fmt.Errorf("aggregate by pricing rules (period): %w", err)
	}

	var out []domain.RuleAggregation
	for rows.Next() {
		var r domain.RuleAggregation
		var mode string
		if err := rows.Scan(&r.RuleID, &r.RatingRuleVersionID, &mode, &r.Quantity); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.AggregationMode = domain.AggregationMode(mode)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	// last_ever pass — only if the meter has any last_ever rules. Each
	// last_ever rule's quantity is the most recent event (across all
	// time) claimed by it. The claim semantics are identical to the
	// period-bounded path; the only difference is the missing time
	// filter.
	leverRows, err := tx.QueryContext(ctx, `
		WITH ranked_rules AS (
			SELECT id, dimension_match, aggregation_mode, rating_rule_version_id,
			       ROW_NUMBER() OVER (ORDER BY priority DESC, created_at ASC) AS rule_rank
			FROM meter_pricing_rules
			WHERE tenant_id = $1 AND meter_id = $2
		),
		claims AS (
			SELECT DISTINCT ON (e.id)
				e.id,
				e.quantity,
				e.timestamp,
				rr.id                     AS rule_id,
				rr.rating_rule_version_id AS rating_rule_version_id
			FROM usage_events e
			JOIN ranked_rules rr ON e.properties @> rr.dimension_match
			WHERE e.tenant_id   = $1
			  AND e.meter_id    = $2
			  AND e.customer_id = $3
			  AND rr.aggregation_mode = 'last_ever'
			ORDER BY e.id, rr.rule_rank ASC
		)
		SELECT DISTINCT ON (rule_id)
			rule_id, rating_rule_version_id, quantity
		FROM claims
		ORDER BY rule_id, timestamp DESC
	`, tenantID, meterID, customerID)
	if err != nil {
		return nil, fmt.Errorf("aggregate by pricing rules (last_ever): %w", err)
	}
	defer func() { _ = leverRows.Close() }()

	for leverRows.Next() {
		var r domain.RuleAggregation
		r.AggregationMode = domain.AggLastEver
		if err := leverRows.Scan(&r.RuleID, &r.RatingRuleVersionID, &r.Quantity); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := leverRows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

func buildUsageWhere(f ListFilter) (string, []any) {
	var clauses []string
	var args []any
	idx := 1

	if f.CustomerID != "" {
		clauses = append(clauses, fmt.Sprintf("customer_id = $%d", idx))
		args = append(args, f.CustomerID)
		idx++
	}
	if f.MeterID != "" {
		clauses = append(clauses, fmt.Sprintf("meter_id = $%d", idx))
		args = append(args, f.MeterID)
		idx++
	}
	if f.From != nil {
		clauses = append(clauses, fmt.Sprintf("timestamp >= $%d", idx))
		args = append(args, *f.From)
		idx++
	}
	if f.To != nil {
		clauses = append(clauses, fmt.Sprintf("timestamp < $%d", idx))
		args = append(args, *f.To)
	}

	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// propertiesJSON serializes the event's free-form properties for the JSONB
// column. This map is also the carrier for multi-dim meter dimensions
// (model, operation, region, etc.) that pricing_rule.dimension_match runs
// subset-match against — losing it here would silently drop dimension
// information at ingest, so a marshal failure is treated as a hard error.
func propertiesJSON(props map[string]any) (string, error) {
	if len(props) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(props)
	if err != nil {
		return "", fmt.Errorf("marshal usage_event.properties: %w", err)
	}
	return string(b), nil
}

// propertiesScanner adapts a *map[string]any to sql.Scanner so SELECT
// statements can read the JSONB column straight into the struct field.
// The pgx driver hands JSONB to Scan as []byte (or string in some paths).
type propertiesScanner struct{ dst *map[string]any }

func (s propertiesScanner) Scan(src any) error {
	if src == nil {
		*s.dst = nil
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported scan type for properties: %T", src)
	}
	if len(raw) == 0 {
		*s.dst = nil
		return nil
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("unmarshal usage_event.properties: %w", err)
	}
	*s.dst = m
	return nil
}
