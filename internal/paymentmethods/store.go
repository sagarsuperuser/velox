package paymentmethods

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Store is the persistence contract. Kept narrow — each operation maps to
// exactly one handler action so we don't grow a god-store.
type Store interface {
	List(ctx context.Context, tenantID, customerID string) ([]PaymentMethod, error)
	Get(ctx context.Context, tenantID, pmID string) (PaymentMethod, error)

	// Upsert writes a payment_methods row keyed by stripe_payment_method_id.
	// Webhooks can fire more than once for the same setup intent (Stripe
	// retries on 5xx), so the webhook path must be idempotent. If first is
	// true and no existing active default for the customer, the new row is
	// promoted to default atomically in the same tx.
	Upsert(ctx context.Context, tenantID string, pm PaymentMethod) (PaymentMethod, error)

	// SetDefault atomically clears any existing default for (customerID)
	// and flags pmID as the new default. Fails with ErrNotFound if pmID is
	// detached or not owned by customerID.
	SetDefault(ctx context.Context, tenantID, customerID, pmID string) (PaymentMethod, error)

	// Detach marks the PM as detached_at = now(). Idempotent — a second
	// call on an already-detached row is a no-op and returns the row.
	Detach(ctx context.Context, tenantID, customerID, pmID string) (PaymentMethod, error)
}

type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore { return &PostgresStore{db: db} }

const pmSelectCols = `id, tenant_id, livemode, customer_id, stripe_payment_method_id,
	type, COALESCE(card_brand,''), COALESCE(card_last4,''),
	COALESCE(card_exp_month,0), COALESCE(card_exp_year,0),
	is_default, detached_at, created_at, updated_at`

func scanPM(row interface {
	Scan(dest ...any) error
}, pm *PaymentMethod) error {
	return row.Scan(&pm.ID, &pm.TenantID, &pm.Livemode, &pm.CustomerID, &pm.StripePaymentMethodID,
		&pm.Type, &pm.CardBrand, &pm.CardLast4, &pm.CardExpMonth, &pm.CardExpYear,
		&pm.IsDefault, &pm.DetachedAt, &pm.CreatedAt, &pm.UpdatedAt)
}

func (s *PostgresStore) List(ctx context.Context, tenantID, customerID string) ([]PaymentMethod, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT `+pmSelectCols+` FROM payment_methods
		WHERE tenant_id = $1 AND customer_id = $2 AND detached_at IS NULL
		ORDER BY is_default DESC, created_at DESC LIMIT 100`, tenantID, customerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []PaymentMethod
	for rows.Next() {
		var pm PaymentMethod
		if err := scanPM(rows, &pm); err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, pmID string) (PaymentMethod, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return PaymentMethod{}, err
	}
	defer postgres.Rollback(tx)

	var pm PaymentMethod
	if err := scanPM(tx.QueryRowContext(ctx, `
		SELECT `+pmSelectCols+` FROM payment_methods
		WHERE tenant_id = $1 AND id = $2`, tenantID, pmID), &pm); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PaymentMethod{}, errs.ErrNotFound
		}
		return PaymentMethod{}, err
	}
	return pm, tx.Commit()
}

// Upsert inserts a row keyed by (tenant_id, livemode, stripe_payment_method_id).
// On conflict we refresh card metadata but leave is_default alone — the
// customer's default choice shouldn't flip just because Stripe resent the
// webhook. If no active default exists for the customer, the new row is
// promoted to default in the same tx (enforces "first PM is default").
func (s *PostgresStore) Upsert(ctx context.Context, tenantID string, pm PaymentMethod) (PaymentMethod, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return PaymentMethod{}, err
	}
	defer postgres.Rollback(tx)

	// Does this customer already have an active default?
	var hasDefault bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM payment_methods
			WHERE tenant_id = $1 AND customer_id = $2
			  AND is_default = true AND detached_at IS NULL
		)`, tenantID, pm.CustomerID).Scan(&hasDefault); err != nil {
		return PaymentMethod{}, fmt.Errorf("check default: %w", err)
	}
	promote := !hasDefault

	var out PaymentMethod
	if err := scanPM(tx.QueryRowContext(ctx, `
		INSERT INTO payment_methods
			(tenant_id, customer_id, stripe_payment_method_id, type,
			 card_brand, card_last4, card_exp_month, card_exp_year,
			 is_default, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''),
		        NULLIF($7,0), NULLIF($8,0), $9, now(), now())
		ON CONFLICT (tenant_id, livemode, stripe_payment_method_id) DO UPDATE
		SET card_brand     = EXCLUDED.card_brand,
		    card_last4     = EXCLUDED.card_last4,
		    card_exp_month = EXCLUDED.card_exp_month,
		    card_exp_year  = EXCLUDED.card_exp_year,
		    detached_at    = NULL,
		    updated_at     = now()
		RETURNING `+pmSelectCols,
		tenantID, pm.CustomerID, pm.StripePaymentMethodID, defaultString(pm.Type, "card"),
		pm.CardBrand, pm.CardLast4, pm.CardExpMonth, pm.CardExpYear,
		promote), &out); err != nil {
		return PaymentMethod{}, fmt.Errorf("upsert payment method: %w", err)
	}

	return out, tx.Commit()
}

// SetDefault does the atomic swap inside one tx so RLS + partial unique
// index see both writes consistently.
func (s *PostgresStore) SetDefault(ctx context.Context, tenantID, customerID, pmID string) (PaymentMethod, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return PaymentMethod{}, err
	}
	defer postgres.Rollback(tx)

	// Clear any other default for this customer first. Postgres validates
	// the partial unique index per-row; clearing before setting avoids a
	// transient violation that would rollback the whole tx.
	if _, err := tx.ExecContext(ctx, `
		UPDATE payment_methods SET is_default = false, updated_at = now()
		WHERE tenant_id = $1 AND customer_id = $2
		  AND is_default = true AND detached_at IS NULL
		  AND id <> $3`, tenantID, customerID, pmID); err != nil {
		return PaymentMethod{}, fmt.Errorf("clear existing default: %w", err)
	}

	var out PaymentMethod
	if err := scanPM(tx.QueryRowContext(ctx, `
		UPDATE payment_methods SET is_default = true, updated_at = now()
		WHERE tenant_id = $1 AND customer_id = $2 AND id = $3
		  AND detached_at IS NULL
		RETURNING `+pmSelectCols,
		tenantID, customerID, pmID), &out); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PaymentMethod{}, errs.ErrNotFound
		}
		return PaymentMethod{}, fmt.Errorf("set default: %w", err)
	}

	return out, tx.Commit()
}

// Detach is idempotent by design. Re-detaching a row is a no-op write
// (detached_at is kept at its original timestamp).
func (s *PostgresStore) Detach(ctx context.Context, tenantID, customerID, pmID string) (PaymentMethod, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return PaymentMethod{}, err
	}
	defer postgres.Rollback(tx)

	var out PaymentMethod
	if err := scanPM(tx.QueryRowContext(ctx, `
		UPDATE payment_methods
		SET detached_at = COALESCE(detached_at, now()),
		    is_default  = false,
		    updated_at  = now()
		WHERE tenant_id = $1 AND customer_id = $2 AND id = $3
		RETURNING `+pmSelectCols,
		tenantID, customerID, pmID), &out); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PaymentMethod{}, errs.ErrNotFound
		}
		return PaymentMethod{}, fmt.Errorf("detach: %w", err)
	}

	return out, tx.Commit()
}

func defaultString(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Compile-time assertion that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)
