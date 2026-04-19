package clock

import (
	"sync"
	"time"
)

// Clock provides the current time. Use Real() in production and NewFake() in tests.
type Clock interface {
	Now() time.Time
}

// Real returns a clock that uses the system time.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// Fake is a controllable clock for testing.
type Fake struct {
	mu      sync.Mutex
	current time.Time
}

// NewFake creates a fake clock fixed at the given time.
func NewFake(t time.Time) *Fake {
	return &Fake{current: t.UTC()}
}

func (c *Fake) Now() time.Time {
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
