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
			return domain.RatingRuleVersion{}, errs.AlreadyExists("rule_key", fmt.Sprintf("rule_key %q version %d already exists", rule.RuleKey, rule.Version))
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

	query += " LIMIT 500"

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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
			return domain.Meter{}, errs.AlreadyExists("key", fmt.Sprintf("meter key %q already exists", m.Key))
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

func (s *PostgresStore) GetMeterByKey(ctx context.Context, tenantID, key string) (domain.Meter, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Meter{}, err
	}
	defer postgres.Rollback(tx)

	var m domain.Meter
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, key, name, unit, aggregation, COALESCE(rating_rule_version_id,''), created_at, updated_at
		FROM meters WHERE key = $1
	`, key).Scan(&m.ID, &m.TenantID, &m.Key, &m.Name, &m.Unit, &m.Aggregation,
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
		FROM meters ORDER BY created_at DESC LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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
			status, base_amount_cents, meter_ids, created_at, updated_at, tax_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,$12)
		RETURNING id, tenant_id, code, name, COALESCE(description,''), currency, billing_interval,
			status, base_amount_cents, meter_ids, created_at, updated_at, tax_code
	`, id, tenantID, p.Code, p.Name, postgres.NullableString(p.Description),
		p.Currency, p.BillingInterval, p.Status, p.BaseAmountCents, meterIDsJSON, now, p.TaxCode,
	).Scan(&p.ID, &p.TenantID, &p.Code, &p.Name, &p.Description, &p.Currency,
		&p.BillingInterval, &p.Status, &p.BaseAmountCents, &meterIDsJSON,
		&p.CreatedAt, &p.UpdatedAt, &p.TaxCode)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Plan{}, errs.AlreadyExists("code", fmt.Sprintf("plan code %q already exists", p.Code))
		}
		return domain.Plan{}, err
	}
	_ = json.Unmarshal(meterIDsJSON, &p.MeterIDs)
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
			status, base_amount_cents, meter_ids, created_at, updated_at, tax_code
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
			status, base_amount_cents, meter_ids, created_at, updated_at, tax_code
		FROM plans ORDER BY created_at DESC LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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
			meter_ids = $5, updated_at = $6, tax_code = $7
		WHERE id = $8
		RETURNING id, tenant_id, code, name, COALESCE(description,''), currency, billing_interval,
			status, base_amount_cents, meter_ids, created_at, updated_at, tax_code
	`, p.Name, postgres.NullableString(p.Description), p.Status, p.BaseAmountCents,
		meterIDsJSON, now, p.TaxCode, p.ID,
	).Scan(&p.ID, &p.TenantID, &p.Code, &p.Name, &p.Description, &p.Currency,
		&p.BillingInterval, &p.Status, &p.BaseAmountCents, &meterIDsJSON,
		&p.CreatedAt, &p.UpdatedAt, &p.TaxCode)

	if err == sql.ErrNoRows {
		return domain.Plan{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Plan{}, err
	}
	_ = json.Unmarshal(meterIDsJSON, &p.MeterIDs)
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
	_ = json.Unmarshal(tiersJSON, &r.GraduatedTiers)
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
		&p.CreatedAt, &p.UpdatedAt, &p.TaxCode)
	if err == sql.ErrNoRows {
		return domain.Plan{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Plan{}, err
	}
	_ = json.Unmarshal(meterIDsJSON, &p.MeterIDs)
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

// ---------------------------------------------------------------------------
// Meter Pricing Rules
// ---------------------------------------------------------------------------

// UpsertMeterPricingRule inserts or updates a pricing rule for a meter.
// The unique key is (tenant_id, meter_id, rating_rule_version_id) — a
// rule is identified by which rating rule it points at, so re-issuing
// the same point-pair with new dimension_match / mode / priority
// updates the existing row. ON CONFLICT keeps the original id and
// created_at, and bumps updated_at.
func (s *PostgresStore) UpsertMeterPricingRule(ctx context.Context, tenantID string, rule domain.MeterPricingRule) (domain.MeterPricingRule, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.MeterPricingRule{}, err
	}
	defer postgres.Rollback(tx)

	id := rule.ID
	if id == "" {
		id = postgres.NewID("vlx_mpr")
	}

	dimMatch := rule.DimensionMatch
	if dimMatch == nil {
		dimMatch = map[string]any{}
	}
	matchJSON, err := json.Marshal(dimMatch)
	if err != nil {
		return domain.MeterPricingRule{}, fmt.Errorf("marshal dimension_match: %w", err)
	}

	now := time.Now().UTC()
	var stored domain.MeterPricingRule
	var storedMatch []byte
	err = tx.QueryRowContext(ctx, `
		INSERT INTO meter_pricing_rules
			(id, tenant_id, meter_id, rating_rule_version_id, dimension_match,
			 aggregation_mode, priority, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
		ON CONFLICT (tenant_id, meter_id, rating_rule_version_id) DO UPDATE
		   SET dimension_match  = EXCLUDED.dimension_match,
		       aggregation_mode = EXCLUDED.aggregation_mode,
		       priority         = EXCLUDED.priority,
		       updated_at       = EXCLUDED.updated_at
		RETURNING id, tenant_id, meter_id, rating_rule_version_id,
		          dimension_match, aggregation_mode, priority,
		          created_at, updated_at
	`, id, tenantID, rule.MeterID, rule.RatingRuleVersionID, matchJSON,
		string(rule.AggregationMode), rule.Priority, now,
	).Scan(
		&stored.ID, &stored.TenantID, &stored.MeterID, &stored.RatingRuleVersionID,
		&storedMatch, &stored.AggregationMode, &stored.Priority,
		&stored.CreatedAt, &stored.UpdatedAt,
	)
	if err != nil {
		return domain.MeterPricingRule{}, err
	}

	if len(storedMatch) > 0 {
		if err := json.Unmarshal(storedMatch, &stored.DimensionMatch); err != nil {
			return domain.MeterPricingRule{}, fmt.Errorf("unmarshal dimension_match: %w", err)
		}
	}
	if stored.DimensionMatch == nil {
		stored.DimensionMatch = map[string]any{}
	}

	if err := tx.Commit(); err != nil {
		return domain.MeterPricingRule{}, err
	}
	return stored, nil
}

func (s *PostgresStore) GetMeterPricingRule(ctx context.Context, tenantID, id string) (domain.MeterPricingRule, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.MeterPricingRule{}, err
	}
	defer postgres.Rollback(tx)

	rule, err := scanMeterPricingRule(tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, meter_id, rating_rule_version_id,
		       dimension_match, aggregation_mode, priority,
		       created_at, updated_at
		  FROM meter_pricing_rules
		 WHERE id = $1
	`, id))
	if err != nil {
		return domain.MeterPricingRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MeterPricingRule{}, err
	}
	return rule, nil
}

// ListMeterPricingRulesByMeter returns all pricing rules for a meter
// ordered by priority DESC, then created_at ASC. This matches the
// runtime resolution order in design-multi-dim-meters.md so callers can
// walk the slice top-down without re-sorting.
func (s *PostgresStore) ListMeterPricingRulesByMeter(ctx context.Context, tenantID, meterID string) ([]domain.MeterPricingRule, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, meter_id, rating_rule_version_id,
		       dimension_match, aggregation_mode, priority,
		       created_at, updated_at
		  FROM meter_pricing_rules
		 WHERE meter_id = $1
		 ORDER BY priority DESC, created_at ASC
	`, meterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.MeterPricingRule
	for rows.Next() {
		rule, err := scanMeterPricingRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) DeleteMeterPricingRule(ctx context.Context, tenantID, id string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `DELETE FROM meter_pricing_rules WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

func scanMeterPricingRule(row rowScanner) (domain.MeterPricingRule, error) {
	var r domain.MeterPricingRule
	var matchJSON []byte
	err := row.Scan(&r.ID, &r.TenantID, &r.MeterID, &r.RatingRuleVersionID,
		&matchJSON, &r.AggregationMode, &r.Priority,
		&r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.MeterPricingRule{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.MeterPricingRule{}, err
	}
	if len(matchJSON) > 0 {
		if err := json.Unmarshal(matchJSON, &r.DimensionMatch); err != nil {
			return domain.MeterPricingRule{}, fmt.Errorf("unmarshal dimension_match: %w", err)
		}
	}
	if r.DimensionMatch == nil {
		r.DimensionMatch = map[string]any{}
	}
	return r, nil
}
