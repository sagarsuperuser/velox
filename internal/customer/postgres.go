package customer

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

func (s *PostgresStore) Create(ctx context.Context, tenantID string, c domain.Customer) (domain.Customer, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Customer{}, err
	}
	defer postgres.Rollback(tx)

	id := postgres.NewID("vlx_cus")
	now := time.Now().UTC()

	err = tx.QueryRowContext(ctx, `
		INSERT INTO customers (id, tenant_id, external_id, display_name, email, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		RETURNING id, tenant_id, external_id, display_name, COALESCE(email,''), status, created_at, updated_at
	`, id, tenantID, c.ExternalID, c.DisplayName, postgres.NullableString(c.Email),
		domain.CustomerStatusActive, now,
	).Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status, &c.CreatedAt, &c.UpdatedAt)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Customer{}, fmt.Errorf("%w: customer with external_id %q already exists", errs.ErrAlreadyExists, c.ExternalID)
		}
		return domain.Customer{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Customer{}, err
	}
	return c, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (domain.Customer, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Customer{}, err
	}
	defer postgres.Rollback(tx)

	var c domain.Customer
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, external_id, display_name, COALESCE(email, ''), status, created_at, updated_at
		FROM customers WHERE id = $1
	`, id).Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status, &c.CreatedAt, &c.UpdatedAt)

	if err == sql.ErrNoRows {
		return domain.Customer{}, errs.ErrNotFound
	}
	return c, err
}

func (s *PostgresStore) GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Customer{}, err
	}
	defer postgres.Rollback(tx)

	var c domain.Customer
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, external_id, display_name, COALESCE(email, ''), status, created_at, updated_at
		FROM customers WHERE external_id = $1
	`, externalID).Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status, &c.CreatedAt, &c.UpdatedAt)

	if err == sql.ErrNoRows {
		return domain.Customer{}, errs.ErrNotFound
	}
	return c, err
}

func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]domain.Customer, int, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, 0, err
	}
	defer postgres.Rollback(tx)

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	where, args := buildCustomerWhere(filter)

	var total int
	countQuery := `SELECT COUNT(*) FROM customers` + where
	if err := tx.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT id, tenant_id, external_id, display_name, COALESCE(email, ''), status, created_at, updated_at
		FROM customers` + where + ` ORDER BY created_at DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1) + ` OFFSET $` + fmt.Sprintf("%d", len(args)+2)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var customers []domain.Customer
	for rows.Next() {
		var c domain.Customer
		if err := rows.Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, 0, err
		}
		customers = append(customers, c)
	}
	return customers, total, rows.Err()
}

func (s *PostgresStore) Update(ctx context.Context, tenantID string, c domain.Customer) (domain.Customer, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Customer{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `
		UPDATE customers SET display_name = $1, email = $2, status = $3, updated_at = $4
		WHERE id = $5
		RETURNING id, tenant_id, external_id, display_name, COALESCE(email, ''), status, created_at, updated_at
	`, c.DisplayName, postgres.NullableString(c.Email), c.Status, now, c.ID,
	).Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status, &c.CreatedAt, &c.UpdatedAt)

	if err == sql.ErrNoRows {
		return domain.Customer{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Customer{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Customer{}, err
	}
	return c, nil
}

func (s *PostgresStore) UpsertBillingProfile(ctx context.Context, tenantID string, bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CustomerBillingProfile{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO customer_billing_profiles (customer_id, tenant_id, legal_name, email, phone,
			address_line1, address_line2, city, state, postal_code, country, currency,
			tax_identifier, profile_status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $15)
		ON CONFLICT (tenant_id, customer_id) DO UPDATE SET
			legal_name = EXCLUDED.legal_name, email = EXCLUDED.email, phone = EXCLUDED.phone,
			address_line1 = EXCLUDED.address_line1, address_line2 = EXCLUDED.address_line2,
			city = EXCLUDED.city, state = EXCLUDED.state, postal_code = EXCLUDED.postal_code,
			country = EXCLUDED.country, currency = EXCLUDED.currency,
			tax_identifier = EXCLUDED.tax_identifier, profile_status = EXCLUDED.profile_status,
			updated_at = EXCLUDED.updated_at
		RETURNING customer_id, tenant_id, COALESCE(legal_name,''), COALESCE(email,''), COALESCE(phone,''),
			COALESCE(address_line1,''), COALESCE(address_line2,''), COALESCE(city,''), COALESCE(state,''),
			COALESCE(postal_code,''), COALESCE(country,''), COALESCE(currency,''),
			COALESCE(tax_identifier,''), profile_status, created_at, updated_at
	`, bp.CustomerID, tenantID, postgres.NullableString(bp.LegalName), postgres.NullableString(bp.Email),
		postgres.NullableString(bp.Phone), postgres.NullableString(bp.AddressLine1),
		postgres.NullableString(bp.AddressLine2), postgres.NullableString(bp.City),
		postgres.NullableString(bp.State), postgres.NullableString(bp.PostalCode),
		postgres.NullableString(bp.Country), postgres.NullableString(bp.Currency),
		postgres.NullableString(bp.TaxIdentifier), bp.ProfileStatus, now,
	).Scan(
		&bp.CustomerID, &bp.TenantID, &bp.LegalName, &bp.Email, &bp.Phone,
		&bp.AddressLine1, &bp.AddressLine2, &bp.City, &bp.State, &bp.PostalCode,
		&bp.Country, &bp.Currency, &bp.TaxIdentifier, &bp.ProfileStatus,
		&bp.CreatedAt, &bp.UpdatedAt,
	)
	if err != nil {
		return domain.CustomerBillingProfile{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.CustomerBillingProfile{}, err
	}
	return bp, nil
}

func (s *PostgresStore) GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CustomerBillingProfile{}, err
	}
	defer postgres.Rollback(tx)

	var bp domain.CustomerBillingProfile
	err = tx.QueryRowContext(ctx, `
		SELECT customer_id, tenant_id, COALESCE(legal_name,''), COALESCE(email,''), COALESCE(phone,''),
			COALESCE(address_line1,''), COALESCE(address_line2,''), COALESCE(city,''), COALESCE(state,''),
			COALESCE(postal_code,''), COALESCE(country,''), COALESCE(currency,''),
			COALESCE(tax_identifier,''), profile_status, created_at, updated_at
		FROM customer_billing_profiles WHERE customer_id = $1
	`, customerID).Scan(
		&bp.CustomerID, &bp.TenantID, &bp.LegalName, &bp.Email, &bp.Phone,
		&bp.AddressLine1, &bp.AddressLine2, &bp.City, &bp.State, &bp.PostalCode,
		&bp.Country, &bp.Currency, &bp.TaxIdentifier, &bp.ProfileStatus,
		&bp.CreatedAt, &bp.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return domain.CustomerBillingProfile{}, errs.ErrNotFound
	}
	return bp, err
}

func (s *PostgresStore) UpsertPaymentSetup(ctx context.Context, tenantID string, ps domain.CustomerPaymentSetup) (domain.CustomerPaymentSetup, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CustomerPaymentSetup{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO customer_payment_setups (customer_id, tenant_id, setup_status,
			default_payment_method_present, payment_method_type,
			stripe_customer_id, stripe_payment_method_id, last_verified_at,
			created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		ON CONFLICT (tenant_id, customer_id) DO UPDATE SET
			setup_status = EXCLUDED.setup_status,
			default_payment_method_present = EXCLUDED.default_payment_method_present,
			payment_method_type = EXCLUDED.payment_method_type,
			stripe_customer_id = EXCLUDED.stripe_customer_id,
			stripe_payment_method_id = EXCLUDED.stripe_payment_method_id,
			last_verified_at = EXCLUDED.last_verified_at,
			updated_at = EXCLUDED.updated_at
		RETURNING customer_id, tenant_id, setup_status, default_payment_method_present,
			COALESCE(payment_method_type,''), COALESCE(stripe_customer_id,''),
			COALESCE(stripe_payment_method_id,''), last_verified_at, created_at, updated_at
	`, ps.CustomerID, tenantID, ps.SetupStatus, ps.DefaultPaymentMethodPresent,
		postgres.NullableString(ps.PaymentMethodType), postgres.NullableString(ps.StripeCustomerID),
		postgres.NullableString(ps.StripePaymentMethodID), postgres.NullableTime(ps.LastVerifiedAt), now,
	).Scan(
		&ps.CustomerID, &ps.TenantID, &ps.SetupStatus, &ps.DefaultPaymentMethodPresent,
		&ps.PaymentMethodType, &ps.StripeCustomerID, &ps.StripePaymentMethodID,
		&ps.LastVerifiedAt, &ps.CreatedAt, &ps.UpdatedAt,
	)
	if err != nil {
		return domain.CustomerPaymentSetup{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.CustomerPaymentSetup{}, err
	}
	return ps, nil
}

func (s *PostgresStore) GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CustomerPaymentSetup{}, err
	}
	defer postgres.Rollback(tx)

	var ps domain.CustomerPaymentSetup
	err = tx.QueryRowContext(ctx, `
		SELECT customer_id, tenant_id, setup_status, default_payment_method_present,
			COALESCE(payment_method_type,''), COALESCE(stripe_customer_id,''),
			COALESCE(stripe_payment_method_id,''), last_verified_at, created_at, updated_at
		FROM customer_payment_setups WHERE customer_id = $1
	`, customerID).Scan(
		&ps.CustomerID, &ps.TenantID, &ps.SetupStatus, &ps.DefaultPaymentMethodPresent,
		&ps.PaymentMethodType, &ps.StripeCustomerID, &ps.StripePaymentMethodID,
		&ps.LastVerifiedAt, &ps.CreatedAt, &ps.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return domain.CustomerPaymentSetup{}, errs.ErrNotFound
	}
	return ps, err
}

func buildCustomerWhere(f ListFilter) (string, []any) {
	var clauses []string
	var args []any
	idx := 1

	if f.Status != "" {
		clauses = append(clauses, fmt.Sprintf("status = $%d", idx))
		args = append(args, f.Status)
		idx++
	}
	if f.ExternalID != "" {
		clauses = append(clauses, fmt.Sprintf("external_id = $%d", idx))
		args = append(args, f.ExternalID)
		idx++
	}

	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + join(clauses, " AND "), args
}

func join(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}
