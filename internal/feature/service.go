package feature

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Flag represents a global feature flag.
type Flag struct {
	Key         string    `json:"key"`
	Enabled     bool      `json:"enabled"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// FlagOverride represents a per-tenant override for a feature flag.
type FlagOverride struct {
	FlagKey   string    `json:"flag_key"`
	TenantID  string    `json:"tenant_id"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// Store defines the data access interface for feature flags.
// Implementations must be safe for concurrent use.
type Store interface {
	GetFlag(ctx context.Context, key string) (Flag, error)
	GetOverride(ctx context.Context, key, tenantID string) (FlagOverride, bool, error)
	SetGlobal(ctx context.Context, key string, enabled bool) error
	SetOverride(ctx context.Context, tenantID, key string, enabled bool) error
	RemoveOverride(ctx context.Context, tenantID, key string) error
	List(ctx context.Context) ([]Flag, error)
	ListOverrides(ctx context.Context, tenantID string) ([]FlagOverride, error)
}

// cacheEntry holds a cached flag evaluation result with an expiry time.
type cacheEntry struct {
	enabled   bool
	expiresAt time.Time
}

// Service provides feature flag evaluation with an in-memory cache.
type Service struct {
	store Store

	mu    sync.RWMutex
	cache map[string]cacheEntry
	ttl   time.Duration
}

// NewService creates a feature flag service backed by the given store.
func NewService(store Store) *Service {
	return &Service{
		store: store,
		cache: make(map[string]cacheEntry),
		ttl:   30 * time.Second,
	}
}

// cacheKey builds a deterministic cache key from flag key and tenant ID.
func cacheKey(flagKey, tenantID string) string {
	return flagKey + ":" + tenantID
}

// IsEnabled checks whether a feature flag is enabled for the given tenant.
// It checks the tenant override first, then falls back to the global flag.
// Returns false on any error (fail-closed).
func (s *Service) IsEnabled(ctx context.Context, flagKey, tenantID string) bool {
	ck := cacheKey(flagKey, tenantID)

	// Check cache first
	s.mu.RLock()
	if entry, ok := s.cache[ck]; ok && time.Now().Before(entry.expiresAt) {
		s.mu.RUnlock()
		return entry.enabled
	}
	s.mu.RUnlock()

	// Cache miss — query store
	enabled := s.resolve(ctx, flagKey, tenantID)

	// Store in cache
	s.mu.Lock()
	s.cache[ck] = cacheEntry{enabled: enabled, expiresAt: time.Now().Add(s.ttl)}
	s.mu.Unlock()

	return enabled
}

// resolve evaluates a flag by checking tenant override first, then global.
func (s *Service) resolve(ctx context.Context, flagKey, tenantID string) bool {
	// Check tenant override first
	if tenantID != "" {
		override, found, err := s.store.GetOverride(ctx, flagKey, tenantID)
		if err != nil {
			slog.Error("feature flag: get override", "key", flagKey, "tenant", tenantID, "error", err)
			return false
		}
		if found {
			return override.Enabled
		}
	}

	// Fall back to global flag
	flag, err := s.store.GetFlag(ctx, flagKey)
	if err != nil {
		slog.Error("feature flag: get global", "key", flagKey, "error", err)
		return false
	}
	return flag.Enabled
}

// SetGlobal updates the global enabled state of a feature flag.
func (s *Service) SetGlobal(ctx context.Context, flagKey string, enabled bool) error {
	if err := s.store.SetGlobal(ctx, flagKey, enabled); err != nil {
		return err
	}
	s.invalidate(flagKey)
	return nil
}

// SetOverride sets a per-tenant override for a feature flag.
func (s *Service) SetOverride(ctx context.Context, tenantID, flagKey string, enabled bool) error {
	if err := s.store.SetOverride(ctx, tenantID, flagKey, enabled); err != nil {
		return err
	}
	s.invalidateKey(cacheKey(flagKey, tenantID))
	return nil
}

// RemoveOverride removes a per-tenant override, reverting to the global value.
func (s *Service) RemoveOverride(ctx context.Context, tenantID, flagKey string) error {
	if err := s.store.RemoveOverride(ctx, tenantID, flagKey); err != nil {
		return err
	}
	s.invalidateKey(cacheKey(flagKey, tenantID))
	return nil
}

// List returns all global feature flags.
func (s *Service) List(ctx context.Context) ([]Flag, error) {
	return s.store.List(ctx)
}

// ListOverrides returns all per-tenant overrides for the given tenant.
func (s *Service) ListOverrides(ctx context.Context, tenantID string) ([]FlagOverride, error) {
	return s.store.ListOverrides(ctx, tenantID)
}

// invalidate removes all cache entries whose key starts with the given flag key.
func (s *Service) invalidate(flagKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := flagKey + ":"
	for k := range s.cache {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(s.cache, k)
		}
	}
}

// invalidateKey removes a single cache entry.
func (s *Service) invalidateKey(key string) {
	s.mu.Lock()
	delete(s.cache, key)
	s.mu.Unlock()
}

// --- PostgreSQL store implementation ---

// PostgresStore implements Store using PostgreSQL.
type PostgresStore struct {
	db *postgres.DB
}

// NewPostgresStore creates a new PostgreSQL-backed feature flag store.
func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) GetFlag(ctx context.Context, key string) (Flag, error) {
	var f Flag
	err := s.db.Pool.QueryRowContext(ctx, `
		SELECT key, enabled, description, created_at, updated_at
		FROM feature_flags WHERE key = $1
	`, key).Scan(&f.Key, &f.Enabled, &f.Description, &f.CreatedAt, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		return Flag{}, fmt.Errorf("feature flag %q not found", key)
	}
	return f, err
}

func (s *PostgresStore) GetOverride(ctx context.Context, key, tenantID string) (FlagOverride, bool, error) {
	var o FlagOverride
	err := s.db.Pool.QueryRowContext(ctx, `
		SELECT flag_key, tenant_id, enabled, created_at
		FROM feature_flag_overrides WHERE flag_key = $1 AND tenant_id = $2
	`, key, tenantID).Scan(&o.FlagKey, &o.TenantID, &o.Enabled, &o.CreatedAt)
	if err == sql.ErrNoRows {
		return FlagOverride{}, false, nil
	}
	if err != nil {
		return FlagOverride{}, false, err
	}
	return o, true, nil
}

func (s *PostgresStore) SetGlobal(ctx context.Context, key string, enabled bool) error {
	res, err := s.db.Pool.ExecContext(ctx, `
		UPDATE feature_flags SET enabled = $1, updated_at = NOW() WHERE key = $2
	`, enabled, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("feature flag %q not found", key)
	}
	return nil
}

func (s *PostgresStore) SetOverride(ctx context.Context, tenantID, key string, enabled bool) error {
	_, err := s.db.Pool.ExecContext(ctx, `
		INSERT INTO feature_flag_overrides (flag_key, tenant_id, enabled)
		VALUES ($1, $2, $3)
		ON CONFLICT (flag_key, tenant_id) DO UPDATE SET enabled = $3
	`, key, tenantID, enabled)
	return err
}

func (s *PostgresStore) RemoveOverride(ctx context.Context, tenantID, key string) error {
	_, err := s.db.Pool.ExecContext(ctx, `
		DELETE FROM feature_flag_overrides WHERE flag_key = $1 AND tenant_id = $2
	`, key, tenantID)
	return err
}

func (s *PostgresStore) List(ctx context.Context) ([]Flag, error) {
	rows, err := s.db.Pool.QueryContext(ctx, `
		SELECT key, enabled, description, created_at, updated_at
		FROM feature_flags ORDER BY key LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var flags []Flag
	for rows.Next() {
		var f Flag
		if err := rows.Scan(&f.Key, &f.Enabled, &f.Description, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		flags = append(flags, f)
	}
	return flags, rows.Err()
}

func (s *PostgresStore) ListOverrides(ctx context.Context, tenantID string) ([]FlagOverride, error) {
	rows, err := s.db.Pool.QueryContext(ctx, `
		SELECT flag_key, tenant_id, enabled, created_at
		FROM feature_flag_overrides WHERE tenant_id = $1 ORDER BY flag_key LIMIT 500
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var overrides []FlagOverride
	for rows.Next() {
		var o FlagOverride
		if err := rows.Scan(&o.FlagKey, &o.TenantID, &o.Enabled, &o.CreatedAt); err != nil {
			return nil, err
		}
		overrides = append(overrides, o)
	}
	return overrides, rows.Err()
}
