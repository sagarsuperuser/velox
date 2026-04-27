package customer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db      *postgres.DB
	enc     *crypto.Encryptor
	blinder *crypto.Blinder
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

// SetBlinder configures the HMAC blinder used to populate customers.email_bidx.
// Required for the customer-initiated magic-link flow, optional otherwise —
// when unset, email_bidx is left NULL and FindByEmailBlindIndex returns no
// matches.
func (s *PostgresStore) SetBlinder(b *crypto.Blinder) {
	s.blinder = b
}

// emailBlindIndex normalises the email (trim + lowercase) and returns its
// blind-index representation. Empty string when the blinder isn't configured
// or the email is empty — both safe-by-default for INSERT/UPDATE.
func (s *PostgresStore) emailBlindIndex(email string) string {
	if s.blinder == nil {
		return ""
	}
	return s.blinder.Blind(strings.ToLower(strings.TrimSpace(email)))
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
		INSERT INTO customers (id, tenant_id, external_id, display_name, email, email_bidx, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), $7, $8, $8)
		RETURNING id, tenant_id, external_id, display_name, email, status, created_at, updated_at
	`, id, tenantID, c.ExternalID, enc.DisplayName, enc.Email,
		s.emailBlindIndex(c.Email), domain.CustomerStatusActive, now,
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
		SELECT id, tenant_id, external_id, display_name, COALESCE(email, ''), status,
			email_status, email_last_bounced_at, COALESCE(email_bounce_reason,''),
			COALESCE(cost_dashboard_token, ''),
			created_at, updated_at
		FROM customers WHERE id = $1
	`, id).Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status,
		(*string)(&c.EmailStatus), &c.EmailLastBouncedAt, &c.EmailBounceReason,
		&c.CostDashboardToken,
		&c.CreatedAt, &c.UpdatedAt)

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
		SELECT id, tenant_id, external_id, display_name, COALESCE(email, ''), status,
			email_status, email_last_bounced_at, COALESCE(email_bounce_reason,''),
			COALESCE(cost_dashboard_token, ''),
			created_at, updated_at
		FROM customers WHERE external_id = $1
	`, externalID).Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status,
		(*string)(&c.EmailStatus), &c.EmailLastBouncedAt, &c.EmailBounceReason,
		&c.CostDashboardToken,
		&c.CreatedAt, &c.UpdatedAt)

	if err == sql.ErrNoRows {
		return domain.Customer{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.Customer{}, err
	}
	return s.decryptCustomer(c)
}

// SetCostDashboardToken writes a freshly minted (or empty) cost-dashboard
// token for the given customer. Empty tokens clear the column — useful
// if an operator wants to revoke all public access without immediately
// minting a replacement. Non-empty writes hit the partial UNIQUE index
// from migration 0064; collisions surface as an error rather than a
// silent overwrite.
func (s *PostgresStore) SetCostDashboardToken(ctx context.Context, tenantID, customerID, token string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `
		UPDATE customers SET cost_dashboard_token = NULLIF($1, ''), updated_at = NOW()
		WHERE id = $2
	`, token, customerID)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return fmt.Errorf("set cost dashboard token: collision: %w", err)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
}

// GetByCostDashboardToken resolves a token to its owning customer
// cross-tenant. Runs under TxBypass because the caller is unauthenticated
// at this point — the 256-bit token itself is the only credential we
// have, and the partial UNIQUE index ensures at most one match. The
// blind-index pattern in FindByEmailBlindIndex is the closest precedent.
func (s *PostgresStore) GetByCostDashboardToken(ctx context.Context, token string) (domain.Customer, error) {
	if token == "" {
		return domain.Customer{}, errs.ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return domain.Customer{}, fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	var c domain.Customer
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, external_id, display_name, COALESCE(email, ''), status,
			email_status, email_last_bounced_at, COALESCE(email_bounce_reason,''),
			COALESCE(cost_dashboard_token, ''),
			created_at, updated_at
		FROM customers WHERE cost_dashboard_token = $1
	`, token).Scan(&c.ID, &c.TenantID, &c.ExternalID, &c.DisplayName, &c.Email, &c.Status,
		(*string)(&c.EmailStatus), &c.EmailLastBouncedAt, &c.EmailBounceReason,
		&c.CostDashboardToken,
		&c.CreatedAt, &c.UpdatedAt)

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
	return c, tx.Commit()
}

// EmailMatch is the narrow result row from FindByEmailBlindIndex — enough
// identity to mint a magic link but no PII. Returned cross-tenant, so the
// caller (the public magic-link handler) iterates one match per (tenant,
// customer) and never leaks details across boundaries.
type EmailMatch struct {
	TenantID   string
	CustomerID string
	Livemode   bool
	Status     string
}

// FindByEmailBlindIndex resolves the email → customer(s) lookup the public
// magic-link endpoint needs. Runs under TxBypass because the caller is
// unauthenticated until this returns — the blind index itself is the only
// handle we have, and an attacker who can't compute HMAC(key, email)
// can't enumerate. Callers must pre-compute the blind index via their own
// crypto.Blinder instance so this method stays a pure DB read.
//
// Returns at most `limit` matches (sane ceiling so a colliding index can
// never fan out into thousands of emails). Empty blind → no matches,
// regardless of DB state, so misconfigured blinders silently fail closed.
func (s *PostgresStore) FindByEmailBlindIndex(ctx context.Context, blind string, limit int) ([]EmailMatch, error) {
	if blind == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT tenant_id, id, livemode, status
		FROM customers
		WHERE email_bidx = $1 AND status = 'active'
		ORDER BY tenant_id, id
		LIMIT $2
	`, blind, limit)
	if err != nil {
		return nil, fmt.Errorf("select: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []EmailMatch
	for rows.Next() {
		var m EmailMatch
		if err := rows.Scan(&m.TenantID, &m.CustomerID, &m.Livemode, &m.Status); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
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
		UPDATE customers SET display_name = $1, email = $2, email_bidx = NULLIF($3,''), status = $4, updated_at = $5
		WHERE id = $6
		RETURNING id, tenant_id, external_id, display_name, email, status, created_at, updated_at
	`, enc.DisplayName, enc.Email, s.emailBlindIndex(c.Email), c.Status, now, c.ID,
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

// MarkEmailBounced flips email_status to 'bounced' and records the
// timestamp + free-text reason. Accepts customerID (not email) to avoid
// routing a raw email string through the store — the caller holds the
// blind-index lookup, which keeps encrypted email handling in one place.
// Idempotent: repeated calls refresh the timestamp and reason but are
// otherwise no-ops.
func (s *PostgresStore) MarkEmailBounced(ctx context.Context, tenantID, customerID, reason string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx, `
		UPDATE customers SET email_status = 'bounced',
			email_last_bounced_at = NOW(),
			email_bounce_reason = NULLIF($1, ''),
			updated_at = NOW()
		WHERE id = $2
	`, reason, customerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return tx.Commit()
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
	status := string(bp.TaxStatus)
	if status == "" {
		status = "standard"
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO customer_billing_profiles (customer_id, tenant_id, legal_name, email, phone,
			address_line1, address_line2, city, state, postal_code, country, currency,
			tax_status, tax_exempt_reason, tax_id, tax_id_type,
			profile_status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $18)
		ON CONFLICT (tenant_id, customer_id) DO UPDATE SET
			legal_name = EXCLUDED.legal_name, email = EXCLUDED.email, phone = EXCLUDED.phone,
			address_line1 = EXCLUDED.address_line1, address_line2 = EXCLUDED.address_line2,
			city = EXCLUDED.city, state = EXCLUDED.state, postal_code = EXCLUDED.postal_code,
			country = EXCLUDED.country, currency = EXCLUDED.currency,
			tax_status = EXCLUDED.tax_status,
			tax_exempt_reason = EXCLUDED.tax_exempt_reason,
			tax_id = EXCLUDED.tax_id, tax_id_type = EXCLUDED.tax_id_type,
			profile_status = EXCLUDED.profile_status, updated_at = EXCLUDED.updated_at
		RETURNING customer_id, tenant_id, COALESCE(legal_name,''), COALESCE(email,''), COALESCE(phone,''),
			COALESCE(address_line1,''), COALESCE(address_line2,''), COALESCE(city,''), COALESCE(state,''),
			COALESCE(postal_code,''), COALESCE(country,''), COALESCE(currency,''),
			tax_status, COALESCE(tax_exempt_reason,''),
			COALESCE(tax_id,''), COALESCE(tax_id_type,''),
			profile_status, created_at, updated_at
	`, bp.CustomerID, tenantID, postgres.NullableString(enc.LegalName), postgres.NullableString(enc.Email),
		postgres.NullableString(enc.Phone), postgres.NullableString(bp.AddressLine1),
		postgres.NullableString(bp.AddressLine2), postgres.NullableString(bp.City),
		postgres.NullableString(bp.State), postgres.NullableString(bp.PostalCode),
		postgres.NullableString(bp.Country), postgres.NullableString(bp.Currency),
		status, bp.TaxExemptReason, enc.TaxID, bp.TaxIDType,
		bp.ProfileStatus, now,
	).Scan(
		&bp.CustomerID, &bp.TenantID, &bp.LegalName, &bp.Email, &bp.Phone,
		&bp.AddressLine1, &bp.AddressLine2, &bp.City, &bp.State, &bp.PostalCode,
		&bp.Country, &bp.Currency, &bp.TaxStatus, &bp.TaxExemptReason,
		&bp.TaxID, &bp.TaxIDType,
		&bp.ProfileStatus, &bp.CreatedAt, &bp.UpdatedAt,
	)
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
	err = tx.QueryRowContext(ctx, `
		SELECT customer_id, tenant_id, COALESCE(legal_name,''), COALESCE(email,''), COALESCE(phone,''),
			COALESCE(address_line1,''), COALESCE(address_line2,''), COALESCE(city,''), COALESCE(state,''),
			COALESCE(postal_code,''), COALESCE(country,''), COALESCE(currency,''),
			tax_status, COALESCE(tax_exempt_reason,''),
			COALESCE(tax_id,''), COALESCE(tax_id_type,''),
			profile_status, created_at, updated_at
		FROM customer_billing_profiles WHERE customer_id = $1
	`, customerID).Scan(
		&bp.CustomerID, &bp.TenantID, &bp.LegalName, &bp.Email, &bp.Phone,
		&bp.AddressLine1, &bp.AddressLine2, &bp.City, &bp.State, &bp.PostalCode,
		&bp.Country, &bp.Currency, &bp.TaxStatus, &bp.TaxExemptReason,
		&bp.TaxID, &bp.TaxIDType,
		&bp.ProfileStatus, &bp.CreatedAt, &bp.UpdatedAt,
	)
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
