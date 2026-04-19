package breaker

import (
	"context"
	"errors"
	"testing"
	"time"
)

var (
	errUnknown = errors.New("stripe 502")
	errDecline = errors.New("card declined")
)

// unknownOnly classifies errUnknown as a countable failure; errDecline is
// excluded from breaker accounting.
func unknownOnly(err error) bool {
	return errors.Is(err, errUnknown)
}

func newTestBreaker(t *testing.T, threshold int, cooldown time.Duration) (*Breaker, *[]string) {
	t.Helper()
	var transitions []string
	b := New(Config{
		FailureThreshold: threshold,
		Cooldown:         cooldown,
		Interval:         0, // never reset counter via timer in tests
		Countable:        unknownOnly,
		OnStateChange: func(tenantID string, from, to State) {
			transitions = append(transitions, string(from)+"->"+string(to)+"/"+tenantID)
		},
	})
	return b, &transitions
}

func exec(b *Breaker, tenantID string, ret error) error {
	_, err := b.Execute(context.Background(), tenantID, func(context.Context) (any, error) {
		return nil, ret
	})
	return err
}

func TestBreaker_ClosedByDefault(t *testing.T) {
	b, _ := newTestBreaker(t, 3, time.Minute)
	if s := b.State("t1"); s != StateClosed {
		t.Errorf("state: got %q, want closed", s)
	}
	if err := exec(b, "t1", nil); err != nil {
		t.Errorf("success on fresh tenant: got %v, want nil", err)
	}
}

func TestBreaker_OpensAfterConsecutiveUnknowns(t *testing.T) {
	b, tr := newTestBreaker(t, 3, time.Minute)

	for range 2 {
		if err := exec(b, "t1", errUnknown); !errors.Is(err, errUnknown) {
			t.Fatalf("under threshold: got %v, want errUnknown passed through", err)
		}
		if s := b.State("t1"); s != StateClosed {
			t.Fatalf("state below threshold: got %q, want closed", s)
		}
	}
	// 3rd consecutive unknown — breaker trips, but this call still returns
	// the underlying error (the call happened and failed).
	if err := exec(b, "t1", errUnknown); !errors.Is(err, errUnknown) {
		t.Fatalf("trip call: got %v, want errUnknown", err)
	}
	if s := b.State("t1"); s != StateOpen {
		t.Fatalf("state at threshold: got %q, want open", s)
	}
	// Subsequent call is rejected without invoking fn.
	called := false
	_, err := b.Execute(context.Background(), "t1", func(context.Context) (any, error) {
		called = true
		return nil, nil
	})
	if !errors.Is(err, ErrOpen) {
		t.Errorf("post-trip call: got %v, want ErrOpen", err)
	}
	if called {
		t.Error("fn must not be invoked when breaker is open")
	}
	if len(*tr) != 1 || (*tr)[0] != "closed->open/t1" {
		t.Errorf("transitions: got %v, want [closed->open/t1]", *tr)
	}
}

func TestBreaker_CardDeclinesDoNotTrip(t *testing.T) {
	b, tr := newTestBreaker(t, 3, time.Minute)

	// 10 card declines must not move the breaker — they're customer problems,
	// not Stripe problems. A tenant with a batch of expired cards in their
	// book must keep getting straight-through answers.
	for range 10 {
		if err := exec(b, "t1", errDecline); !errors.Is(err, errDecline) {
			t.Fatalf("decline: got %v, want errDecline", err)
		}
	}
	if s := b.State("t1"); s != StateClosed {
		t.Errorf("state after 10 declines: got %q, want closed", s)
	}
	if len(*tr) != 0 {
		t.Errorf("declines must not trigger state transitions; got %v", *tr)
	}
}

func TestBreaker_SuccessResetsConsecutiveCounter(t *testing.T) {
	b, _ := newTestBreaker(t, 3, time.Minute)

	_ = exec(b, "t1", errUnknown)
	_ = exec(b, "t1", errUnknown)
	_ = exec(b, "t1", nil) // success — clears consecutive counter
	_ = exec(b, "t1", errUnknown)
	_ = exec(b, "t1", errUnknown)
	if s := b.State("t1"); s != StateClosed {
		t.Errorf("2+success+2 unknowns must not trip: got %q", s)
	}
}

func TestBreaker_CooldownGraduatesToHalfOpenAndProbeSucceeds(t *testing.T) {
	b, tr := newTestBreaker(t, 2, 50*time.Millisecond)
	_ = exec(b, "t1", errUnknown)
	_ = exec(b, "t1", errUnknown) // open now

	// Mid-cooldown: blocked.
	if err := exec(b, "t1", nil); !errors.Is(err, ErrOpen) {
		t.Fatalf("mid-cooldown: got %v, want ErrOpen", err)
	}

	time.Sleep(60 * time.Millisecond)

	// First call after cooldown → half-open probe → success → closed.
	if err := exec(b, "t1", nil); err != nil {
		t.Fatalf("post-cooldown probe: got %v, want nil", err)
	}
	if s := b.State("t1"); s != StateClosed {
		t.Errorf("state after successful probe: got %q, want closed", s)
	}

	got := *tr
	want := []string{"closed->open/t1", "open->half_open/t1", "half_open->closed/t1"}
	if len(got) != len(want) {
		t.Fatalf("transitions: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("transition %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	b, tr := newTestBreaker(t, 2, 50*time.Millisecond)
	_ = exec(b, "t1", errUnknown)
	_ = exec(b, "t1", errUnknown) // open
	time.Sleep(60 * time.Millisecond)
	_ = exec(b, "t1", errUnknown) // probe fails → re-open with fresh cooldown

	if s := b.State("t1"); s != StateOpen {
		t.Fatalf("state after failed probe: got %q, want open", s)
	}
	got := *tr
	want := []string{"closed->open/t1", "open->half_open/t1", "half_open->open/t1"}
	if len(got) != len(want) {
		t.Fatalf("transitions: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("transition %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBreaker_ResetClosesOpen(t *testing.T) {
	b, tr := newTestBreaker(t, 2, time.Hour)
	_ = exec(b, "t1", errUnknown)
	_ = exec(b, "t1", errUnknown) // open
	b.Reset("t1")

	if s := b.State("t1"); s != StateClosed {
		t.Errorf("state after reset: got %q, want closed", s)
	}
	if err := exec(b, "t1", nil); err != nil {
		t.Errorf("Execute after reset: got %v, want nil", err)
	}
	// Reset must clear the consecutive-failure counter too: one fresh
	// unknown must not immediately retrip the breaker.
	_ = exec(b, "t1", errUnknown)
	if s := b.State("t1"); s != StateClosed {
		t.Errorf("Reset failed to clear counter: one unknown retripped (%q)", s)
	}

	// closed->open from the initial trips, then open->closed from Reset.
	got := *tr
	if len(got) < 2 || got[len(got)-1] != "open->closed/t1" {
		t.Errorf("reset transitions: got %v", got)
	}
}

func TestBreaker_TenantsIsolated(t *testing.T) {
	b, _ := newTestBreaker(t, 2, time.Hour)
	_ = exec(b, "t1", errUnknown)
	_ = exec(b, "t1", errUnknown) // t1 open

	if b.State("t1") != StateOpen {
		t.Fatalf("t1 should be open")
	}
	if b.State("t2") != StateClosed {
		t.Errorf("t2 should remain closed while t1 is open; got %q", b.State("t2"))
	}
	if err := exec(b, "t2", nil); err != nil {
		t.Errorf("t2 Execute: got %v, want nil — one tenant must not block others", err)
	}
}

// A rejected call (ErrOpen) must not itself count toward breaker state — a
// stream of rejections should leave Counts unchanged. This is gobreaker's
// default behavior but pin it here since a regression would mean the
// breaker could never recover once open under sustained incoming traffic.
func TestBreaker_RejectionsDoNotAffectState(t *testing.T) {
	b, _ := newTestBreaker(t, 2, time.Hour)
	_ = exec(b, "t1", errUnknown)
	_ = exec(b, "t1", errUnknown) // open

	for range 100 {
		if err := exec(b, "t1", nil); !errors.Is(err, ErrOpen) {
			t.Fatalf("during open: got %v, want ErrOpen", err)
		}
	}
	if s := b.State("t1"); s != StateOpen {
		t.Errorf("state after 100 rejections: got %q, want still open", s)
	}
}
