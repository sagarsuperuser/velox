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
		OnStateChange: func(from, to State) {
			transitions = append(transitions, string(from)+"->"+string(to))
		},
	})
	return b, &transitions
}

func exec(b *Breaker, ret error) error {
	_, err := b.Execute(context.Background(), func(context.Context) (any, error) {
		return nil, ret
	})
	return err
}

func TestBreaker_ClosedByDefault(t *testing.T) {
	b, _ := newTestBreaker(t, 3, time.Minute)
	if s := b.State(); s != StateClosed {
		t.Errorf("state: got %q, want closed", s)
	}
	if err := exec(b, nil); err != nil {
		t.Errorf("success on fresh breaker: got %v, want nil", err)
	}
}

func TestBreaker_OpensAfterConsecutiveUnknowns(t *testing.T) {
	b, tr := newTestBreaker(t, 3, time.Minute)

	for range 2 {
		if err := exec(b, errUnknown); !errors.Is(err, errUnknown) {
			t.Fatalf("under threshold: got %v, want errUnknown passed through", err)
		}
		if s := b.State(); s != StateClosed {
			t.Fatalf("state below threshold: got %q, want closed", s)
		}
	}
	// 3rd consecutive unknown — breaker trips, but this call still returns
	// the underlying error (the call happened and failed).
	if err := exec(b, errUnknown); !errors.Is(err, errUnknown) {
		t.Fatalf("trip call: got %v, want errUnknown", err)
	}
	if s := b.State(); s != StateOpen {
		t.Fatalf("state at threshold: got %q, want open", s)
	}
	// Subsequent call is rejected without invoking fn.
	called := false
	_, err := b.Execute(context.Background(), func(context.Context) (any, error) {
		called = true
		return nil, nil
	})
	if !errors.Is(err, ErrOpen) {
		t.Errorf("post-trip call: got %v, want ErrOpen", err)
	}
	if called {
		t.Error("fn must not be invoked when breaker is open")
	}
	if len(*tr) != 1 || (*tr)[0] != "closed->open" {
		t.Errorf("transitions: got %v, want [closed->open]", *tr)
	}
}

func TestBreaker_CardDeclinesDoNotTrip(t *testing.T) {
	b, tr := newTestBreaker(t, 3, time.Minute)

	// 10 card declines must not move the breaker — they're customer problems,
	// not Stripe problems. Callers with a batch of expired cards in their
	// book must keep getting straight-through answers.
	for range 10 {
		if err := exec(b, errDecline); !errors.Is(err, errDecline) {
			t.Fatalf("decline: got %v, want errDecline", err)
		}
	}
	if s := b.State(); s != StateClosed {
		t.Errorf("state after 10 declines: got %q, want closed", s)
	}
	if len(*tr) != 0 {
		t.Errorf("declines must not trigger state transitions; got %v", *tr)
	}
}

func TestBreaker_SuccessResetsConsecutiveCounter(t *testing.T) {
	b, _ := newTestBreaker(t, 3, time.Minute)

	_ = exec(b, errUnknown)
	_ = exec(b, errUnknown)
	_ = exec(b, nil) // success — clears consecutive counter
	_ = exec(b, errUnknown)
	_ = exec(b, errUnknown)
	if s := b.State(); s != StateClosed {
		t.Errorf("2+success+2 unknowns must not trip: got %q", s)
	}
}

func TestBreaker_CooldownGraduatesToHalfOpenAndProbeSucceeds(t *testing.T) {
	b, tr := newTestBreaker(t, 2, 50*time.Millisecond)
	_ = exec(b, errUnknown)
	_ = exec(b, errUnknown) // open now

	// Mid-cooldown: blocked.
	if err := exec(b, nil); !errors.Is(err, ErrOpen) {
		t.Fatalf("mid-cooldown: got %v, want ErrOpen", err)
	}

	time.Sleep(60 * time.Millisecond)

	// First call after cooldown → half-open probe → success → closed.
	if err := exec(b, nil); err != nil {
		t.Fatalf("post-cooldown probe: got %v, want nil", err)
	}
	if s := b.State(); s != StateClosed {
		t.Errorf("state after successful probe: got %q, want closed", s)
	}

	got := *tr
	want := []string{"closed->open", "open->half_open", "half_open->closed"}
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
	_ = exec(b, errUnknown)
	_ = exec(b, errUnknown) // open
	time.Sleep(60 * time.Millisecond)
	_ = exec(b, errUnknown) // probe fails → re-open with fresh cooldown

	if s := b.State(); s != StateOpen {
		t.Fatalf("state after failed probe: got %q, want open", s)
	}
	got := *tr
	want := []string{"closed->open", "open->half_open", "half_open->open"}
	if len(got) != len(want) {
		t.Fatalf("transitions: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("transition %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// A rejected call (ErrOpen) must not itself count toward breaker state — a
// stream of rejections should leave Counts unchanged. This is gobreaker's
// default behavior but pin it here since a regression would mean the
// breaker could never recover once open under sustained incoming traffic.
func TestBreaker_RejectionsDoNotAffectState(t *testing.T) {
	b, _ := newTestBreaker(t, 2, time.Hour)
	_ = exec(b, errUnknown)
	_ = exec(b, errUnknown) // open

	for range 100 {
		if err := exec(b, nil); !errors.Is(err, ErrOpen) {
			t.Fatalf("during open: got %v, want ErrOpen", err)
		}
	}
	if s := b.State(); s != StateOpen {
		t.Errorf("state after 100 rejections: got %q, want still open", s)
	}
}
