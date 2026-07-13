package tenant

import (
	"context"
	"database/sql"
	"strings"

	"github.com/sagarsuperuser/velox/internal/audit"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// AuditEmitter is the narrow in-tx audit seam (ADR-090).
type AuditEmitter interface {
	LogInTx(ctx context.Context, tx *sql.Tx, e audit.Entry) error
}

type Service struct {
	store       Store
	auditLogger AuditEmitter
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// SetAuditLogger wires in-tx audit emission for tenant provisioning
// (ADR-090 / design-panel Q6): platform tenant creation previously left NO
// audit trail — the /v1/tenants route group mounts no catch-all middleware
// and nothing wrote a row. The row lands in the NEW tenant's own log with
// the platform credential as actor.
func (s *Service) SetAuditLogger(a AuditEmitter) { s.auditLogger = a }

type CreateInput struct {
	Name string `json:"name"`
}

func (s *Service) Create(ctx context.Context, input CreateInput) (domain.Tenant, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Tenant{}, errs.Required("name")
	}

	// Tenant provisioning is an account-plane fact: record it in the live
	// log explicitly (TxTenant requires an explicit livemode, and platform
	// routes carry none — a tenant create is not a test-mode action).
	ctx = postgres.WithLivemode(ctx, true)

	var emit func(tx *sql.Tx, out domain.Tenant) error
	if s.auditLogger != nil {
		emit = func(tx *sql.Tx, out domain.Tenant) error {
			return s.auditLogger.LogInTx(ctx, tx, audit.Entry{
				Action:        domain.AuditActionCreate,
				ResourceType:  "tenant",
				ResourceID:    out.ID,
				ResourceLabel: out.Name,
				Metadata:      map[string]any{"name": out.Name},
			})
		}
	}
	return s.store.CreateAudited(ctx, domain.Tenant{Name: name}, emit)
}

func (s *Service) Get(ctx context.Context, id string) (domain.Tenant, error) {
	if id == "" {
		return domain.Tenant{}, errs.Required("id")
	}
	return s.store.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Tenant, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) UpdateStatus(ctx context.Context, id string, status domain.TenantStatus) (domain.Tenant, error) {
	if id == "" {
		return domain.Tenant{}, errs.Required("id")
	}
	return s.store.UpdateStatus(ctx, id, status)
}
