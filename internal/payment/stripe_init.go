package payment

import (
	"sync"

	"github.com/stripe/stripe-go/v82"
)

// InitStripe sets the Stripe SDK secret key once at process startup.
// The Stripe SDK uses a package-global `stripe.Key`; mutating it per
// request races under concurrent load (and could serve requests with
// the wrong tenant's key in a multi-key future). Call this once from
// main/router wiring and never touch stripe.Key again.
func InitStripe(apiKey string) {
	if apiKey == "" {
		return
	}
	stripeInitOnce.Do(func() {
		stripe.Key = apiKey
	})
}

var stripeInitOnce sync.Once
