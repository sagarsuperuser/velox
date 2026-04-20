// Package breaker provides a global circuit breaker around Stripe API
// calls, built on sony/gobreaker/v2. Stripe is a single external
// dependency; when it's unhealthy (5xx, timeout, connection reset), every
// caller should back off together so we don't pile on during an incident.
//
// Only "unknown" outcomes (5xx, timeout, connection reset — i.e. the
// Stripe-side problem category) count as failures. Card declines,
// validation errors, and breaker-rejected calls are excluded from breaker
// accounting so a batch of expired cards doesn't trip the breaker.
//
// Per-tenant isolation is not provided here — if one tenant's Stripe
// account is consistently 5xx-ing, that's a rare case we can split off
// later. The operational complexity of per-tenant breakers (state machine
// per tenant, metric cardinality, admin endpoints) isn't worth paying for
// a scenario that hasn't shown up in production.
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

// ErrOpen is returned when the breaker is open (or half-open and at its
// probe limit). Callers should treat this as a transient, retryable
// failure — the Stripe call never happened, so don't mark the invoice
// failed and don't tick the dunning attempt count.
//
// A single sentinel unifies gobreaker's ErrOpenState and ErrTooManyRequests
// for our callers, since the two conditions are operationally identical:
// "breaker is protecting Stripe right now, try later."
var ErrOpen = errors.New("stripe circuit breaker open")

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
	// OnStateChange fires whenever the breaker transitions states.
	OnStateChange func(from, to State)
}

// Breaker is a thread-safe global circuit breaker.
type Breaker struct {
	cb *gb.CircuitBreaker[any]
	mu sync.Mutex
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

	threshold := cfg.FailureThreshold
	countable := cfg.Countable
	onChange := cfg.OnStateChange

	cb := gb.NewCircuitBreaker[any](gb.Settings{
		Name:        "stripe",
		MaxRequests: 1, // one half-open probe at a time
		Interval:    cfg.Interval,
		Timeout:     cfg.Cooldown,
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
				onChange(fromGB(from), fromGB(to))
			}
		},
	})

	return &Breaker{cb: cb}
}

// Execute runs fn under the breaker. Returns ErrOpen if the breaker is
// rejecting; otherwise returns fn's (any, error). Only errors where
// cfg.Countable returns true count toward the breaker's failure counter —
// card declines return an error but are excluded from accounting.
func (b *Breaker) Execute(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	result, err := b.cb.Execute(func() (any, error) {
		return fn(ctx)
	})
	if errors.Is(err, gb.ErrOpenState) || errors.Is(err, gb.ErrTooManyRequests) {
		return nil, ErrOpen
	}
	return result, err
}

// State returns the current breaker state. Intended for metrics; do NOT
// gate calls on this (races with concurrent Execute) — always call
// Execute and handle ErrOpen.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return fromGB(b.cb.State())
}
