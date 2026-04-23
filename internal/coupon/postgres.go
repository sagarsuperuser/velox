package coupon

import (
	"context"
	"database/sql"
	"errors"
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

// couponColumns is the single source of truth for the SELECT list —
// keeping it here means every read path returns rows in the same order
// the scan helpers expect, so column adds only need to touch couponColumns
// + scanDest and not the four query sites.
const couponColumns = `id, tenant_id, code, name, type, amount_off, percent_off_bp,
	currency, max_redemptions, times_redeemed, expires_at, plan_ids,
	duration, duration_periods, stackable,
	COALESCE(customer_id, ''), restrictions, metadata, archived_at, created_at`

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
	metadataBytes := c.Metadata
	if len(metadataBytes) == 0 {
		metadataBytes = []byte(`{}`)
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO coupons (id, tenant_id, code, name, type, amount_off, percent_off_bp,
			currency, max_redemptions, times_redeemed, expires_at, plan_ids,
			duration, duration_periods, stackable,
			customer_id, restrictions, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$19)
		RETURNING `+couponColumns,
		c.ID, tenantID, c.Code, c.Name, c.Type, c.AmountOff, c.PercentOffBP,
		c.Currency, c.MaxRedemptions, 0, postgres.NullableTime(c.ExpiresAt),
		planIDs, c.Duration, c.DurationPeriods, c.Stackable,
		postgres.NullableString(c.CustomerID), c.Restrictions, metadataBytes, now,
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

func (s *PostgresStore) List(ctx context.Context, tenantID string, includeArchived bool) ([]domain.Coupon, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	q := `SELECT ` + couponColumns + ` FROM coupons`
	if !includeArchived {
		q += ` WHERE archived_at IS NULL`
	}
	q += ` ORDER BY created_at DESC LIMIT 500`

	rows, err := tx.QueryContext(ctx, q)
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

	metadataBytes := c.Metadata
	if len(metadataBytes) == 0 {
		metadataBytes = []byte(`{}`)
	}

	err = tx.QueryRowContext(ctx, `
		UPDATE coupons
		SET name = $2,
		    max_redemptions = $3,
		    expires_at = $4,
		    restrictions = $5,
		    metadata = $6,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+couponColumns,
		c.ID, c.Name, c.MaxRedemptions, postgres.NullableTime(c.ExpiresAt),
		c.Restrictions, metadataBytes,
	).Scan(scanDest(&c)...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Coupon{}, errs.ErrNotFound
		}
		return domain.Coupon{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Coupon{}, err
	}
	return c, nil
}

func (s *PostgresStore) Archive(ctx context.Context, tenantID, id string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	// Idempotent: COALESCE preserves the original archive timestamp if
	// the caller archives an already-archived row.
	res, err := tx.ExecContext(ctx, `
		UPDATE coupons
		SET archived_at = COALESCE(archived_at, $2), updated_at = now()
		WHERE id = $1
	`, id, at.UTC())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) Unarchive(ctx context.Context, tenantID, id string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `
		UPDATE coupons SET archived_at = NULL, updated_at = now() WHERE id = $1
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

// RedeemAtomic locks the coupon for update, validates live-state gates,
// increments times_redeemed, and inserts the redemption — all in one tx.
// Two failure modes beyond SQL errors: ErrCouponGate for a gate-fail
// (archived/expired/max), and a silently-returned existing row when the
// idempotency key collides (Replay=true in the result).
func (s *PostgresStore) RedeemAtomic(ctx context.Context, tenantID string, in RedeemAtomicInput) (RedeemAtomicResult, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return RedeemAtomicResult{}, err
	}
	defer postgres.Rollback(tx)

	// Lock the coupon row for the life of the tx. Any concurrent redeem
	// against the same code queues behind us — essential for the
	// max_redemptions gate to be correct.
	var c domain.Coupon
	err = tx.QueryRowContext(ctx, `
		SELECT `+couponColumns+` FROM coupons WHERE code = $1 FOR UPDATE
	`, in.Code).Scan(scanDest(&c)...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RedeemAtomicResult{}, ErrCouponGate{Reason: GateNotFound}
		}
		return RedeemAtomicResult{}, err
	}

	// Gate: archived, expired, max. Ordering matters only for the error
	// message — archived beats expired beats max because that's the
	// operator-controlled reason and most informative to the caller.
	now := time.Now()
	if c.ArchivedAt != nil {
		return RedeemAtomicResult{}, ErrCouponGate{Reason: GateArchived}
	}
	if c.ExpiresAt != nil && !c.ExpiresAt.After(now) {
		return RedeemAtomicResult{}, ErrCouponGate{Reason: GateExpired}
	}
	if c.MaxRedemptions != nil && c.TimesRedeemed >= *c.MaxRedemptions {
		return RedeemAtomicResult{}, ErrCouponGate{Reason: GateMaxRedemptions}
	}

	// Bump the counter first. If the redemption insert then fails (e.g.
	// subscription-unique collision), the tx rolls back and we're back
	// to the pre-redeem state — no counter drift.
	_, err = tx.ExecContext(ctx, `
		UPDATE coupons SET times_redeemed = times_redeemed + 1, updated_at = now()
		WHERE id = $1
	`, c.ID)
	if err != nil {
		return RedeemAtomicResult{}, err
	}
	c.TimesRedeemed++

	// Insert the redemption. ON CONFLICT handles the three partial-UNIQUE
	// indexes (idempotency, subscription, invoice) — the most common
	// legitimate collision is idempotency-key replay, which we surface as
	// Replay=true after a second fetch.
	redID := postgres.NewID("crd")
	nowUTC := now.UTC()
	var r domain.CouponRedemption
	err = tx.QueryRowContext(ctx, `
		INSERT INTO coupon_redemptions (id, tenant_id, coupon_id, customer_id,
			subscription_id, invoice_id, discount_cents, idempotency_key, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, tenant_id, coupon_id, customer_id,
			COALESCE(subscription_id,''), COALESCE(invoice_id,''),
			discount_cents, periods_applied, COALESCE(idempotency_key,''), created_at
	`, redID, tenantID, c.ID, in.CustomerID,
		postgres.NullableString(in.SubscriptionID), postgres.NullableString(in.InvoiceID),
		in.DiscountCents, postgres.NullableString(in.IdempotencyKey), nowUTC,
	).Scan(&r.ID, &r.TenantID, &r.CouponID, &r.CustomerID,
		&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.PeriodsApplied,
		&r.IdempotencyKey, &r.CreatedAt)
	if err != nil {
		// Idempotency-key collision: return the existing redemption as a
		// replay so the caller sees the same response they saw first time.
		// Must be done outside this tx (we'll roll back) because the
		// existing row is in its own tx.
		if postgres.IsUniqueViolation(err) && in.IdempotencyKey != "" && isIdempotencyCollision(err) {
			_ = tx.Rollback()
			return s.replayByIdempotencyKey(ctx, tenantID, in.IdempotencyKey)
		}
		return RedeemAtomicResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return RedeemAtomicResult{}, err
	}
	return RedeemAtomicResult{Coupon: c, Redemption: r, Replay: false}, nil
}

// idempotencyIndex is the name of the partial-unique index on
// coupon_redemptions(tenant_id, idempotency_key). Kept as a const so the
// constraint-name match below can't silently drift from the migration.
const idempotencyIndex = "idx_coupon_redemptions_idempotency"

// isIdempotencyCollision checks whether a unique-violation came from the
// idempotency index specifically (vs the subscription/invoice dedupe
// indexes). Uses the typed pgconn.PgError.ConstraintName via
// postgres.UniqueViolationConstraint — matching on err.Error() substrings
// is fragile to driver wrapping and locale changes.
func isIdempotencyCollision(err error) bool {
	return postgres.UniqueViolationConstraint(err) == idempotencyIndex
}

func (s *PostgresStore) replayByIdempotencyKey(ctx context.Context, tenantID, key string) (RedeemAtomicResult, error) {
	r, err := s.GetRedemptionByIdempotencyKey(ctx, tenantID, key)
	if err != nil {
		return RedeemAtomicResult{}, fmt.Errorf("idempotency replay lookup: %w", err)
	}
	c, err := s.Get(ctx, tenantID, r.CouponID)
	if err != nil {
		return RedeemAtomicResult{}, fmt.Errorf("idempotency replay coupon lookup: %w", err)
	}
	return RedeemAtomicResult{Coupon: c, Redemption: r, Replay: true}, nil
}

func (s *PostgresStore) GetRedemptionByIdempotencyKey(ctx context.Context, tenantID, key string) (domain.CouponRedemption, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CouponRedemption{}, err
	}
	defer postgres.Rollback(tx)

	var r domain.CouponRedemption
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, coupon_id, customer_id,
			COALESCE(subscription_id,''), COALESCE(invoice_id,''),
			discount_cents, periods_applied, COALESCE(idempotency_key,''), created_at
		FROM coupon_redemptions
		WHERE idempotency_key = $1
	`, key).Scan(&r.ID, &r.TenantID, &r.CouponID, &r.CustomerID,
		&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.PeriodsApplied,
		&r.IdempotencyKey, &r.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.CouponRedemption{}, errs.ErrNotFound
		}
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
			discount_cents, periods_applied, COALESCE(idempotency_key,''), created_at
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
			&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.PeriodsApplied,
			&r.IdempotencyKey, &r.CreatedAt); err != nil {
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
			discount_cents, periods_applied, COALESCE(idempotency_key,''), created_at
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
			&r.SubscriptionID, &r.InvoiceID, &r.DiscountCents, &r.PeriodsApplied,
			&r.IdempotencyKey, &r.CreatedAt); err != nil {
			return nil, err
		}
		redemptions = append(redemptions, r)
	}
	return redemptions, rows.Err()
}

func (s *PostgresStore) CountRedemptionsByCustomer(ctx context.Context, tenantID, couponID, customerID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return 0, err
	}
	defer postgres.Rollback(tx)

	var n int
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM coupon_redemptions
		WHERE coupon_id = $1 AND customer_id = $2
	`, couponID, customerID).Scan(&n)
	return n, err
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
		&c.ID, &c.TenantID, &c.Code, &c.Name, &c.Type, &c.AmountOff, &c.PercentOffBP,
		&c.Currency, &c.MaxRedemptions, &c.TimesRedeemed, &c.ExpiresAt,
		(*postgres.StringArray)(&c.PlanIDs),
		&c.Duration, &c.DurationPeriods, &c.Stackable,
		&c.CustomerID, &c.Restrictions, &c.Metadata, &c.ArchivedAt, &c.CreatedAt,
	}
}

// scanCoupon scans a single coupon from a *sql.Row.
func scanCoupon(row *sql.Row) (domain.Coupon, error) {
	var c domain.Coupon
	if err := row.Scan(scanDest(&c)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
