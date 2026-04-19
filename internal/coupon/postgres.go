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

	err = tx.QueryRowContext(ctx, `
		INSERT INTO coupons (id, tenant_id, code, name, type, amount_off, percent_off,
			currency, max_redemptions, times_redeemed, expires_at, plan_ids, active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)
		RETURNING id, tenant_id, code, name, type, amount_off, percent_off, currency,
			max_redemptions, times_redeemed, expires_at, plan_ids, active, created_at
	`, c.ID, tenantID, c.Code, c.Name, c.Type, c.AmountOff, c.PercentOff,
		c.Currency, c.MaxRedemptions, 0, postgres.NullableTime(c.ExpiresAt),
		planIDs, c.Active, now,
	).Scan(&c.ID, &c.TenantID, &c.Code, &c.Name, &c.Type, &c.AmountOff, &c.PercentOff,
		&c.Currency, &c.MaxRedemptions, &c.TimesRedeemed, &c.ExpiresAt,
		(*postgres.StringArray)(&c.PlanIDs), &c.Active, &c.CreatedAt)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Coupon{}, fmt.Errorf("%w: coupon code %q", errs.ErrAlreadyExists, c.Code)
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
		SELECT id, tenant_id, code, name, type, amount_off, percent_off, currency,
			max_redemptions, times_redeemed, expires_at, plan_ids, active, created_at
		FROM coupons WHERE id = $1
	`, id))
}

func (s *PostgresStore) GetByCode(ctx context.Context, tenantID, code string) (domain.Coupon, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Coupon{}, err
	}
	defer postgres.Rollback(tx)

	return scanCoupon(tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, code, name, type, amount_off, percent_off, currency,
			max_redemptions, times_redeemed, expires_at, plan_ids, active, created_at
		FROM coupons WHERE code = $1
	`, code))
}

func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]domain.Coupon, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, code, name, type, amount_off, percent_off, currency,
			max_redemptions, times_redeemed, expires_at, plan_ids, active, created_at
		FROM coupons ORDER BY created_at DESC
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
		RETURNING id, tenant_id, code, name, type, amount_off, percent_off, currency,
			max_redemptions, times_redeemed, expires_at, plan_ids, active, created_at
	`, c.ID, c.Name, c.MaxRedemptions, postgres.NullableTime(c.ExpiresAt),
	).Scan(&c.ID, &c.TenantID, &c.Code, &c.Name, &c.Type, &c.AmountOff, &c.PercentOff,
		&c.Currency, &c.MaxRedemptions, &c.TimesRedeemed, &c.ExpiresAt,
		(*postgres.StringArray)(&c.PlanIDs), &c.Active, &c.CreatedAt)
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
			COALESCE(subscription_id,''), COALESCE(invoice_id,''), discount_cents, created_at
	`, r.ID, tenantID, r.CouponID, r.CustomerID,
		postgres.NullableString(r.SubscriptionID), postgres.NullableString(r.InvoiceID),
		r.DiscountCents, now,
	).Scan(&r.ID, &r.TenantID, &r.CouponID, &r.CustomerID,
		&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.CreatedAt)
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
			COALESCE(subscription_id,''), COALESCE(invoice_id,''), discount_cents, created_at
		FROM coupon_redemptions WHERE coupon_id = $1
		ORDER BY created_at DESC
	`, couponID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var redemptions []domain.CouponRedemption
	for rows.Next() {
		var r domain.CouponRedemption
		if err := rows.Scan(&r.ID, &r.TenantID, &r.CouponID, &r.CustomerID,
			&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.CreatedAt); err != nil {
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
			COALESCE(subscription_id,''), COALESCE(invoice_id,''), discount_cents, created_at
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
			&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.CreatedAt); err != nil {
			return nil, err
		}
		redemptions = append(redemptions, r)
	}
	return redemptions, rows.Err()
}

// scanCoupon scans a single coupon from a *sql.Row.
func scanCoupon(row *sql.Row) (domain.Coupon, error) {
	var c domain.Coupon
	err := row.Scan(&c.ID, &c.TenantID, &c.Code, &c.Name, &c.Type, &c.AmountOff, &c.PercentOff,
		&c.Currency, &c.MaxRedemptions, &c.TimesRedeemed, &c.ExpiresAt,
		(*postgres.StringArray)(&c.PlanIDs), &c.Active, &c.CreatedAt)
	if err != nil {
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
	err := rows.Scan(&c.ID, &c.TenantID, &c.Code, &c.Name, &c.Type, &c.AmountOff, &c.PercentOff,
		&c.Currency, &c.MaxRedemptions, &c.TimesRedeemed, &c.ExpiresAt,
		(*postgres.StringArray)(&c.PlanIDs), &c.Active, &c.CreatedAt)
	return c, err
}
