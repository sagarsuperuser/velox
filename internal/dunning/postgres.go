package dunning

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

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

func (s *PostgresStore) GetPolicy(ctx context.Context, tenantID string) (domain.DunningPolicy, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.DunningPolicy{}, err
	}
	defer postgres.Rollback(tx)

	var p domain.DunningPolicy
	var scheduleJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, name, enabled, retry_schedule, max_retry_attempts,
			final_action, grace_period_days, created_at, updated_at
		FROM dunning_policies LIMIT 1
	`).Scan(&p.ID, &p.TenantID, &p.Name, &p.Enabled, &scheduleJSON,
		&p.MaxRetryAttempts, &p.FinalAction, &p.GracePeriodDays, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.DunningPolicy{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.DunningPolicy{}, err
	}
	_ = json.Unmarshal(scheduleJSON, &p.RetrySchedule)
	return p, nil
}

func (s *PostgresStore) UpsertPolicy(ctx context.Context, tenantID string, p domain.DunningPolicy) (domain.DunningPolicy, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.DunningPolicy{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_dpol")
	now := time.Now().UTC()
	scheduleJSON, _ := json.Marshal(p.RetrySchedule)

	var scheduleOut []byte
	err = tx.QueryRowContext(ctx, `
		INSERT INTO dunning_policies (id, tenant_id, name, enabled, retry_schedule,
			max_retry_attempts, final_action, grace_period_days, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
		ON CONFLICT (tenant_id, livemode) DO UPDATE SET
			name = EXCLUDED.name, enabled = EXCLUDED.enabled,
			retry_schedule = EXCLUDED.retry_schedule,
			max_retry_attempts = EXCLUDED.max_retry_attempts,
			final_action = EXCLUDED.final_action,
			grace_period_days = EXCLUDED.grace_period_days,
			updated_at = EXCLUDED.updated_at
		RETURNING id, tenant_id, name, enabled, retry_schedule, max_retry_attempts,
			final_action, grace_period_days, created_at, updated_at
	`, id, tenantID, p.Name, p.Enabled, scheduleJSON, p.MaxRetryAttempts,
		p.FinalAction, p.GracePeriodDays, now,
	).Scan(&p.ID, &p.TenantID, &p.Name, &p.Enabled, &scheduleOut,
		&p.MaxRetryAttempts, &p.FinalAction, &p.GracePeriodDays, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return domain.DunningPolicy{}, err
	}
	_ = json.Unmarshal(scheduleOut, &p.RetrySchedule)
	if err := tx.Commit(); err != nil {
		return domain.DunningPolicy{}, err
	}
	return p, nil
}

func (s *PostgresStore) CreateRun(ctx context.Context, tenantID string, run domain.InvoiceDunningRun) (domain.InvoiceDunningRun, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_drun")
	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO invoice_dunning_runs (id, tenant_id, invoice_id, customer_id, policy_id,
			state, reason, attempt_count, next_action_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)
		RETURNING id, tenant_id, invoice_id, COALESCE(customer_id,''), policy_id, state,
			COALESCE(reason,''), attempt_count, last_attempt_at, next_action_at,
			paused, resolved_at, COALESCE(resolution,''), created_at, updated_at
	`, id, tenantID, run.InvoiceID, postgres.NullableString(run.CustomerID),
		run.PolicyID, run.State, postgres.NullableString(run.Reason),
		run.AttemptCount, postgres.NullableTime(run.NextActionAt), now,
	).Scan(&run.ID, &run.TenantID, &run.InvoiceID, &run.CustomerID, &run.PolicyID,
		&run.State, &run.Reason, &run.AttemptCount, &run.LastAttemptAt, &run.NextActionAt,
		&run.Paused, &run.ResolvedAt, &run.Resolution, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.InvoiceDunningRun{}, err
	}
	return run, nil
}

func (s *PostgresStore) GetRun(ctx context.Context, tenantID, id string) (domain.InvoiceDunningRun, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}
	defer postgres.Rollback(tx)

	var run domain.InvoiceDunningRun
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, invoice_id, COALESCE(customer_id,''), policy_id, state,
			COALESCE(reason,''), attempt_count, last_attempt_at, next_action_at,
			paused, resolved_at, COALESCE(resolution,''), created_at, updated_at
		FROM invoice_dunning_runs WHERE id = $1
	`, id).Scan(&run.ID, &run.TenantID, &run.InvoiceID, &run.CustomerID, &run.PolicyID,
		&run.State, &run.Reason, &run.AttemptCount, &run.LastAttemptAt, &run.NextActionAt,
		&run.Paused, &run.ResolvedAt, &run.Resolution, &run.CreatedAt, &run.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.InvoiceDunningRun{}, errs.ErrNotFound
	}
	return run, err
}

func (s *PostgresStore) GetActiveRunByInvoice(ctx context.Context, tenantID, invoiceID string) (domain.InvoiceDunningRun, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}
	defer postgres.Rollback(tx)

	var run domain.InvoiceDunningRun
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, invoice_id, COALESCE(customer_id,''), policy_id, state,
			COALESCE(reason,''), attempt_count, last_attempt_at, next_action_at,
			paused, resolved_at, COALESCE(resolution,''), created_at, updated_at
		FROM invoice_dunning_runs
		WHERE invoice_id = $1 AND state NOT IN ('resolved', 'escalated')
		LIMIT 1
	`, invoiceID).Scan(&run.ID, &run.TenantID, &run.InvoiceID, &run.CustomerID, &run.PolicyID,
		&run.State, &run.Reason, &run.AttemptCount, &run.LastAttemptAt, &run.NextActionAt,
		&run.Paused, &run.ResolvedAt, &run.Resolution, &run.CreatedAt, &run.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.InvoiceDunningRun{}, errs.ErrNotFound
	}
	return run, err
}

func (s *PostgresStore) ListRuns(ctx context.Context, filter RunListFilter) ([]domain.InvoiceDunningRun, int, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, 0, err
	}
	defer postgres.Rollback(tx)

	args := []any{}
	clauses := []string{}
	idx := 1

	if filter.InvoiceID != "" {
		clauses = append(clauses, fmt.Sprintf("invoice_id = $%d", idx))
		args = append(args, filter.InvoiceID)
		idx++
	}
	if filter.State != "" {
		clauses = append(clauses, fmt.Sprintf("state = $%d", idx))
		args = append(args, filter.State)
		idx++
	}

	whereClause := ""
	if len(clauses) > 0 {
		whereClause = " WHERE "
		for i, c := range clauses {
			if i > 0 {
				whereClause += " AND "
			}
			whereClause += c
		}
	}

	var total int
	countQuery := `SELECT COUNT(*) FROM invoice_dunning_runs` + whereClause
	if err := tx.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT id, tenant_id, invoice_id, COALESCE(customer_id,''), policy_id, state,
		COALESCE(reason,''), attempt_count, last_attempt_at, next_action_at,
		paused, resolved_at, COALESCE(resolution,''), created_at, updated_at
		FROM invoice_dunning_runs` + whereClause + ` ORDER BY created_at DESC`
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var runs []domain.InvoiceDunningRun
	for rows.Next() {
		var r domain.InvoiceDunningRun
		if err := rows.Scan(&r.ID, &r.TenantID, &r.InvoiceID, &r.CustomerID, &r.PolicyID,
			&r.State, &r.Reason, &r.AttemptCount, &r.LastAttemptAt, &r.NextActionAt,
			&r.Paused, &r.ResolvedAt, &r.Resolution, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, 0, err
		}
		runs = append(runs, r)
	}
	return runs, total, rows.Err()
}

func (s *PostgresStore) UpdateRun(ctx context.Context, tenantID string, run domain.InvoiceDunningRun) (domain.InvoiceDunningRun, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		UPDATE invoice_dunning_runs SET state=$1, reason=$2, attempt_count=$3,
			last_attempt_at=$4, next_action_at=$5, paused=$6, resolved_at=$7, resolution=$8, updated_at=$9
		WHERE id=$10`,
		run.State, postgres.NullableString(run.Reason), run.AttemptCount,
		postgres.NullableTime(run.LastAttemptAt), postgres.NullableTime(run.NextActionAt),
		run.Paused, postgres.NullableTime(run.ResolvedAt), postgres.NullableString(string(run.Resolution)),
		now, run.ID)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}
	run.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return domain.InvoiceDunningRun{}, err
	}
	return run, nil
}

func (s *PostgresStore) ListDueRuns(ctx context.Context, tenantID string, dueBefore time.Time, limit int) ([]domain.InvoiceDunningRun, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	if limit <= 0 {
		limit = 20
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, invoice_id, COALESCE(customer_id,''), policy_id, state,
			COALESCE(reason,''), attempt_count, last_attempt_at, next_action_at,
			paused, resolved_at, COALESCE(resolution,''), created_at, updated_at
		FROM invoice_dunning_runs
		WHERE next_action_at <= $1 AND paused = false
			AND state NOT IN ('resolved', 'escalated')
		ORDER BY next_action_at ASC LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, dueBefore, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var runs []domain.InvoiceDunningRun
	for rows.Next() {
		var r domain.InvoiceDunningRun
		if err := rows.Scan(&r.ID, &r.TenantID, &r.InvoiceID, &r.CustomerID, &r.PolicyID,
			&r.State, &r.Reason, &r.AttemptCount, &r.LastAttemptAt, &r.NextActionAt,
			&r.Paused, &r.ResolvedAt, &r.Resolution, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (s *PostgresStore) CreateEvent(ctx context.Context, tenantID string, event domain.InvoiceDunningEvent) (domain.InvoiceDunningEvent, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.InvoiceDunningEvent{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_devt")
	now := time.Now().UTC()
	metaJSON, _ := json.Marshal(event.Metadata)
	if event.Metadata == nil {
		metaJSON = []byte("{}")
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO invoice_dunning_events (id, run_id, tenant_id, invoice_id,
			event_type, state, reason, attempt_count, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, id, event.RunID, tenantID, event.InvoiceID, event.EventType, event.State,
		postgres.NullableString(event.Reason), event.AttemptCount, metaJSON, now)
	if err != nil {
		return domain.InvoiceDunningEvent{}, err
	}
	event.ID = id
	event.TenantID = tenantID
	event.CreatedAt = now
	if err := tx.Commit(); err != nil {
		return domain.InvoiceDunningEvent{}, err
	}
	return event, nil
}

func (s *PostgresStore) ListEvents(ctx context.Context, tenantID, runID string) ([]domain.InvoiceDunningEvent, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, run_id, tenant_id, invoice_id, event_type, state,
			COALESCE(reason,''), attempt_count, metadata, created_at
		FROM invoice_dunning_events WHERE run_id = $1
		ORDER BY created_at ASC
	`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var events []domain.InvoiceDunningEvent
	for rows.Next() {
		var e domain.InvoiceDunningEvent
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.RunID, &e.TenantID, &e.InvoiceID,
			&e.EventType, &e.State, &e.Reason, &e.AttemptCount, &metaJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metaJSON, &e.Metadata)
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *PostgresStore) GetCustomerOverride(ctx context.Context, tenantID, customerID string) (domain.CustomerDunningOverride, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CustomerDunningOverride{}, err
	}
	defer postgres.Rollback(tx)

	var o domain.CustomerDunningOverride
	err = tx.QueryRowContext(ctx, `
		SELECT customer_id, tenant_id, max_retry_attempts, grace_period_days, COALESCE(final_action,'')
		FROM customer_dunning_overrides WHERE customer_id = $1
	`, customerID).Scan(&o.CustomerID, &o.TenantID, &o.MaxRetryAttempts, &o.GracePeriodDays, &o.FinalAction)
	if err == sql.ErrNoRows {
		return domain.CustomerDunningOverride{}, errs.ErrNotFound
	}
	return o, err
}

func (s *PostgresStore) UpsertCustomerOverride(ctx context.Context, tenantID string, o domain.CustomerDunningOverride) (domain.CustomerDunningOverride, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CustomerDunningOverride{}, err
	}
	defer postgres.Rollback(tx)

	err = tx.QueryRowContext(ctx, `
		INSERT INTO customer_dunning_overrides (customer_id, tenant_id, max_retry_attempts, grace_period_days, final_action, updated_at)
		VALUES ($1,$2,$3,$4,$5,NOW())
		ON CONFLICT (tenant_id, customer_id) DO UPDATE SET
			max_retry_attempts = EXCLUDED.max_retry_attempts,
			grace_period_days = EXCLUDED.grace_period_days,
			final_action = EXCLUDED.final_action,
			updated_at = NOW()
		RETURNING customer_id, tenant_id, max_retry_attempts, grace_period_days, COALESCE(final_action,'')
	`, o.CustomerID, tenantID, o.MaxRetryAttempts, o.GracePeriodDays,
		postgres.NullableString(o.FinalAction),
	).Scan(&o.CustomerID, &o.TenantID, &o.MaxRetryAttempts, &o.GracePeriodDays, &o.FinalAction)
	if err != nil {
		return domain.CustomerDunningOverride{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CustomerDunningOverride{}, err
	}
	return o, nil
}

func (s *PostgresStore) DeleteCustomerOverride(ctx context.Context, tenantID, customerID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	result, err := tx.ExecContext(ctx, `DELETE FROM customer_dunning_overrides WHERE customer_id = $1`, customerID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}
