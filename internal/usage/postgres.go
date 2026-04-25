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

	props, err := propertiesJSON(event.Properties)
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
		&event.SubscriptionID, &event.Quantity, propertiesScanner{&event.Properties},
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
			&e.SubscriptionID, &e.Quantity, propertiesScanner{&e.Properties},
			&e.IdempotencyKey, &e.Timestamp); err != nil {
			return nil, 0, err
		}
		events = append(events, e)
	}
	return events, total, rows.Err()
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
