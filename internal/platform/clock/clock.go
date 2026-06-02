package clock

import (
	"context"
	"sync"
	"time"
)

// Clock provides the current time. The ctx parameter lets callers ride
// a request-scoped effective-now (a test clock's frozen_time) plumbed
// via WithEffectiveNow at operator entry points; without one, callers
// get wall-clock.
//
// Why ctx-aware, not bare Now(): on a clock-pinned billing entity,
// every business-logic timestamp must be in the test clock's
// frozen_time domain (Stripe's "no semantic change" guarantee). A
// bare wall-clock Now() forces every callsite to remember to resolve
// the pin manually — exactly the leak we hit shipping ADR-029. With
// a ctx-aware Now(ctx), services bind effective-now once at the
// operator entry point and every downstream callsite (including
// stores) inherits automatically.
//
// Callers that legitimately want wall-clock regardless of pinning
// (cron tick scheduler, webhook delivery timestamps, postgres
// updated_at columns, audit-log recorded_at) call time.Now().UTC()
// directly — we don't go through Clock for those, by design.
type Clock interface {
	// Now returns ctx's effective-now if bound (clock.WithEffectiveNow),
	// otherwise wall-clock UTC. The ctx-binding shape replaces the
	// per-callsite ClockResolver pattern shipped in earlier ADR-029
	// fixes — ctx propagation gives compositional correctness without
	// every callsite having to remember to resolve.
	Now(ctx context.Context) time.Time
}

// Real returns a clock that uses the system time when ctx has no bound
// effective-now, and the bound value otherwise.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now(ctx context.Context) time.Time {
	if t, ok := EffectiveNow(ctx); ok {
		return t
	}
	return time.Now().UTC()
}

// Fake is a controllable clock for testing. The bound effective-now
// from ctx still wins over Fake's `current` field — tests that want to
// exercise the ctx-binding path can do so without losing Fake's
// per-test deterministic value.
type Fake struct {
	mu      sync.Mutex
	current time.Time
}

// NewFake creates a fake clock fixed at the given time.
func NewFake(t time.Time) *Fake {
	return &Fake{current: t.UTC()}
}

func (c *Fake) Now(ctx context.Context) time.Time {
	if t, ok := EffectiveNow(ctx); ok {
		return t
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// Set changes the clock to a specific time.
func (c *Fake) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = t.UTC()
}

// Advance moves the clock forward by the given duration.
func (c *Fake) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

// effectiveNowKey is the ctx key for the bound effective-now. Private
// type so external packages can't poke the binding without going
// through WithEffectiveNow / EffectiveNow.
type effectiveNowKeyType struct{}

var effectiveNowKey = effectiveNowKeyType{}

// WithEffectiveNow binds an effective-now timestamp to ctx. Every
// downstream Clock.Now(ctx) call reads this value instead of
// wall-clock. Used at operator entry points on clock-pinned entities:
// resolve the pin via Resolver, bind here, and every nested service /
// store / render call inherits the simulated time.
//
// The contract is "binding at the boundary, inheritance below it."
// New code doesn't have to think about test clocks — touching
// `s.clock.Now(ctx)` Just Works.
func WithEffectiveNow(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, effectiveNowKey, t.UTC())
}

// EffectiveNow returns the bound timestamp and true when WithEffectiveNow
// has been called on this ctx; zero time and false otherwise.
func EffectiveNow(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	v := ctx.Value(effectiveNowKey)
	if v == nil {
		return time.Time{}, false
	}
	t, ok := v.(time.Time)
	return t, ok
}

// Now is the package-level shortcut for "the simulated time if ctx is
// bound, wall-clock otherwise" — same semantics as Real().Now(ctx) but
// with zero allocation overhead and no clock-field dep on the caller.
//
// Used by postgres stores and other infrastructure code that doesn't
// own a Clock struct field but still needs ctx-aware timestamps.
// Service-layer code should keep using s.clock.Now(ctx) so tests can
// pin a Fake clock; stores read directly from ctx because their
// production behavior is identical to Real() and tests bind ctx
// directly.
func Now(ctx context.Context) time.Time {
	if t, ok := EffectiveNow(ctx); ok {
		return t
	}
	return time.Now().UTC()
}

// IsSimulated reports whether ctx is bound to a test clock's effective-now
// (via WithEffectiveNow / BindEffectiveNow). True means every clock.Now(ctx)
// on this ctx returns simulated frozen-time, not wall-clock — so any domain
// timestamp stamped under this ctx lives on a customer's simulated timeline.
// Callers persist this at write time (e.g. invoice.is_simulated) so read-side
// code renders a "simulated" badge without re-deriving from a mutable pin.
func IsSimulated(ctx context.Context) bool {
	_, ok := EffectiveNow(ctx)
	return ok
}

// Resolver maps an entity reference to its effective-now — frozen_time
// when the entity is pinned to a test clock, wall-clock otherwise.
// Used at operator entry points to bind ctx via WithEffectiveNow, and
// at boundary code paths (Stripe webhook handlers, async workers)
// where there's no inherited ctx binding.
//
// Implemented by *billing.Engine. The platform/clock package owns the
// interface to avoid an import cycle (every domain that needs binding
// can depend on clock without depending on billing).
type Resolver interface {
	EffectiveNowForCustomer(ctx context.Context, tenantID, customerID string) (time.Time, error)
	EffectiveNowForSubscription(ctx context.Context, tenantID, subscriptionID string) (time.Time, error)
	EffectiveNowForInvoice(ctx context.Context, tenantID, invoiceID string) (time.Time, error)
}

// Pin describes which entity to resolve effective-now from. Exactly
// one of CustomerID / SubscriptionID / InvoiceID must be non-empty;
// callers pick the most specific id available. TenantID is always
// required.
type Pin struct {
	TenantID       string
	CustomerID     string
	SubscriptionID string
	InvoiceID      string
}

// BindEffectiveNow resolves a Pin via the resolver and returns a ctx
// with effective-now bound. On resolver error, returns the original
// ctx unmodified — failing the operator's action on a dangling pin
// is worse than stamping the wrong domain on an edge-case row. The
// caller can log the error if they care; most don't.
//
// The returned bool is true when the ctx now carries effective-now,
// false when the binding was skipped (no resolver, no ids, or error).
func BindEffectiveNow(ctx context.Context, r Resolver, p Pin) (context.Context, bool) {
	if r == nil {
		return ctx, false
	}
	var (
		t   time.Time
		err error
	)
	switch {
	case p.InvoiceID != "":
		t, err = r.EffectiveNowForInvoice(ctx, p.TenantID, p.InvoiceID)
	case p.SubscriptionID != "":
		t, err = r.EffectiveNowForSubscription(ctx, p.TenantID, p.SubscriptionID)
	case p.CustomerID != "":
		t, err = r.EffectiveNowForCustomer(ctx, p.TenantID, p.CustomerID)
	default:
		return ctx, false
	}
	if err != nil {
		return ctx, false
	}
	return WithEffectiveNow(ctx, t), true
}
