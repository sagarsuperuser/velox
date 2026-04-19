package billing

import (
	"context"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// postgresLocker adapts *postgres.DB to the billing.Locker interface so the
// scheduler can stay decoupled from the concrete DB package — tests inject a
// fake Locker, production wires this adapter via NewPostgresLocker.
type postgresLocker struct {
	db *postgres.DB
}

// NewPostgresLocker returns a Locker that acquires Postgres session-scoped
// advisory locks. Pass the result to Scheduler.SetLocker alongside the
// billing and dunning lock keys from the postgres package.
func NewPostgresLocker(db *postgres.DB) Locker {
	return &postgresLocker{db: db}
}

func (p *postgresLocker) TryAdvisoryLock(ctx context.Context, key int64) (Lock, bool, error) {
	lock, ok, err := p.db.TryAdvisoryLock(ctx, key)
	if err != nil || !ok {
		return nil, ok, err
	}
	return lock, true, nil
}
