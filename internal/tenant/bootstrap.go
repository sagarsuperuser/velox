package tenant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/api/respond"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// BootstrapHandler provides a one-time setup endpoint for creating
// the first tenant + API keys. Protected by VELOX_BOOTSTRAP_TOKEN env var.
// Only works when no tenants exist (race-safe via INSERT ... ON CONFLICT).
type BootstrapHandler struct {
	db    *postgres.DB
	token string // Required token from VELOX_BOOTSTRAP_TOKEN env var; empty = disabled
}

func NewBootstrapHandler(db *postgres.DB) *BootstrapHandler {
	return &BootstrapHandler{
		db:    db,
		token: strings.TrimSpace(os.Getenv("VELOX_BOOTSTRAP_TOKEN")),
	}
}

func (h *BootstrapHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.bootstrap)
	return r
}

type bootstrapRequest struct {
	TenantName string `json:"tenant_name"`
	Token      string `json:"token"`
}

type bootstrapResponse struct {
	Tenant    domain.Tenant `json:"tenant"`
	SecretKey string        `json:"secret_key"`
	PublicKey string        `json:"public_key"`
	Message   string        `json:"message"`
}

func (h *BootstrapHandler) bootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req bootstrapRequest
	json.NewDecoder(r.Body).Decode(&req)

	// Verify bootstrap token if configured
	if h.token != "" {
		provided := strings.TrimSpace(req.Token)
		if provided == "" {
			provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if provided != h.token {
			respond.Error(w, r, http.StatusForbidden, "authentication_error", "forbidden",
				"invalid bootstrap token — set VELOX_BOOTSTRAP_TOKEN env var and pass it as token field or Authorization header")
			return
		}
	}

	if strings.TrimSpace(req.TenantName) == "" {
		req.TenantName = "Default Tenant"
	}

	// Race-safe tenant creation: INSERT ... ON CONFLICT DO NOTHING + check affected rows.
	// Two simultaneous requests will not both succeed.
	tenantID := postgres.NewID("vlx_ten")
	now := time.Now().UTC()

	result, err := h.db.Pool.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status, created_at, updated_at)
		SELECT $1, $2, 'active', $3, $3
		WHERE NOT EXISTS (SELECT 1 FROM tenants LIMIT 1)`,
		tenantID, req.TenantName, now)
	if err != nil {
		respond.InternalError(w, r)
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		respond.Error(w, r, http.StatusConflict, "invalid_request_error", "already_bootstrapped",
			"bootstrap already completed — tenants exist")
		return
	}

	// Create keys
	secretRaw, secretPrefix, secretHash := generateKey("vlx_secret_")
	pubRaw, pubPrefix, pubHash := generateKey("vlx_pub_")

	tx, err := h.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		respond.InternalError(w, r)
		return
	}
	defer postgres.Rollback(tx)

	secretKeyID := postgres.NewID("vlx_key")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, name, tenant_id) VALUES ($1,$2,$3,'secret','Bootstrap Secret Key',$4)`,
		secretKeyID, secretPrefix, secretHash, tenantID); err != nil {
		respond.InternalError(w, r)
		return
	}

	pubKeyID := postgres.NewID("vlx_key")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, key_prefix, key_hash, key_type, name, tenant_id) VALUES ($1,$2,$3,'publishable','Bootstrap Publishable Key',$4)`,
		pubKeyID, pubPrefix, pubHash, tenantID); err != nil {
		respond.InternalError(w, r)
		return
	}

	if err := tx.Commit(); err != nil {
		respond.InternalError(w, r)
		return
	}

	respond.JSON(w, r, http.StatusCreated, bootstrapResponse{
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
