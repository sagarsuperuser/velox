package billingalert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// PostgresStore is the canonical Store implementation. Every method
// opens its own tenant-scoped tx via postgres.TxTenant — RLS hides
// cross-tenant rows so a misrouted request returns ErrNotFound.
type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// ErrAlreadyFired is returned by FireInTx when the UNIQUE
// (alert_id, period_from) constraint catches a duplicate insert. The
// evaluator treats this as a no-op (another replica or a previous
// crashed-and-retried tick already fired this period).
var ErrAlreadyFired = errors.New("billingalert: already fired this period")

const billingAlertColumns = `
	id, tenant_id, customer_id, title,
	COALESCE(meter_id, ''),
	COALESCE(dimensions, '{}'::jsonb),
	threshold_amount_cents, threshold_quantity,
	recurrence, status,
	last_triggered_at, last_period_start,
	created_at, updated_at
`

func (s *PostgresStore) Create(ctx context.Context, tenantID string, alert domain.BillingAlert) (domain.BillingAlert, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.BillingAlert{}, err
	}
	defer postgres.Rollback(tx)

	dimsJSON, err := marshalDimensions(alert.Filter.Dimensions)
	if err != nil {
		return domain.BillingAlert{}, err
	}

	var amount sql.NullInt64
	if alert.Threshold.AmountCentsGTE != nil {
		amount = sql.NullInt64{Int64: *alert.Threshold.AmountCentsGTE, Valid: true}
	}
	var qty sql.NullString
	if alert.Threshold.QuantityGTE != nil {
		qty = sql.NullString{String: alert.Threshold.QuantityGTE.String(), Valid: true}
	}

	var meterID sql.NullString
	if strings.TrimSpace(alert.Filter.MeterID) != "" {
		meterID = sql.NullString{String: alert.Filter.MeterID, Valid: true}
	}

	id := postgres.NewID("vlx_alrt")
	row := tx.QueryRowContext(ctx, `
		INSERT INTO billing_alerts (
			id, tenant_id, customer_id, title, meter_id, dimensions,
			threshold_amount_cents, threshold_quantity, recurrence, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'active')
		RETURNING `+billingAlertColumns,
		id, tenantID, alert.CustomerID, alert.Title,
		meterID, dimsJSON, amount, qty, string(alert.Recurrence),
	)
	out, err := scanAlert(row)
	if err != nil {
		return domain.BillingAlert{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.BillingAlert{}, err
	}
	return out, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (domain.BillingAlert, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.BillingAlert{}, err
	}
	defer postgres.Rollback(tx)

	row := tx.QueryRowContext(ctx, `SELECT `+billingAlertColumns+` FROM billing_alerts WHERE id = $1`, id)
	out, err := scanAlert(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BillingAlert{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.BillingAlert{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.BillingAlert{}, err
	}
	return out, nil
}

func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]domain.BillingAlert, int, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, 0, err
	}
	defer postgres.Rollback(tx)

	var (
		conds  []string
		args   []any
		argIdx = 1
	)
	if strings.TrimSpace(filter.CustomerID) != "" {
		conds = append(conds, fmt.Sprintf("customer_id = $%d", argIdx))
		args = append(args, filter.CustomerID)
		argIdx++
	}
	if strings.TrimSpace(string(filter.Status)) != "" {
		conds = append(conds, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, string(filter.Status))
		argIdx++
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM billing_alerts "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, filter.Limit, filter.Offset)
	rows, err := tx.QueryContext(ctx,
		"SELECT "+billingAlertColumns+" FROM billing_alerts "+where+
			" ORDER BY created_at DESC LIMIT $"+itoa(argIdx)+" OFFSET $"+itoa(argIdx+1),
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var out []domain.BillingAlert
	for rows.Next() {
		alert, err := scanAlert(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, alert)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *PostgresStore) Archive(ctx context.Context, tenantID, id string) (domain.BillingAlert, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.BillingAlert{}, err
	}
	defer postgres.Rollback(tx)

	row := tx.QueryRowContext(ctx, `
		UPDATE billing_alerts
		SET status = 'archived', updated_at = now()
		WHERE id = $1
		RETURNING `+billingAlertColumns,
		id,
	)
	out, err := scanAlert(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BillingAlert{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.BillingAlert{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.BillingAlert{}, err
	}
	return out, nil
}

// ListCandidates returns alerts the evaluator should evaluate this
// tick. Runs under TxBypass because the evaluator is cross-tenant
// (one leader per cluster) and needs to see every armed alert.
//
// The partial index idx_billing_alerts_evaluator keeps this scan
// bounded to currently-firing-eligible rows.
func (s *PostgresStore) ListCandidates(ctx context.Context, limit int) ([]domain.BillingAlert, error) {
	if limit <= 0 {
		limit = 500
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+billingAlertColumns+`
		FROM billing_alerts
		WHERE status IN ('active','triggered_for_period')
		ORDER BY created_at
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []domain.BillingAlert
	for rows.Next() {
		alert, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, alert)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// FireInTx inserts the trigger row + updates the alert's status. The
// caller (the evaluator) holds the tx open across this method and
// the outbox enqueue, then commits both atomically.
func (s *PostgresStore) FireInTx(
	ctx context.Context,
	tx *sql.Tx,
	alert domain.BillingAlert,
	trigger domain.BillingAlertTrigger,
	newStatus domain.BillingAlertStatus,
) (domain.BillingAlertTrigger, error) {
	id := postgres.NewID("vlx_atrg")
	qty := trigger.ObservedQuantity
	if qty.Equal(decimal.Zero) {
		qty = decimal.Zero
	}

	row := tx.QueryRowContext(ctx, `
		INSERT INTO billing_alert_triggers (
			id, tenant_id, alert_id, period_from, period_to,
			observed_amount_cents, observed_quantity, currency
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, tenant_id, alert_id, period_from, period_to,
			observed_amount_cents, observed_quantity, currency, triggered_at
	`,
		id, alert.TenantID, alert.ID, trigger.PeriodFrom, trigger.PeriodTo,
		trigger.ObservedAmountCents, qty.String(), trigger.Currency,
	)

	var inserted domain.BillingAlertTrigger
	var qtyStr string
	if err := row.Scan(
		&inserted.ID, &inserted.TenantID, &inserted.AlertID,
		&inserted.PeriodFrom, &inserted.PeriodTo,
		&inserted.ObservedAmountCents, &qtyStr, &inserted.Currency,
		&inserted.TriggeredAt,
	); err != nil {
		// Postgres unique-violation (SQLSTATE 23505). Surface as our
		// sentinel so the evaluator can swallow + skip without
		// double-emitting.
		if postgres.IsUniqueViolation(err) {
			return domain.BillingAlertTrigger{}, ErrAlreadyFired
		}
		return domain.BillingAlertTrigger{}, fmt.Errorf("insert billing_alert_trigger: %w", err)
	}
	parsed, err := decimal.NewFromString(qtyStr)
	if err != nil {
		return domain.BillingAlertTrigger{}, fmt.Errorf("parse observed_quantity %q: %w", qtyStr, err)
	}
	inserted.ObservedQuantity = parsed

	if _, err := tx.ExecContext(ctx, `
		UPDATE billing_alerts
		SET status = $2,
		    last_triggered_at = $3,
		    last_period_start = $4,
		    updated_at = now()
		WHERE id = $1
	`, alert.ID, string(newStatus), inserted.TriggeredAt, trigger.PeriodFrom); err != nil {
		return domain.BillingAlertTrigger{}, fmt.Errorf("update billing_alert: %w", err)
	}

	return inserted, nil
}

func (s *PostgresStore) Rearm(ctx context.Context, tenantID, alertID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	if _, err := tx.ExecContext(ctx, `
		UPDATE billing_alerts
		SET status = 'active', updated_at = now()
		WHERE id = $1 AND status = 'triggered_for_period'
	`, alertID); err != nil {
		return fmt.Errorf("rearm billing_alert: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresStore) BeginTenantTx(ctx context.Context, tenantID string) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
}

// rowScanner abstracts QueryRowContext / Rows.Scan into one helper so
// per-column scan code only lives in scanAlert.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAlert(rs rowScanner) (domain.BillingAlert, error) {
	var (
		alert      domain.BillingAlert
		meterID    string
		dimsJSON   []byte
		amountNI   sql.NullInt64
		qtyNS      sql.NullString
		recurrence string
		status     string
		lastTrig   sql.NullTime
		lastPeriod sql.NullTime
	)
	if err := rs.Scan(
		&alert.ID, &alert.TenantID, &alert.CustomerID, &alert.Title,
		&meterID, &dimsJSON,
		&amountNI, &qtyNS,
		&recurrence, &status,
		&lastTrig, &lastPeriod,
		&alert.CreatedAt, &alert.UpdatedAt,
	); err != nil {
		return domain.BillingAlert{}, err
	}

	alert.Filter.MeterID = meterID
	if len(dimsJSON) > 0 {
		var dims map[string]any
		if err := json.Unmarshal(dimsJSON, &dims); err != nil {
			return domain.BillingAlert{}, fmt.Errorf("unmarshal dimensions: %w", err)
		}
		alert.Filter.Dimensions = dims
	}
	if alert.Filter.Dimensions == nil {
		alert.Filter.Dimensions = map[string]any{}
	}

	if amountNI.Valid {
		v := amountNI.Int64
		alert.Threshold.AmountCentsGTE = &v
	}
	if qtyNS.Valid {
		parsed, err := decimal.NewFromString(qtyNS.String)
		if err != nil {
			return domain.BillingAlert{}, fmt.Errorf("parse threshold_quantity: %w", err)
		}
		alert.Threshold.QuantityGTE = &parsed
	}

	alert.Recurrence = domain.BillingAlertRecurrence(recurrence)
	alert.Status = domain.BillingAlertStatus(status)
	if lastTrig.Valid {
		t := lastTrig.Time
		alert.LastTriggeredAt = &t
	}
	if lastPeriod.Valid {
		t := lastPeriod.Time
		alert.LastPeriodStart = &t
	}
	return alert, nil
}

// marshalDimensions converts the always-object map to JSON bytes for
// the JSONB column. nil → '{}' (the always-object identity).
func marshalDimensions(dims map[string]any) ([]byte, error) {
	if dims == nil {
		dims = map[string]any{}
	}
	return json.Marshal(dims)
}

// itoa converts a small positive int to its decimal string. Avoids
// pulling in strconv just to interpolate parameter indices.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
