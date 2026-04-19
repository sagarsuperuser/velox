package customer

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db  *postgres.DB
	enc *crypto.Encryptor
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// SetEncryptor configures PII field encryption for customer data at rest.
// When set, email, display_name (Customer) and legal_name, email, phone,
// tax_identifier, tax_id (BillingProfile) are encrypted before write and
// decrypted after read.
func (s *PostgresStore) SetEncryptor(enc *crypto.Encryptor) {
	s.enc = enc
}

// encryptCustomer encrypts PII fields on a Customer before writing to the DB.
func (s *PostgresStore) encryptCustomer(c domain.Customer) (domain.Customer, error) {
	if s.enc == nil {
		return c, nil
	}
	var err error
	if c.Email, err = s.enc.Encrypt(c.Email); err != nil {
		return c, fmt.Errorf("encrypt customer email: %w", err)
	}
	if c.DisplayName, err = s.enc.Encrypt(c.DisplayName); err != nil {
		return c, fmt.Errorf("encrypt customer display_name: %w", err)
	}
	return c, nil
}

// decryptCustomer decrypts PII fields on a Customer after reading from the DB.
func (s *PostgresStore) decryptCustomer(c domain.Customer) (domain.Customer, error) {
	if s.enc == nil {
		return c, nil
	}
	var err error
	if c.Email, err = s.enc.Decrypt(c.Email); err != nil {
		return c, fmt.Errorf("decrypt customer email: %w", err)
	}
	if c.DisplayName, err = s.enc.Decrypt(c.DisplayName); err != nil {
		return c, fmt.Errorf("decrypt customer display_name: %w", err)
	}
	return c, nil
}

// encryptBillingProfile encrypts PII fields on a BillingProfile before writing.
func (s *PostgresStore) encryptBillingProfile(bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error) {
	if s.enc == nil {
		return bp, nil
	}
	var err error
	if bp.LegalName, err = s.enc.Encrypt(bp.LegalName); err != nil {
		return bp, fmt.Errorf("encrypt billing legal_name: %w", err)
	}
	if bp.Email, err = s.enc.Encrypt(bp.Email); err != nil {
		return bp, fmt.Errorf("encrypt billing email: %w", err)
	}
	if bp.Phone, err = s.enc.Encrypt(bp.Phone); err != nil {
		return bp, fmt.Errorf("encrypt billing phone: %w", err)
	}
	if bp.TaxID, err = s.enc.Encrypt(bp.TaxID); err != nil {
		return bp, fmt.Errorf("encrypt billing tax_id: %w", err)
	}
	return bp, nil
}

// decryptBillingProfile decrypts PII fields on a BillingProfile after reading.
func (s *PostgresStore) decryptBillingProfile(bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error) {
	if s.enc == nil {
		return bp, nil
	}
	var err error
	if bp.LegalName, err = s.enc.Decrypt(bp.LegalName); err != nil {
		return bp, fmt.Errorf("decrypt billing legal_name: %w", err)
	}
	if bp.Email, err = s.enc.Decrypt(bp.Email); err != nil {
		return bp, fmt.Errorf("decrypt billing email: %w", err)
	}
	if bp.Phone, err = s.enc.Decrypt(bp.Phone); err != nil {
		return bp, fmt.Errorf("decrypt billing phone: %w", err)
	}
	if bp.TaxID, err = s.enc.Decrypt(bp.TaxID); err != nil {
		return bp, fmt.Errorf("decrypt billing tax_id: %w", err)
	}
	return bp, nil
}

func (s *PostgresStore) Create(ctx context.Context, tenantID string, c domain.Customer) (domain.Customer, error) {
	enc, err := s.encryptCustomer(c)
	if err != nil {
		return domain.Customer{}, err
	}

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
		RETURNING id, tenant_id, external_id, display_name, email, status, created_at, updated_at
	`, id, tenantID, c.ExternalID, enc.DisplayName, enc.Email,
		domain.CustomerStatusActive, now,
	).Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status, &c.CreatedAt, &c.UpdatedAt)

	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return domain.Customer{}, errs.AlreadyExists("external_id", fmt.Sprintf("customer with external_id %q already exists", c.ExternalID))
		}
		return domain.Customer{}, err
	}

	c, err = s.decryptCustomer(c)
	if err != nil {
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
	if err != nil {
		return domain.Customer{}, err
	}
	return s.decryptCustomer(c)
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
	if err != nil {
		return domain.Customer{}, err
	}
	return s.decryptCustomer(c)
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
	defer func() { _ = rows.Close() }()

	var customers []domain.Customer
	for rows.Next() {
		var c domain.Customer
		if err := rows.Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, 0, err
		}
		c, err = s.decryptCustomer(c)
		if err != nil {
			return nil, 0, err
		}
		customers = append(customers, c)
	}
	return customers, total, rows.Err()
}

func (s *PostgresStore) Update(ctx context.Context, tenantID string, c domain.Customer) (domain.Customer, error) {
	enc, err := s.encryptCustomer(c)
	if err != nil {
		return domain.Customer{}, err
	}

	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.Customer{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	err = tx.QueryRowContext(ctx, `
		UPDATE customers SET display_name = $1, email = $2, status = $3, updated_at = $4
		WHERE id = $5
		RETURNING id, tenant_id, external_id, display_name, email, status, created_at, updated_at
	`, enc.DisplayName, enc.Email, c.Status, now, c.ID,
	).Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status, &c.CreatedAt, &c.UpdatedAt)

	if err == sql.ErrNoRows {
		return domain.Customer{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Customer{}, err
	}

	c, err = s.decryptCustomer(c)
	if err != nil {
		return domain.Customer{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Customer{}, err
	}
	return c, nil
}

func (s *PostgresStore) UpsertBillingProfile(ctx context.Context, tenantID string, bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error) {
	enc, err := s.encryptBillingProfile(bp)
	if err != nil {
		return domain.CustomerBillingProfile{}, err
	}

	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.CustomerBillingProfile{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	var overrideBP sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO customer_billing_profiles (customer_id, tenant_id, legal_name, email, phone,
			address_line1, address_line2, city, state, postal_code, country, currency,
			tax_exempt, tax_id, tax_id_type, tax_override_rate_bp,
			profile_status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $18)
		ON CONFLICT (tenant_id, customer_id) DO UPDATE SET
			legal_name = EXCLUDED.legal_name, email = EXCLUDED.email, phone = EXCLUDED.phone,
			address_line1 = EXCLUDED.address_line1, address_line2 = EXCLUDED.address_line2,
			city = EXCLUDED.city, state = EXCLUDED.state, postal_code = EXCLUDED.postal_code,
			country = EXCLUDED.country, currency = EXCLUDED.currency,
			tax_exempt = EXCLUDED.tax_exempt,
			tax_id = EXCLUDED.tax_id, tax_id_type = EXCLUDED.tax_id_type,
			tax_override_rate_bp = EXCLUDED.tax_override_rate_bp,
			profile_status = EXCLUDED.profile_status, updated_at = EXCLUDED.updated_at
		RETURNING customer_id, tenant_id, COALESCE(legal_name,''), COALESCE(email,''), COALESCE(phone,''),
			COALESCE(address_line1,''), COALESCE(address_line2,''), COALESCE(city,''), COALESCE(state,''),
			COALESCE(postal_code,''), COALESCE(country,''), COALESCE(currency,''),
			tax_exempt, COALESCE(tax_id,''), COALESCE(tax_id_type,''), tax_override_rate_bp,
			profile_status, created_at, updated_at
	`, bp.CustomerID, tenantID, postgres.NullableString(enc.LegalName), postgres.NullableString(enc.Email),
		postgres.NullableString(enc.Phone), postgres.NullableString(bp.AddressLine1),
		postgres.NullableString(bp.AddressLine2), postgres.NullableString(bp.City),
		postgres.NullableString(bp.State), postgres.NullableString(bp.PostalCode),
		postgres.NullableString(bp.Country), postgres.NullableString(bp.Currency),
		bp.TaxExempt, enc.TaxID, bp.TaxIDType, bp.TaxOverrideRateBP,
		bp.ProfileStatus, now,
	).Scan(
		&bp.CustomerID, &bp.TenantID, &bp.LegalName, &bp.Email, &bp.Phone,
		&bp.AddressLine1, &bp.AddressLine2, &bp.City, &bp.State, &bp.PostalCode,
		&bp.Country, &bp.Currency, &bp.TaxExempt,
		&bp.TaxID, &bp.TaxIDType, &overrideBP,
		&bp.ProfileStatus, &bp.CreatedAt, &bp.UpdatedAt,
	)
	if overrideBP.Valid {
		v := int(overrideBP.Int64)
		bp.TaxOverrideRateBP = &v
	}
	if err != nil {
		return domain.CustomerBillingProfile{}, err
	}

	bp, err = s.decryptBillingProfile(bp)
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
	var overrideBP sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT customer_id, tenant_id, COALESCE(legal_name,''), COALESCE(email,''), COALESCE(phone,''),
			COALESCE(address_line1,''), COALESCE(address_line2,''), COALESCE(city,''), COALESCE(state,''),
			COALESCE(postal_code,''), COALESCE(country,''), COALESCE(currency,''),
			tax_exempt, COALESCE(tax_id,''), COALESCE(tax_id_type,''), tax_override_rate_bp,
			profile_status, created_at, updated_at
		FROM customer_billing_profiles WHERE customer_id = $1
	`, customerID).Scan(
		&bp.CustomerID, &bp.TenantID, &bp.LegalName, &bp.Email, &bp.Phone,
		&bp.AddressLine1, &bp.AddressLine2, &bp.City, &bp.State, &bp.PostalCode,
		&bp.Country, &bp.Currency, &bp.TaxExempt,
		&bp.TaxID, &bp.TaxIDType, &overrideBP,
		&bp.ProfileStatus, &bp.CreatedAt, &bp.UpdatedAt,
	)
	if overrideBP.Valid {
		v := int(overrideBP.Int64)
		bp.TaxOverrideRateBP = &v
	}
	if err == sql.ErrNoRows {
		return domain.CustomerBillingProfile{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.CustomerBillingProfile{}, err
	}
	return s.decryptBillingProfile(bp)
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
			stripe_customer_id, stripe_payment_method_id,
			card_brand, card_last4, card_exp_month, card_exp_year,
			last_verified_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)
		ON CONFLICT (tenant_id, customer_id) DO UPDATE SET
			setup_status = EXCLUDED.setup_status,
			default_payment_method_present = EXCLUDED.default_payment_method_present,
			payment_method_type = EXCLUDED.payment_method_type,
			stripe_customer_id = EXCLUDED.stripe_customer_id,
			stripe_payment_method_id = EXCLUDED.stripe_payment_method_id,
			card_brand = EXCLUDED.card_brand,
			card_last4 = EXCLUDED.card_last4,
			card_exp_month = EXCLUDED.card_exp_month,
			card_exp_year = EXCLUDED.card_exp_year,
			last_verified_at = EXCLUDED.last_verified_at,
			updated_at = EXCLUDED.updated_at
		RETURNING customer_id, tenant_id, setup_status, default_payment_method_present,
			COALESCE(payment_method_type,''), COALESCE(stripe_customer_id,''),
			COALESCE(stripe_payment_method_id,''),
			COALESCE(card_brand,''), COALESCE(card_last4,''),
			COALESCE(card_exp_month, 0), COALESCE(card_exp_year, 0),
			last_verified_at, created_at, updated_at
	`, ps.CustomerID, tenantID, ps.SetupStatus, ps.DefaultPaymentMethodPresent,
		postgres.NullableString(ps.PaymentMethodType), postgres.NullableString(ps.StripeCustomerID),
		postgres.NullableString(ps.StripePaymentMethodID),
		postgres.NullableString(ps.CardBrand), postgres.NullableString(ps.CardLast4),
		ps.CardExpMonth, ps.CardExpYear,
		postgres.NullableTime(ps.LastVerifiedAt), now,
	).Scan(
		&ps.CustomerID, &ps.TenantID, &ps.SetupStatus, &ps.DefaultPaymentMethodPresent,
		&ps.PaymentMethodType, &ps.StripeCustomerID, &ps.StripePaymentMethodID,
		&ps.CardBrand, &ps.CardLast4, &ps.CardExpMonth, &ps.CardExpYear,
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
			COALESCE(stripe_payment_method_id,''),
			COALESCE(card_brand,''), COALESCE(card_last4,''),
			COALESCE(card_exp_month, 0), COALESCE(card_exp_year, 0),
			last_verified_at, created_at, updated_at
		FROM customer_payment_setups WHERE customer_id = $1
	`, customerID).Scan(
		&ps.CustomerID, &ps.TenantID, &ps.SetupStatus, &ps.DefaultPaymentMethodPresent,
		&ps.PaymentMethodType, &ps.StripeCustomerID, &ps.StripePaymentMethodID,
		&ps.CardBrand, &ps.CardLast4, &ps.CardExpMonth, &ps.CardExpYear,
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
