package tenant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// BootstrapHandler provides a one-time setup endpoint for creating
// the first tenant + API key. Only works when no tenants exist.
type BootstrapHandler struct {
	db *postgres.DB
}

func NewBootstrapHandler(db *postgres.DB) *BootstrapHandler {
	return &BootstrapHandler{db: db}
}

func (h *BootstrapHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.bootstrap)
	return r
}

type bootstrapRequest struct {
	TenantName string `json:"tenant_name"`
}

type bootstrapResponse struct {
	Tenant    domain.Tenant `json:"tenant"`
	SecretKey string        `json:"secret_key"`
	PublicKey string        `json:"public_key"`
	Message   string        `json:"message"`
}

func (h *BootstrapHandler) bootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Safety: only allow bootstrap when no tenants exist
	var count int
	err := h.db.Pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenants`).Scan(&count)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to check tenants")
		return
	}
	if count > 0 {
		writeErr(w, http.StatusConflict, "bootstrap already completed — tenants exist")
		return
	}

	var req bootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.TenantName) == "" {
		req.TenantName = "Default Tenant"
	}

	// Create tenant
	tenantID := postgres.NewID("vlx_ten")
	now := time.Now().UTC()
	_, err = h.db.Pool.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status, created_at, updated_at) VALUES ($1, $2, 'active', $3, $3)`,
		tenantID, req.TenantName, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create tenant")
		return
	}

	// Create secret key
	secretRaw, secretPrefix, secretHash := generateKey("vlx_secret_")

	// Create publishable key
	pubRaw, pubPrefix, pubHash := generateKey("vlx_pub_")

	// Insert keys (bypass RLS since we just created the tenant)
	tx, err := h.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create keys")
		return
	}
	defer postgres.Rollback(tx)

	secretKeyID := postgres.NewID("vlx_key")
	tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, name, tenant_id) VALUES ($1,$2,$3,'secret','Bootstrap Secret Key',$4)`,
		secretKeyID, secretPrefix, secretHash, tenantID)

	pubKeyID := postgres.NewID("vlx_key")
	tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, name, tenant_id) VALUES ($1,$2,$3,'publishable','Bootstrap Publishable Key',$4)`,
		pubKeyID, pubPrefix, pubHash, tenantID)

	if err := tx.Commit(); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to save keys")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(bootstrapResponse{
		Tenant:    domain.Tenant{ID: tenantID, Name: req.TenantName, Status: domain.TenantStatusActive, CreatedAt: now, UpdatedAt: now},
		SecretKey: secretRaw,
		PublicKey: pubRaw,
		Message:   "Bootstrap complete. Save these keys — the secret key will not be shown again.",
	})
}

func generateKey(prefix string) (raw, dbPrefix, hashHex string) {
	secret := make([]byte, 32)
	rand.Read(secret)
	secretHex := hex.EncodeToString(secret)
	raw = prefix + secretHex
	dbPrefix = prefix + secretHex[:12]
	hash := sha256.Sum256([]byte(raw))
	hashHex = hex.EncodeToString(hash[:])
	return
}

func writeErr(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"type": "api_error", "message": message},
	})
}
