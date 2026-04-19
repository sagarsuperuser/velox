// Package breaker provides a per-tenant circuit breaker around Stripe API
// calls, built on sony/gobreaker/v2. One misbehaving tenant — or Stripe
// having a rough hour for accounts in a specific region — must not burn
// request budget for every other tenant on the platform, so each tenant
// has its own state machine.
//
// Only "unknown" outcomes (5xx, timeout, connection reset — i.e. the
// Stripe-side problem category) count as failures. Card declines,
// validation errors, and breaker-rejected calls are excluded from breaker
// accounting so a bad merchant card batch doesn't open every tenant's
// breaker.
package breaker

import (
	"context"
	"errors"
	"sync"
	"time"

	gb "github.com/sony/gobreaker/v2"
)

// State is a human-readable breaker state; emitted as a metric label.
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

func fromGB(s gb.State) State {
	switch s {
	case gb.StateClosed:
		return StateClosed
	case gb.StateHalfOpen:
		return StateHalfOpen
	case gb.StateOpen:
		return StateOpen
	}
	return StateClosed
}

// ErrOpen is returned when the tenant's breaker is open (or half-open and
// at its probe limit). Callers should treat this as a transient, retryable
// failure — the Stripe call never happened, so don't mark the invoice
// failed and don't tick the dunning attempt count.
//
// A single sentinel unifies gobreaker's ErrOpenState and ErrTooManyRequests
// for our callers, since the two conditions are operationally identical:
// "breaker is protecting Stripe right now, try later."
var ErrOpen = errors.New("stripe circuit breaker open for tenant")

// Countable reports whether an error returned from a Stripe call should
// count as a breaker failure. The canonical implementation in package
// payment returns true for PaymentError.Unknown (5xx/timeout) and false
// for card declines and validation errors.
//
// Implementations must NOT return true for nil — a nil error is always
// success and gobreaker handles that itself.
type Countable func(err error) bool

// Config tunes the breaker. Defaults target Stripe's published error
// rates: at 5 consecutive Unknown failures, we're almost certainly looking
// at an incident, not bad luck, and rapid retries only worsen the blast
// radius.
type Config struct {
	// FailureThreshold is the number of consecutive Unknown failures that
	// will open the breaker. Defaults to 5.
	FailureThreshold int
	// Cooldown is how long the breaker stays open before allowing a probe.
	// Defaults to 30s.
	Cooldown time.Duration
	// Interval clears the internal Counts periodically in the closed state,
	// so isolated failures spaced far apart don't accumulate toward the
	// threshold. 0 disables (counts persist until a success resets them).
	// Defaults to 60s.
	Interval time.Duration
	// Countable classifies errors returned from Stripe. Required.
	Countable Countable
	// OnStateChange fires whenever a tenant transitions states.
	OnStateChange func(tenantID string, from, to State)
}

// Breaker is a thread-safe per-tenant circuit breaker.
type Breaker struct {
	cfg      Config
	mu       sync.Mutex
	breakers map[string]*gb.CircuitBreaker[any]
}

// New constructs a Breaker. cfg.Countable is required; zero values for
// other fields are replaced with defaults.
func New(cfg Config) *Breaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 60 * time.Second
	}
	if cfg.Countable == nil {
		// Safer default: treat every error as a failure. Production wiring
		// always supplies a real classifier — this default exists only to
		// avoid a nil deref if someone forgets.
		cfg.Countable = func(err error) bool { return err != nil }
	}
	return &Breaker{cfg: cfg, breakers: make(map[string]*gb.CircuitBreaker[any])}
}

// Execute runs fn under tenantID's breaker. Returns ErrOpen if the breaker
// is rejecting; otherwise returns fn's (T, error). Only errors where
// cfg.Countable returns true count toward the breaker's failure counter —
// card declines return an error but are excluded from accounting.
func (b *Breaker) Execute(ctx context.Context, tenantID string, fn func(context.Context) (any, error)) (any, error) {
	cb := b.get(tenantID)
	result, err := cb.Execute(func() (any, error) {
		return fn(ctx)
	})
	if errors.Is(err, gb.ErrOpenState) || errors.Is(err, gb.ErrTooManyRequests) {
		return nil, ErrOpen
	}
	return result, err
}

// Reset forces tenantID's breaker back to closed. Used by the manual
// operator endpoint after Stripe's status page confirms recovery, so a
// tenant can shave the final cooldown off their own recovery time without
// waiting for the probe cycle.
//
// Implemented by dropping the breaker from the map — the next Execute
// recreates it in the closed state. This works because gobreaker exposes
// no public Reset and the per-tenant breaker has no cross-call state we
// care to preserve (Counts are internal and only drive trip decisions).
// The OnStateChange callback fires only on real transitions inside the
// breaker; a destroy-and-recreate is invisible to it, so we emit a
// synthetic "* -> closed" notification ourselves.
func (b *Breaker) Reset(tenantID string) {
	b.mu.Lock()
	cb, existed := b.breakers[tenantID]
	var prev State
	if existed {
		prev = fromGB(cb.State())
	}
	delete(b.breakers, tenantID)
	b.mu.Unlock()

	if existed && prev != StateClosed && b.cfg.OnStateChange != nil {
		b.cfg.OnStateChange(tenantID, prev, StateClosed)
	}
}

// State returns the current state for tenantID. Intended for metrics and
// the admin endpoint response; do NOT gate calls on this (races with
// concurrent Execute) — always call Execute and handle ErrOpen.
func (b *Breaker) State(tenantID string) State {
	b.mu.Lock()
	cb, ok := b.breakers[tenantID]
	b.mu.Unlock()
	if !ok {
		return StateClosed
	}
	return fromGB(cb.State())
}

// get returns the breaker for tenantID, creating it on first use.
func (b *Breaker) get(tenantID string) *gb.CircuitBreaker[any] {
	b.mu.Lock()
	defer b.mu.Unlock()

	if cb, ok := b.breakers[tenantID]; ok {
		return cb
	}

	threshold := b.cfg.FailureThreshold
	countable := b.cfg.Countable
	onChange := b.cfg.OnStateChange

	cb := gb.NewCircuitBreaker[any](gb.Settings{
		Name:        "stripe:" + tenantID,
		MaxRequests: 1, // one half-open probe at a time
		Interval:    b.cfg.Interval,
		Timeout:     b.cfg.Cooldown,
		ReadyToTrip: func(c gb.Counts) bool {
			return int(c.ConsecutiveFailures) >= threshold
		},
		// IsSuccessful: nil errors → success; non-nil errors → failure.
		// IsExcluded rescues card declines (customer problem, not Stripe
		// problem) from being counted as failures at all. gobreaker checks
		// IsExcluded *before* IsSuccessful, so excluded errors also aren't
		// counted as successes — they're neither.
		IsExcluded: func(err error) bool {
			if err == nil {
				return false
			}
			return !countable(err)
		},
		OnStateChange: func(_ string, from, to gb.State) {
			if onChange != nil {
				onChange(tenantID, fromGB(from), fromGB(to))
			}
		},
	})
	b.breakers[tenantID] = cb
	return cb
}
