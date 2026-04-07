package tenant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// SettingsStore handles tenant settings CRUD.
type SettingsStore struct {
	db *postgres.DB
}

func NewSettingsStore(db *postgres.DB) *SettingsStore {
	return &SettingsStore{db: db}
}

func (s *SettingsStore) Get(ctx context.Context, tenantID string) (domain.TenantSettings, error) {
	var ts domain.TenantSettings
	err := s.db.Pool.QueryRowContext(ctx, `
		SELECT tenant_id, default_currency, timezone, invoice_prefix, invoice_next_seq,
			net_payment_terms, COALESCE(company_name,''), COALESCE(company_address,''),
			COALESCE(company_email,''), COALESCE(company_phone,''), COALESCE(logo_url,''),
			created_at, updated_at
		FROM tenant_settings WHERE tenant_id = $1
	`, tenantID).Scan(&ts.TenantID, &ts.DefaultCurrency, &ts.Timezone, &ts.InvoicePrefix,
		&ts.InvoiceNextSeq, &ts.NetPaymentTerms, &ts.CompanyName, &ts.CompanyAddress,
		&ts.CompanyEmail, &ts.CompanyPhone, &ts.LogoURL, &ts.CreatedAt, &ts.UpdatedAt)

	if err == sql.ErrNoRows {
		return domain.TenantSettings{}, errs.ErrNotFound
	}
	return ts, err
}

func (s *SettingsStore) Upsert(ctx context.Context, ts domain.TenantSettings) (domain.TenantSettings, error) {
	now := time.Now().UTC()
	err := s.db.Pool.QueryRowContext(ctx, `
		INSERT INTO tenant_settings (tenant_id, default_currency, timezone, invoice_prefix,
			net_payment_terms, company_name, company_address, company_email, company_phone,
			logo_url, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		ON CONFLICT (tenant_id) DO UPDATE SET
			default_currency = EXCLUDED.default_currency, timezone = EXCLUDED.timezone,
			invoice_prefix = EXCLUDED.invoice_prefix, net_payment_terms = EXCLUDED.net_payment_terms,
			company_name = EXCLUDED.company_name, company_address = EXCLUDED.company_address,
			company_email = EXCLUDED.company_email, company_phone = EXCLUDED.company_phone,
			logo_url = EXCLUDED.logo_url, updated_at = EXCLUDED.updated_at
		RETURNING tenant_id, default_currency, timezone, invoice_prefix, invoice_next_seq,
			net_payment_terms, COALESCE(company_name,''), COALESCE(company_address,''),
			COALESCE(company_email,''), COALESCE(company_phone,''), COALESCE(logo_url,''),
			created_at, updated_at
	`, ts.TenantID, ts.DefaultCurrency, ts.Timezone, ts.InvoicePrefix,
		ts.NetPaymentTerms, postgres.NullableString(ts.CompanyName),
		postgres.NullableString(ts.CompanyAddress), postgres.NullableString(ts.CompanyEmail),
		postgres.NullableString(ts.CompanyPhone), postgres.NullableString(ts.LogoURL), now,
	).Scan(&ts.TenantID, &ts.DefaultCurrency, &ts.Timezone, &ts.InvoicePrefix,
		&ts.InvoiceNextSeq, &ts.NetPaymentTerms, &ts.CompanyName, &ts.CompanyAddress,
		&ts.CompanyEmail, &ts.CompanyPhone, &ts.LogoURL, &ts.CreatedAt, &ts.UpdatedAt)

	return ts, err
}

// NextInvoiceNumber atomically increments the sequence and returns the next invoice number.
func (s *SettingsStore) NextInvoiceNumber(ctx context.Context, tenantID string) (string, error) {
	var prefix string
	var seq int
	err := s.db.Pool.QueryRowContext(ctx, `
		UPDATE tenant_settings SET invoice_next_seq = invoice_next_seq + 1
		WHERE tenant_id = $1
		RETURNING invoice_prefix, invoice_next_seq - 1
	`, tenantID).Scan(&prefix, &seq)
	if err != nil {
		// No settings → use default
		return "", err
	}
	return strings.ToUpper(prefix) + "-" + padSeq(seq), nil
}

func padSeq(n int) string {
	return padInt(n, 6)
}

func padInt(n, width int) string {
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// SettingsHandler handles HTTP for tenant settings.
type SettingsHandler struct {
	store *SettingsStore
}

func NewSettingsHandler(store *SettingsStore) *SettingsHandler {
	return &SettingsHandler{store: store}
}

func (h *SettingsHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.get)
	r.Put("/", h.upsert)
	return r
}

func (h *SettingsHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	ts, err := h.store.Get(r.Context(), tenantID)
	if errors.Is(err, errs.ErrNotFound) {
		// Return defaults
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(domain.TenantSettings{
			TenantID:        tenantID,
			DefaultCurrency: "USD",
			Timezone:        "UTC",
			InvoicePrefix:   "VLX",
			NetPaymentTerms: 30,
		})
		return
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal_error"})
		slog.Error("get settings", "error", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(ts)
}

func (h *SettingsHandler) upsert(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantID(r.Context())

	var ts domain.TenantSettings
	if err := json.NewDecoder(r.Body).Decode(&ts); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
		return
	}
	ts.TenantID = tenantID

	if ts.DefaultCurrency == "" {
		ts.DefaultCurrency = "USD"
	}
	if ts.Timezone == "" {
		ts.Timezone = "UTC"
	}
	if ts.InvoicePrefix == "" {
		ts.InvoicePrefix = "VLX"
	}
	if ts.NetPaymentTerms <= 0 {
		ts.NetPaymentTerms = 30
	}

	result, err := h.store.Upsert(r.Context(), ts)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal_error"})
		slog.Error("upsert settings", "error", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(result)
}
