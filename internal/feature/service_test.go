package feature

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// memStore is an in-memory implementation of Store for testing.
type memStore struct {
	flags     map[string]Flag
	overrides map[string]FlagOverride // key = "flagKey:tenantID"
	calls     int                     // tracks number of store calls for cache testing
}

func newMemStore() *memStore {
	return &memStore{
		flags:     make(map[string]Flag),
		overrides: make(map[string]FlagOverride),
	}
}

func (m *memStore) GetFlag(_ context.Context, key string) (Flag, error) {
	m.calls++
	f, ok := m.flags[key]
	if !ok {
		return Flag{}, fmt.Errorf("feature flag %q not found", key)
	}
	return f, nil
}

func (m *memStore) GetOverride(_ context.Context, key, tenantID string) (FlagOverride, bool, error) {
	m.calls++
	o, ok := m.overrides[key+":"+tenantID]
	if !ok {
		return FlagOverride{}, false, nil
	}
	return o, true, nil
}

func (m *memStore) SetGlobal(_ context.Context, key string, enabled bool) error {
	m.calls++
	f, ok := m.flags[key]
	if !ok {
		return fmt.Errorf("feature flag %q not found", key)
	}
	f.Enabled = enabled
	f.UpdatedAt = time.Now()
	m.flags[key] = f
	return nil
}

func (m *memStore) SetOverride(_ context.Context, tenantID, key string, enabled bool) error {
	m.calls++
	m.overrides[key+":"+tenantID] = FlagOverride{
		FlagKey:   key,
		TenantID:  tenantID,
		Enabled:   enabled,
		CreatedAt: time.Now(),
	}
	return nil
}

func (m *memStore) RemoveOverride(_ context.Context, tenantID, key string) error {
	m.calls++
	delete(m.overrides, key+":"+tenantID)
	return nil
}

func (m *memStore) List(_ context.Context) ([]Flag, error) {
	m.calls++
	var result []Flag
	for _, f := range m.flags {
		result = append(result, f)
	}
	return result, nil
}

func (m *memStore) ListOverrides(_ context.Context, tenantID string) ([]FlagOverride, error) {
	m.calls++
	var result []FlagOverride
	for _, o := range m.overrides {
		if o.TenantID == tenantID {
			result = append(result, o)
		}
	}
	return result, nil
}

// seedFlag adds a flag to the in-memory store for testing.
func (m *memStore) seedFlag(key string, enabled bool) {
	m.flags[key] = Flag{
		Key:         key,
		Enabled:     enabled,
		Description: "test flag",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func TestIsEnabled_GlobalEnabled(t *testing.T) {
	store := newMemStore()
	store.seedFlag("billing.auto_charge", true)
	svc := NewService(store)

	got := svc.IsEnabled(context.Background(), "billing.auto_charge", "tenant_1")
	if !got {
		t.Error("expected true for globally enabled flag, got false")
	}
}

func TestIsEnabled_GlobalDisabled(t *testing.T) {
	store := newMemStore()
	store.seedFlag("billing.auto_charge", false)
	svc := NewService(store)

	got := svc.IsEnabled(context.Background(), "billing.auto_charge", "tenant_1")
	if got {
		t.Error("expected false for globally disabled flag, got true")
	}
}

func TestIsEnabled_TenantOverrideEnablesDisabledGlobal(t *testing.T) {
	store := newMemStore()
	store.seedFlag("dunning.enabled", false) // globally disabled
	svc := NewService(store)

	// Set tenant override to enabled
	if err := svc.SetOverride(context.Background(), "tenant_1", "dunning.enabled", true); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}

	got := svc.IsEnabled(context.Background(), "dunning.enabled", "tenant_1")
	if !got {
		t.Error("expected true: tenant override should enable a globally disabled flag")
	}
}

func TestIsEnabled_TenantOverrideDisablesEnabledGlobal(t *testing.T) {
	store := newMemStore()
	store.seedFlag("webhooks.enabled", true) // globally enabled
	svc := NewService(store)

	// Set tenant override to disabled
	if err := svc.SetOverride(context.Background(), "tenant_1", "webhooks.enabled", false); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}

	got := svc.IsEnabled(context.Background(), "webhooks.enabled", "tenant_1")
	if got {
		t.Error("expected false: tenant override should disable a globally enabled flag")
	}
}

func TestIsEnabled_UnknownFlag(t *testing.T) {
	store := newMemStore()
	svc := NewService(store)

	got := svc.IsEnabled(context.Background(), "nonexistent.flag", "tenant_1")
	if got {
		t.Error("expected false for unknown flag (fail-closed), got true")
	}
}

func TestIsEnabled_CacheHit(t *testing.T) {
	store := newMemStore()
	store.seedFlag("billing.auto_charge", true)
	svc := NewService(store)

	// First call — cache miss, hits the store
	svc.IsEnabled(context.Background(), "billing.auto_charge", "tenant_1")
	callsAfterFirst := store.calls

	// Second call — should hit cache, no additional store calls
	got := svc.IsEnabled(context.Background(), "billing.auto_charge", "tenant_1")
	if !got {
		t.Error("expected true on cached call")
	}
	if store.calls != callsAfterFirst {
		t.Errorf("expected cache hit (no additional store calls), but store.calls went from %d to %d",
			callsAfterFirst, store.calls)
	}
}

func TestSetGlobal(t *testing.T) {
	store := newMemStore()
	store.seedFlag("billing.auto_charge", false)
	svc := NewService(store)

	if err := svc.SetGlobal(context.Background(), "billing.auto_charge", true); err != nil {
		t.Fatalf("SetGlobal: %v", err)
	}

	// Should reflect the new value (cache was invalidated)
	got := svc.IsEnabled(context.Background(), "billing.auto_charge", "tenant_1")
	if !got {
		t.Error("expected true after SetGlobal(true)")
	}
}

func TestSetGlobal_NotFound(t *testing.T) {
	store := newMemStore()
	svc := NewService(store)

	err := svc.SetGlobal(context.Background(), "nonexistent", true)
	if err == nil {
		t.Error("expected error for non-existent flag")
	}
}

func TestRemoveOverride(t *testing.T) {
	store := newMemStore()
	store.seedFlag("dunning.enabled", false) // globally disabled
	svc := NewService(store)

	// Set override, then remove it
	_ = svc.SetOverride(context.Background(), "tenant_1", "dunning.enabled", true)
	if err := svc.RemoveOverride(context.Background(), "tenant_1", "dunning.enabled"); err != nil {
		t.Fatalf("RemoveOverride: %v", err)
	}

	// Should fall back to global (disabled)
	got := svc.IsEnabled(context.Background(), "dunning.enabled", "tenant_1")
	if got {
		t.Error("expected false after removing override (global is disabled)")
	}
}

func TestList(t *testing.T) {
	store := newMemStore()
	store.seedFlag("a.flag", true)
	store.seedFlag("b.flag", false)
	svc := NewService(store)

	flags, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(flags) != 2 {
		t.Errorf("expected 2 flags, got %d", len(flags))
	}
}

func TestListOverrides(t *testing.T) {
	store := newMemStore()
	store.seedFlag("a.flag", true)
	store.seedFlag("b.flag", false)
	svc := NewService(store)

	_ = svc.SetOverride(context.Background(), "t1", "a.flag", false)
	_ = svc.SetOverride(context.Background(), "t1", "b.flag", true)
	_ = svc.SetOverride(context.Background(), "t2", "a.flag", true) // different tenant

	overrides, err := svc.ListOverrides(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListOverrides: %v", err)
	}
	if len(overrides) != 2 {
		t.Errorf("expected 2 overrides for t1, got %d", len(overrides))
	}
}
