package coupon

import (
	"context"
	"database/sql"
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

// couponColumns is the single source of truth for the SELECT list — keeping
// it here means every read path returns rows in the same order the scan
// helpers expect, and FEAT-6's duration/stackable columns don't need to be
// threaded through four copy-pasted queries.
const couponColumns = `id, tenant_id, code, name, type, amount_off, percent_off,
	currency, max_redemptions, times_redeemed, expires_at, plan_ids,
	duration, duration_periods, stackable, active, created_at`

func (s *PostgresStore) Create(ctx context.Context, tenantID string, c domain.Coupon) (domain.Coupon, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Coupon{}, err
	}
	defer postgres.Rollback(tx)

	c.ID = postgres.NewID("cpn")
	now := time.Now().UTC()

	planIDs := postgres.StringArray(c.PlanIDs)
	if planIDs == nil {
		planIDs = postgres.StringArray{}
	}
	if c.Duration == "" {
		c.Duration = domain.CouponDurationForever
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO coupons (id, tenant_id, code, name, type, amount_off, percent_off,
			currency, max_redemptions, times_redeemed, expires_at, plan_ids,
			duration, duration_periods, stackable,
			active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)
		RETURNING `+couponColumns,
		c.ID, tenantID, c.Code, c.Name, c.Type, c.AmountOff, c.PercentOff,
		c.Currency, c.MaxRedemptions, 0, postgres.NullableTime(c.ExpiresAt),
		planIDs, c.Duration, c.DurationPeriods, c.Stackable,
		c.Active, now,
	).Scan(scanDest(&c)...)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Coupon{}, errs.AlreadyExists("code", fmt.Sprintf("coupon code %q already exists", c.Code))
		}
		return domain.Coupon{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Coupon{}, err
	}
	return c, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (domain.Coupon, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Coupon{}, err
	}
	defer postgres.Rollback(tx)

	return scanCoupon(tx.QueryRowContext(ctx, `
		SELECT `+couponColumns+` FROM coupons WHERE id = $1
	`, id))
}

func (s *PostgresStore) GetByCode(ctx context.Context, tenantID, code string) (domain.Coupon, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Coupon{}, err
	}
	defer postgres.Rollback(tx)

	return scanCoupon(tx.QueryRowContext(ctx, `
		SELECT `+couponColumns+` FROM coupons WHERE code = $1
	`, code))
}

func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]domain.Coupon, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+couponColumns+` FROM coupons ORDER BY created_at DESC LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var coupons []domain.Coupon
	for rows.Next() {
		c, err := scanCouponRow(rows)
		if err != nil {
			return nil, err
		}
		coupons = append(coupons, c)
	}
	return coupons, rows.Err()
}

func (s *PostgresStore) Update(ctx context.Context, tenantID string, c domain.Coupon) (domain.Coupon, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Coupon{}, err
	}
	defer postgres.Rollback(tx)

	err = tx.QueryRowContext(ctx, `
		UPDATE coupons SET name = $2, max_redemptions = $3, expires_at = $4, updated_at = now()
		WHERE id = $1
		RETURNING `+couponColumns,
		c.ID, c.Name, c.MaxRedemptions, postgres.NullableTime(c.ExpiresAt),
	).Scan(scanDest(&c)...)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Coupon{}, errs.ErrNotFound
		}
		return domain.Coupon{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Coupon{}, err
	}
	return c, nil
}

func (s *PostgresStore) Deactivate(ctx context.Context, tenantID, id string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `
		UPDATE coupons SET active = false, updated_at = now() WHERE id = $1
	`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) IncrementRedemptions(ctx context.Context, tenantID, id string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `
		UPDATE coupons SET times_redeemed = times_redeemed + 1, updated_at = now() WHERE id = $1
	`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) CreateRedemption(ctx context.Context, tenantID string, r domain.CouponRedemption) (domain.CouponRedemption, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CouponRedemption{}, err
	}
	defer postgres.Rollback(tx)

	r.ID = postgres.NewID("crd")
	now := time.Now().UTC()

	err = tx.QueryRowContext(ctx, `
		INSERT INTO coupon_redemptions (id, tenant_id, coupon_id, customer_id,
			subscription_id, invoice_id, discount_cents, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, tenant_id, coupon_id, customer_id,
			COALESCE(subscription_id,''), COALESCE(invoice_id,''),
			discount_cents, periods_applied, created_at
	`, r.ID, tenantID, r.CouponID, r.CustomerID,
		postgres.NullableString(r.SubscriptionID), postgres.NullableString(r.InvoiceID),
		r.DiscountCents, now,
	).Scan(&r.ID, &r.TenantID, &r.CouponID, &r.CustomerID,
		&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.PeriodsApplied, &r.CreatedAt)
	if err != nil {
		return domain.CouponRedemption{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.CouponRedemption{}, err
	}
	return r, nil
}

func (s *PostgresStore) ListRedemptions(ctx context.Context, tenantID, couponID string) ([]domain.CouponRedemption, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, coupon_id, customer_id,
			COALESCE(subscription_id,''), COALESCE(invoice_id,''),
			discount_cents, periods_applied, created_at
		FROM coupon_redemptions WHERE coupon_id = $1
		ORDER BY created_at DESC LIMIT 1000
	`, couponID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var redemptions []domain.CouponRedemption
	for rows.Next() {
		var r domain.CouponRedemption
		if err := rows.Scan(&r.ID, &r.TenantID, &r.CouponID, &r.CustomerID,
			&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.PeriodsApplied, &r.CreatedAt); err != nil {
			return nil, err
		}
		redemptions = append(redemptions, r)
	}
	return redemptions, rows.Err()
}

func (s *PostgresStore) ListRedemptionsBySubscription(ctx context.Context, tenantID, subscriptionID string) ([]domain.CouponRedemption, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, coupon_id, customer_id,
			COALESCE(subscription_id,''), COALESCE(invoice_id,''),
			discount_cents, periods_applied, created_at
		FROM coupon_redemptions WHERE subscription_id = $1
		ORDER BY created_at ASC
	`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var redemptions []domain.CouponRedemption
	for rows.Next() {
		var r domain.CouponRedemption
		if err := rows.Scan(&r.ID, &r.TenantID, &r.CouponID, &r.CustomerID,
			&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.PeriodsApplied, &r.CreatedAt); err != nil {
			return nil, err
		}
		redemptions = append(redemptions, r)
	}
	return redemptions, rows.Err()
}

// IncrementPeriodsApplied advances the redemption's counter by one. Not
// guarded against double-increment because the billing engine is the only
// caller and it invokes this once per (invoice, redemption) after a
// successful invoice create — duplicate calls would indicate a bug
// upstream, not a race worth encoding here.
func (s *PostgresStore) IncrementPeriodsApplied(ctx context.Context, tenantID, redemptionID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `
		UPDATE coupon_redemptions
		SET periods_applied = periods_applied + 1
		WHERE id = $1
	`, redemptionID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

// scanDest returns the pointers for Scan in the exact order of couponColumns.
// Centralising this makes sure Create / Update / Get / List all agree on the
// column order — otherwise renaming or adding a column silently goes out of
// sync with one of the four SQL sites.
func scanDest(c *domain.Coupon) []any {
	return []any{
		&c.ID, &c.TenantID, &c.Code, &c.Name, &c.Type, &c.AmountOff, &c.PercentOff,
		&c.Currency, &c.MaxRedemptions, &c.TimesRedeemed, &c.ExpiresAt,
		(*postgres.StringArray)(&c.PlanIDs),
		&c.Duration, &c.DurationPeriods, &c.Stackable,
		&c.Active, &c.CreatedAt,
	}
}

// scanCoupon scans a single coupon from a *sql.Row.
func scanCoupon(row *sql.Row) (domain.Coupon, error) {
	var c domain.Coupon
	if err := row.Scan(scanDest(&c)...); err != nil {
		if err == sql.ErrNoRows {
			return domain.Coupon{}, errs.ErrNotFound
		}
		return domain.Coupon{}, err
	}
	return c, nil
}

// scanCouponRow scans a coupon from *sql.Rows.
func scanCouponRow(rows *sql.Rows) (domain.Coupon, error) {
	var c domain.Coupon
	err := rows.Scan(scanDest(&c)...)
	return c, err
}
