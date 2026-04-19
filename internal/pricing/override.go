package pricing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Override methods on the existing PostgresStore.

func (s *PostgresStore) CreateOverride(ctx context.Context, tenantID string, o domain.CustomerPriceOverride) (domain.CustomerPriceOverride, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CustomerPriceOverride{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_cpo")
	now := time.Now().UTC()
	tiersJSON, _ := json.Marshal(o.GraduatedTiers)

	err = tx.QueryRowContext(ctx, `
		INSERT INTO customer_price_overrides (id, tenant_id, customer_id, rating_rule_version_id,
			mode, flat_amount_cents, graduated_tiers, package_size, package_amount_cents,
			overage_unit_amount_cents, reason, active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
		ON CONFLICT (tenant_id, customer_id, rating_rule_version_id) DO UPDATE SET
			mode = EXCLUDED.mode, flat_amount_cents = EXCLUDED.flat_amount_cents,
			graduated_tiers = EXCLUDED.graduated_tiers, package_size = EXCLUDED.package_size,
			package_amount_cents = EXCLUDED.package_amount_cents,
			overage_unit_amount_cents = EXCLUDED.overage_unit_amount_cents,
			reason = EXCLUDED.reason, active = EXCLUDED.active, updated_at = EXCLUDED.updated_at
		RETURNING id, tenant_id, customer_id, rating_rule_version_id, mode,
			flat_amount_cents, graduated_tiers, package_size, package_amount_cents,
			overage_unit_amount_cents, COALESCE(reason,''), active, created_at, updated_at
	`, id, tenantID, o.CustomerID, o.RatingRuleVersionID,
		o.Mode, o.FlatAmountCents, tiersJSON, o.PackageSize,
		o.PackageAmountCents, o.OverageUnitAmountCents,
		postgres.NullableString(o.Reason), true, now,
	).Scan(&o.ID, &o.TenantID, &o.CustomerID, &o.RatingRuleVersionID, &o.Mode,
		&o.FlatAmountCents, &tiersJSON, &o.PackageSize, &o.PackageAmountCents,
		&o.OverageUnitAmountCents, &o.Reason, &o.Active, &o.CreatedAt, &o.UpdatedAt)

	if err != nil {
		return domain.CustomerPriceOverride{}, err
	}
	_ = json.Unmarshal(tiersJSON, &o.GraduatedTiers)
	if err := tx.Commit(); err != nil {
		return domain.CustomerPriceOverride{}, err
	}
	return o, nil
}

func (s *PostgresStore) GetOverride(ctx context.Context, tenantID, customerID, ruleID string) (domain.CustomerPriceOverride, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CustomerPriceOverride{}, err
	}
	defer postgres.Rollback(tx)

	var o domain.CustomerPriceOverride
	var tiersJSON []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, customer_id, rating_rule_version_id, mode,
			flat_amount_cents, graduated_tiers, package_size, package_amount_cents,
			overage_unit_amount_cents, COALESCE(reason,''), active, created_at, updated_at
		FROM customer_price_overrides
		WHERE customer_id = $1 AND rating_rule_version_id = $2 AND active = true
	`, customerID, ruleID).Scan(&o.ID, &o.TenantID, &o.CustomerID, &o.RatingRuleVersionID,
		&o.Mode, &o.FlatAmountCents, &tiersJSON, &o.PackageSize, &o.PackageAmountCents,
		&o.OverageUnitAmountCents, &o.Reason, &o.Active, &o.CreatedAt, &o.UpdatedAt)

	if err == sql.ErrNoRows {
		return domain.CustomerPriceOverride{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.CustomerPriceOverride{}, err
	}
	_ = json.Unmarshal(tiersJSON, &o.GraduatedTiers)
	return o, nil
}

func (s *PostgresStore) ListOverrides(ctx context.Context, tenantID, customerID string) ([]domain.CustomerPriceOverride, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	query := `SELECT id, tenant_id, customer_id, rating_rule_version_id, mode,
		flat_amount_cents, graduated_tiers, package_size, package_amount_cents,
		overage_unit_amount_cents, COALESCE(reason,''), active, created_at, updated_at
		FROM customer_price_overrides WHERE active = true`

	args := []any{}
	if customerID != "" {
		query += fmt.Sprintf(" AND customer_id = $%d", len(args)+1)
		args = append(args, customerID)
	}
	query += " ORDER BY created_at DESC"

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var overrides []domain.CustomerPriceOverride
	for rows.Next() {
		var o domain.CustomerPriceOverride
		var tiersJSON []byte
		if err := rows.Scan(&o.ID, &o.TenantID, &o.CustomerID, &o.RatingRuleVersionID,
			&o.Mode, &o.FlatAmountCents, &tiersJSON, &o.PackageSize, &o.PackageAmountCents,
			&o.OverageUnitAmountCents, &o.Reason, &o.Active, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(tiersJSON, &o.GraduatedTiers)
		overrides = append(overrides, o)
	}
	return overrides, rows.Err()
}

// Override handler methods on the pricing service.

type CreateOverrideInput struct {
	CustomerID             string              `json:"customer_id"`
	RatingRuleVersionID    string              `json:"rating_rule_version_id"`
	Mode                   domain.PricingMode  `json:"mode"`
	FlatAmountCents        int64               `json:"flat_amount_cents"`
	GraduatedTiers         []domain.RatingTier `json:"graduated_tiers"`
	PackageSize            int64               `json:"package_size"`
	PackageAmountCents     int64               `json:"package_amount_cents"`
	OverageUnitAmountCents int64               `json:"overage_unit_amount_cents"`
	Reason                 string              `json:"reason,omitempty"`
}

func (s *Service) CreateOverride(ctx context.Context, tenantID string, input CreateOverrideInput) (domain.CustomerPriceOverride, error) {
	if strings.TrimSpace(input.CustomerID) == "" {
		return domain.CustomerPriceOverride{}, errs.Required("customer_id")
	}
	if strings.TrimSpace(input.RatingRuleVersionID) == "" {
		return domain.CustomerPriceOverride{}, errs.Required("rating_rule_version_id")
	}

	// Validate pricing config
	testRule := domain.RatingRuleVersion{
		Mode:                   input.Mode,
		FlatAmountCents:        input.FlatAmountCents,
		GraduatedTiers:         input.GraduatedTiers,
		PackageSize:            input.PackageSize,
		PackageAmountCents:     input.PackageAmountCents,
		OverageUnitAmountCents: input.OverageUnitAmountCents,
	}
	if _, err := domain.ComputeAmountCents(testRule, 1); err != nil {
		return domain.CustomerPriceOverride{}, errs.Invalid("pricing", fmt.Sprintf("invalid pricing configuration: %v", err))
	}

	return s.store.CreateOverride(ctx, tenantID, domain.CustomerPriceOverride{
		CustomerID:             input.CustomerID,
		RatingRuleVersionID:    input.RatingRuleVersionID,
		Mode:                   input.Mode,
		FlatAmountCents:        input.FlatAmountCents,
		GraduatedTiers:         input.GraduatedTiers,
		PackageSize:            input.PackageSize,
		PackageAmountCents:     input.PackageAmountCents,
		OverageUnitAmountCents: input.OverageUnitAmountCents,
		Reason:                 strings.TrimSpace(input.Reason),
	})
}

func (s *Service) GetOverride(ctx context.Context, tenantID, customerID, ruleID string) (domain.CustomerPriceOverride, error) {
	return s.store.GetOverride(ctx, tenantID, customerID, ruleID)
}

func (s *Service) ListOverrides(ctx context.Context, tenantID, customerID string) ([]domain.CustomerPriceOverride, error) {
	return s.store.ListOverrides(ctx, tenantID, customerID)
}
