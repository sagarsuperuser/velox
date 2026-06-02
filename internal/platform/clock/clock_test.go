package clock

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRealClock_FallbackAndBinding covers the two branches of Real()
// — wall-clock when ctx has no binding, and the bound value when it
// does. Locks in the contract that Real() respects ctx-bound
// effective-now as a first-class case.
func TestRealClock_FallbackAndBinding(t *testing.T) {
	c := Real()

	t.Run("falls back to wall clock without binding", func(t *testing.T) {
		before := time.Now().UTC()
		got := c.Now(context.Background())
		after := time.Now().UTC()
		if got.Before(before) || got.After(after) {
			t.Errorf("wall-clock fallback: got %v, want between %v and %v", got, before, after)
		}
	})

	t.Run("bound effective-now wins over wall clock", func(t *testing.T) {
		want := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
		ctx := WithEffectiveNow(context.Background(), want)
		got := c.Now(ctx)
		if !got.Equal(want) {
			t.Errorf("bound: got %v, want %v", got, want)
		}
	})
}

// TestFakeClock_FallbackAndBinding mirrors the real-clock test for
// Fake. Tests get the same ctx-binding semantics so they can pin
// effective-now without losing Fake's per-test deterministic value.
func TestFakeClock_FallbackAndBinding(t *testing.T) {
	fixed := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	c := NewFake(fixed)

	t.Run("returns fixed time without binding", func(t *testing.T) {
		got := c.Now(context.Background())
		if !got.Equal(fixed) {
			t.Errorf("fake fixed: got %v, want %v", got, fixed)
		}
	})

	t.Run("bound effective-now wins over fixed time", func(t *testing.T) {
		want := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
		ctx := WithEffectiveNow(context.Background(), want)
		got := c.Now(ctx)
		if !got.Equal(want) {
			t.Errorf("fake with binding: got %v, want %v", got, want)
		}
	})

	t.Run("Set + Advance still work", func(t *testing.T) {
		other := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		c.Set(other)
		if got := c.Now(context.Background()); !got.Equal(other) {
			t.Errorf("after Set: got %v, want %v", got, other)
		}
		c.Advance(time.Hour)
		if got := c.Now(context.Background()); !got.Equal(other.Add(time.Hour)) {
			t.Errorf("after Advance(1h): got %v, want %v", got, other.Add(time.Hour))
		}
	})
}

// TestPackageNow covers the clock.Now(ctx) shortcut used by postgres
// stores and render-layer code that doesn't own a Clock field.
func TestPackageNow(t *testing.T) {
	t.Run("falls back to wall clock", func(t *testing.T) {
		before := time.Now().UTC()
		got := Now(context.Background())
		after := time.Now().UTC()
		if got.Before(before) || got.After(after) {
			t.Errorf("wall-clock: got %v, want between %v and %v", got, before, after)
		}
	})

	t.Run("returns bound effective-now", func(t *testing.T) {
		want := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
		ctx := WithEffectiveNow(context.Background(), want)
		got := Now(ctx)
		if !got.Equal(want) {
			t.Errorf("bound: got %v, want %v", got, want)
		}
	})
}

// TestEffectiveNow covers the lookup helper directly.
func TestEffectiveNow(t *testing.T) {
	t.Run("nil ctx returns zero, false", func(t *testing.T) {
		// EffectiveNow explicitly handles nil for defensive callers
		// (postgres-store callsites where ctx might be missing during
		// migration). The lint complaint about nil ctx doesn't apply.
		//nolint:staticcheck // SA1012: intentional nil-ctx test
		got, ok := EffectiveNow(nil)
		if ok {
			t.Error("expected ok=false for nil ctx")
		}
		if !got.IsZero() {
			t.Errorf("expected zero time, got %v", got)
		}
	})

	t.Run("unbound ctx returns zero, false", func(t *testing.T) {
		got, ok := EffectiveNow(context.Background())
		if ok {
			t.Error("expected ok=false for unbound ctx")
		}
		if !got.IsZero() {
			t.Errorf("expected zero time, got %v", got)
		}
	})

	t.Run("bound ctx returns the value", func(t *testing.T) {
		want := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
		ctx := WithEffectiveNow(context.Background(), want)
		got, ok := EffectiveNow(ctx)
		if !ok {
			t.Error("expected ok=true for bound ctx")
		}
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// TestBindEffectiveNow covers the resolver-driven binding helper used
// at every operator entry point. Verifies the precedence (invoice →
// sub → customer), the resolver-error fallback, and the no-resolver
// shortcut.
func TestBindEffectiveNow(t *testing.T) {
	frozen := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)

	t.Run("nil resolver returns ctx unchanged", func(t *testing.T) {
		base := context.Background()
		ctx, ok := BindEffectiveNow(base, nil, Pin{TenantID: "t1", CustomerID: "cus_1"})
		if ok {
			t.Error("expected ok=false")
		}
		if _, bound := EffectiveNow(ctx); bound {
			t.Error("expected ctx to remain unbound")
		}
	})

	t.Run("invoice id wins over sub and customer", func(t *testing.T) {
		stub := &stubResolver{
			invoiceFunc: func() (time.Time, error) { return frozen, nil },
			subFunc:     func() (time.Time, error) { return time.Time{}, errors.New("should not be called") },
			custFunc:    func() (time.Time, error) { return time.Time{}, errors.New("should not be called") },
		}
		ctx, ok := BindEffectiveNow(context.Background(), stub, Pin{
			TenantID: "t1", InvoiceID: "inv_1", SubscriptionID: "sub_1", CustomerID: "cus_1",
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		got, _ := EffectiveNow(ctx)
		if !got.Equal(frozen) {
			t.Errorf("got %v, want %v", got, frozen)
		}
	})

	t.Run("sub id wins over customer when no invoice", func(t *testing.T) {
		stub := &stubResolver{
			subFunc:  func() (time.Time, error) { return frozen, nil },
			custFunc: func() (time.Time, error) { return time.Time{}, errors.New("should not be called") },
		}
		ctx, ok := BindEffectiveNow(context.Background(), stub, Pin{
			TenantID: "t1", SubscriptionID: "sub_1", CustomerID: "cus_1",
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		got, _ := EffectiveNow(ctx)
		if !got.Equal(frozen) {
			t.Errorf("got %v, want %v", got, frozen)
		}
	})

	t.Run("customer id is the final fallback", func(t *testing.T) {
		stub := &stubResolver{
			custFunc: func() (time.Time, error) { return frozen, nil },
		}
		ctx, ok := BindEffectiveNow(context.Background(), stub, Pin{
			TenantID: "t1", CustomerID: "cus_1",
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		got, _ := EffectiveNow(ctx)
		if !got.Equal(frozen) {
			t.Errorf("got %v, want %v", got, frozen)
		}
	})

	t.Run("resolver error returns ctx unchanged", func(t *testing.T) {
		stub := &stubResolver{
			custFunc: func() (time.Time, error) { return time.Time{}, errors.New("dangling pin") },
		}
		ctx, ok := BindEffectiveNow(context.Background(), stub, Pin{
			TenantID: "t1", CustomerID: "cus_missing",
		})
		if ok {
			t.Error("expected ok=false on resolver error")
		}
		if _, bound := EffectiveNow(ctx); bound {
			t.Error("expected ctx to remain unbound on resolver error")
		}
	})

	t.Run("no ids returns ctx unchanged", func(t *testing.T) {
		stub := &stubResolver{}
		ctx, ok := BindEffectiveNow(context.Background(), stub, Pin{TenantID: "t1"})
		if ok {
			t.Error("expected ok=false")
		}
		if _, bound := EffectiveNow(ctx); bound {
			t.Error("expected ctx to remain unbound")
		}
	})
}

type stubResolver struct {
	invoiceFunc func() (time.Time, error)
	subFunc     func() (time.Time, error)
	custFunc    func() (time.Time, error)
}

func (s *stubResolver) EffectiveNowForInvoice(_ context.Context, _, _ string) (time.Time, error) {
	if s.invoiceFunc == nil {
		return time.Time{}, errors.New("invoiceFunc not stubbed")
	}
	return s.invoiceFunc()
}

func (s *stubResolver) EffectiveNowForSubscription(_ context.Context, _, _ string) (time.Time, error) {
	if s.subFunc == nil {
		return time.Time{}, errors.New("subFunc not stubbed")
	}
	return s.subFunc()
}

func (s *stubResolver) EffectiveNowForCustomer(_ context.Context, _, _ string) (time.Time, error) {
	if s.custFunc == nil {
		return time.Time{}, errors.New("custFunc not stubbed")
	}
	return s.custFunc()
}

func TestIsSimulated(t *testing.T) {
	// Unbound ctx → wall-clock → not simulated.
	if IsSimulated(context.Background()) {
		t.Error("IsSimulated on an unbound ctx: got true, want false")
	}
	// nil ctx is tolerated (EffectiveNow guards it) → not simulated.
	//nolint:staticcheck // exercising the nil-ctx guard deliberately
	if IsSimulated(nil) {
		t.Error("IsSimulated(nil): got true, want false")
	}
	// Bound to a frozen clock → simulated.
	frozen := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	ctx := WithEffectiveNow(context.Background(), frozen)
	if !IsSimulated(ctx) {
		t.Error("IsSimulated on a clock-bound ctx: got false, want true")
	}
}
