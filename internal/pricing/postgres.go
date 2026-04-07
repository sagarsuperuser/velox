package pricing

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

// ---------------------------------------------------------------------------
// Rating Rules
// ---------------------------------------------------------------------------

func (s *PostgresStore) CreateRatingRule(ctx context.Context, tenantID string, rule domain.RatingRuleVersion) (domain.RatingRuleVersion, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.RatingRuleVersion{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_rrv")
	tiersJSON, err := json.Marshal(rule.GraduatedTiers)
	if err != nil {
		return domain.RatingRuleVersion{}, fmt.Errorf("marshal graduated_tiers: %w", err)
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO rating_rule_versions (id, tenant_id, rule_key, name, version, lifecycle_state, mode,
			currency, flat_amount_cents, graduated_tiers, package_size, package_amount_cents,
			overage_unit_amount_cents, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id, tenant_id, rule_key, name, version, lifecycle_state, mode, currency,
			flat_amount_cents, graduated_tiers, package_size, package_amount_cents,
			overage_unit_amount_cents, created_at
	`, id, tenantID, rule.RuleKey, rule.Name, rule.Version, rule.LifecycleState, rule.Mode,
		rule.Currency, rule.FlatAmountCents, tiersJSON, rule.PackageSize,
		rule.PackageAmountCents, rule.OverageUnitAmountCents, time.Now().UTC(),
	).Scan(
		&rule.ID, &rule.TenantID, &rule.RuleKey, &rule.Name, &rule.Version,
		&rule.LifecycleState, &rule.Mode, &rule.Currency, &rule.FlatAmountCents,
		&tiersJSON, &rule.PackageSize, &rule.PackageAmountCents,
		&rule.OverageUnitAmountCents, &rule.CreatedAt,
	)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.RatingRuleVersion{}, fmt.Errorf("%w: rule_key %q version %d", errs.ErrAlreadyExists, rule.RuleKey, rule.Version)
		}
		return domain.RatingRuleVersion{}, err
	}

	if err := json.Unmarshal(tiersJSON, &rule.GraduatedTiers); err != nil {
		return domain.RatingRuleVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RatingRuleVersion{}, err
	}
	return rule, nil
}

func (s *PostgresStore) GetRatingRule(ctx context.Context, tenantID, id string) (domain.RatingRuleVersion, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.RatingRuleVersion{}, err
	}
	defer postgres.Rollback(tx)

	return scanRatingRule(tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, rule_key, name, version, lifecycle_state, mode, currency,
			flat_amount_cents, graduated_tiers, package_size, package_amount_cents,
			overage_unit_amount_cents, created_at
		FROM rating_rule_versions WHERE id = $1
	`, id))
}

func (s *PostgresStore) ListRatingRules(ctx context.Context, filter RatingRuleFilter) ([]domain.RatingRuleVersion, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	query := `SELECT id, tenant_id, rule_key, name, version, lifecycle_state, mode, currency,
		flat_amount_cents, graduated_tiers, package_size, package_amount_cents,
		overage_unit_amount_cents, created_at
		FROM rating_rule_versions`
	args := []any{}
	clauses := []string{}
	idx := 1

	if filter.RuleKey != "" {
		clauses = append(clauses, fmt.Sprintf("rule_key = $%d", idx))
		args = append(args, filter.RuleKey)
		idx++
	}
	if filter.LifecycleState != "" {
		clauses = append(clauses, fmt.Sprintf("lifecycle_state = $%d", idx))
		args = append(args, filter.LifecycleState)
		idx++
	}

	if len(clauses) > 0 {
		query += " WHERE " + joinClauses(clauses)
	}

	if filter.LatestOnly {
		query = `SELECT DISTINCT ON (rule_key) ` + query[len("SELECT "):]
		query += " ORDER BY rule_key, version DESC"
	} else {
		query += " ORDER BY rule_key, version DESC"
	}

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []domain.RatingRuleVersion
	for rows.Next() {
		r, err := scanRatingRuleRow(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// ---------------------------------------------------------------------------
// Meters
// ---------------------------------------------------------------------------

func (s *PostgresStore) CreateMeter(ctx context.Context, tenantID string, m domain.Meter) (domain.Meter, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Meter{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_mtr")
	now := time.Now().UTC()

	err = tx.QueryRowContext(ctx, `
		INSERT INTO meters (id, tenant_id, key, name, unit, aggregation, rating_rule_version_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
		RETURNING id, tenant_id, key, name, unit, aggregation, COALESCE(rating_rule_version_id,''), created_at, updated_at
	`, id, tenantID, m.Key, m.Name, m.Unit, m.Aggregation,
		postgres.NullableString(m.RatingRuleVersionID), now,
	).Scan(&m.ID, &m.TenantID, &m.Key, &m.Name, &m.Unit, &m.Aggregation,
		&m.RatingRuleVersionID, &m.CreatedAt, &m.UpdatedAt)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Meter{}, fmt.Errorf("%w: meter key %q", errs.ErrAlreadyExists, m.Key)
		}
		return domain.Meter{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Meter{}, err
	}
	return m, nil
}

func (s *PostgresStore) GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Meter{}, err
	}
	defer postgres.Rollback(tx)

	var m domain.Meter
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, key, name, unit, aggregation, COALESCE(rating_rule_version_id,''), created_at, updated_at
		FROM meters WHERE id = $1
	`, id).Scan(&m.ID, &m.TenantID, &m.Key, &m.Name, &m.Unit, &m.Aggregation,
		&m.RatingRuleVersionID, &m.CreatedAt, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.Meter{}, errs.ErrNotFound
	}
	return m, err
}

func (s *PostgresStore) ListMeters(ctx context.Context, tenantID string) ([]domain.Meter, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, key, name, unit, aggregation, COALESCE(rating_rule_version_id,''), created_at, updated_at
		FROM meters ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var meters []domain.Meter
	for rows.Next() {
		var m domain.Meter
		if err := rows.Scan(&m.ID, &m.TenantID, &m.Key, &m.Name, &m.Unit, &m.Aggregation,
			&m.RatingRuleVersionID, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		meters = append(meters, m)
	}
	return meters, rows.Err()
}

func (s *PostgresStore) UpdateMeter(ctx context.Context, tenantID string, m domain.Meter) (domain.Meter, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Meter{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `
		UPDATE meters SET name = $1, unit = $2, aggregation = $3, rating_rule_version_id = $4, updated_at = $5
		WHERE id = $6
		RETURNING id, tenant_id, key, name, unit, aggregation, COALESCE(rating_rule_version_id,''), created_at, updated_at
	`, m.Name, m.Unit, m.Aggregation, postgres.NullableString(m.RatingRuleVersionID), now, m.ID,
	).Scan(&m.ID, &m.TenantID, &m.Key, &m.Name, &m.Unit, &m.Aggregation,
		&m.RatingRuleVersionID, &m.CreatedAt, &m.UpdatedAt)

	if err == sql.ErrNoRows {
		return domain.Meter{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Meter{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Meter{}, err
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

func (s *PostgresStore) CreatePlan(ctx context.Context, tenantID string, p domain.Plan) (domain.Plan, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Plan{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_pln")
	now := time.Now().UTC()
	meterIDsJSON, _ := json.Marshal(p.MeterIDs)

	err = tx.QueryRowContext(ctx, `
		INSERT INTO plans (id, tenant_id, code, name, description, currency, billing_interval,
			status, base_amount_cents, meter_ids, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		RETURNING id, tenant_id, code, name, COALESCE(description,''), currency, billing_interval,
			status, base_amount_cents, meter_ids, created_at, updated_at
	`, id, tenantID, p.Code, p.Name, postgres.NullableString(p.Description),
		p.Currency, p.BillingInterval, p.Status, p.BaseAmountCents, meterIDsJSON, now,
	).Scan(&p.ID, &p.TenantID, &p.Code, &p.Name, &p.Description, &p.Currency,
		&p.BillingInterval, &p.Status, &p.BaseAmountCents, &meterIDsJSON,
		&p.CreatedAt, &p.UpdatedAt)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Plan{}, fmt.Errorf("%w: plan code %q", errs.ErrAlreadyExists, p.Code)
		}
		return domain.Plan{}, err
	}
	json.Unmarshal(meterIDsJSON, &p.MeterIDs)
	if err := tx.Commit(); err != nil {
		return domain.Plan{}, err
	}
	return p, nil
}

func (s *PostgresStore) GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Plan{}, err
	}
	defer postgres.Rollback(tx)

	return scanPlan(tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, code, name, COALESCE(description,''), currency, billing_interval,
			status, base_amount_cents, meter_ids, created_at, updated_at
		FROM plans WHERE id = $1
	`, id))
}

func (s *PostgresStore) ListPlans(ctx context.Context, tenantID string) ([]domain.Plan, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, code, name, COALESCE(description,''), currency, billing_interval,
			status, base_amount_cents, meter_ids, created_at, updated_at
		FROM plans ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plans []domain.Plan
	for rows.Next() {
		p, err := scanPlanRow(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func (s *PostgresStore) UpdatePlan(ctx context.Context, tenantID string, p domain.Plan) (domain.Plan, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Plan{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	meterIDsJSON, _ := json.Marshal(p.MeterIDs)

	err = tx.QueryRowContext(ctx, `
		UPDATE plans SET name = $1, description = $2, status = $3, base_amount_cents = $4,
			meter_ids = $5, updated_at = $6
		WHERE id = $7
		RETURNING id, tenant_id, code, name, COALESCE(description,''), currency, billing_interval,
			status, base_amount_cents, meter_ids, created_at, updated_at
	`, p.Name, postgres.NullableString(p.Description), p.Status, p.BaseAmountCents,
		meterIDsJSON, now, p.ID,
	).Scan(&p.ID, &p.TenantID, &p.Code, &p.Name, &p.Description, &p.Currency,
		&p.BillingInterval, &p.Status, &p.BaseAmountCents, &meterIDsJSON,
		&p.CreatedAt, &p.UpdatedAt)

	if err == sql.ErrNoRows {
		return domain.Plan{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Plan{}, err
	}
	json.Unmarshal(meterIDsJSON, &p.MeterIDs)
	if err := tx.Commit(); err != nil {
		return domain.Plan{}, err
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Scan helpers
// ---------------------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRatingRule(row rowScanner) (domain.RatingRuleVersion, error) {
	var r domain.RatingRuleVersion
	var tiersJSON []byte
	err := row.Scan(&r.ID, &r.TenantID, &r.RuleKey, &r.Name, &r.Version,
		&r.LifecycleState, &r.Mode, &r.Currency, &r.FlatAmountCents,
		&tiersJSON, &r.PackageSize, &r.PackageAmountCents,
		&r.OverageUnitAmountCents, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return domain.RatingRuleVersion{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.RatingRuleVersion{}, err
	}
	json.Unmarshal(tiersJSON, &r.GraduatedTiers)
	return r, nil
}

func scanRatingRuleRow(rows *sql.Rows) (domain.RatingRuleVersion, error) {
	return scanRatingRule(rows)
}

func scanPlan(row rowScanner) (domain.Plan, error) {
	var p domain.Plan
	var meterIDsJSON []byte
	err := row.Scan(&p.ID, &p.TenantID, &p.Code, &p.Name, &p.Description, &p.Currency,
		&p.BillingInterval, &p.Status, &p.BaseAmountCents, &meterIDsJSON,
		&p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.Plan{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Plan{}, err
	}
	json.Unmarshal(meterIDsJSON, &p.MeterIDs)
	return p, nil
}

func scanPlanRow(rows *sql.Rows) (domain.Plan, error) {
	return scanPlan(rows)
}

func joinClauses(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += " AND "
		}
		result += p
	}
	return result
}
