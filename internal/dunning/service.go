package dunning

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// PaymentRetrier retries a payment for an invoice.
type PaymentRetrier interface {
	RetryPayment(ctx context.Context, tenantID, invoiceID, stripeCustomerID string) error
}

// SubscriptionPauser pauses a subscription when dunning exhausts retries.
type SubscriptionPauser interface {
	Pause(ctx context.Context, tenantID, id string) error
}

// InvoiceGetter gets invoice details for finding the subscription.
type InvoiceGetter interface {
	Get(ctx context.Context, tenantID, id string) (domain.Invoice, error)
}

// CustomerEmailFetcher resolves customer contact info for email notifications.
type CustomerEmailFetcher interface {
	GetCustomerEmail(ctx context.Context, tenantID, customerID string) (email, displayName string, err error)
}

// EmailNotifier sends dunning-related emails.
type EmailNotifier interface {
	SendPaymentFailed(to, customerName, invoiceNumber, reason string) error
	SendDunningWarning(to, customerName, invoiceNumber string, attemptNumber, maxAttempts int, nextRetryDate string) error
	SendDunningEscalation(to, customerName, invoiceNumber string, action string) error
}

type Service struct {
	store         Store
	retrier       PaymentRetrier
	subPauser     SubscriptionPauser
	invoiceGet    InvoiceGetter
	events        domain.EventDispatcher
	emailNotifier EmailNotifier
	customerEmail CustomerEmailFetcher
	clock         clock.Clock
}

func NewService(store Store, retrier PaymentRetrier, clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{store: store, retrier: retrier, clock: clk}
}

func (s *Service) SetRetrier(retrier PaymentRetrier) {
	s.retrier = retrier
}

// SetSubscriptionPauser configures subscription pausing for dunning final actions.
func (s *Service) SetSubscriptionPauser(pauser SubscriptionPauser, invoices InvoiceGetter) {
	s.subPauser = pauser
	s.invoiceGet = invoices
}

// SetEmailNotifier configures email notifications for dunning events.
func (s *Service) SetEmailNotifier(notifier EmailNotifier) {
	s.emailNotifier = notifier
}

// SetCustomerEmailFetcher configures customer email resolution for dunning notifications.
func (s *Service) SetCustomerEmailFetcher(fetcher CustomerEmailFetcher) {
	s.customerEmail = fetcher
}

// SetEventDispatcher configures outbound webhook event firing.
func (s *Service) SetEventDispatcher(events domain.EventDispatcher) {
	s.events = events
}

func (s *Service) fireEvent(ctx context.Context, tenantID, eventType string, payload map[string]any) {
	if s.events == nil {
		return
	}
	go func() {
		_ = s.events.Dispatch(ctx, tenantID, eventType, payload)
	}()
}

// StartDunning initiates a dunning run for a failed invoice payment.
func (s *Service) StartDunning(ctx context.Context, tenantID string, invoiceID, customerID string) (domain.InvoiceDunningRun, error) {
	// Check if there's already an active run for this invoice
	existing, err := s.store.GetActiveRunByInvoice(ctx, tenantID, invoiceID)
	if err == nil && existing.ID != "" {
		return existing, nil // Idempotent — return existing run
	}

	policy, err := s.store.GetPolicy(ctx, tenantID)
	if err != nil {
		return domain.InvoiceDunningRun{}, fmt.Errorf("get dunning policy: %w", err)
	}
	if !policy.Enabled {
		return domain.InvoiceDunningRun{}, fmt.Errorf("dunning is disabled for this tenant")
	}

	// Check for customer-specific dunning override
	if override, err := s.store.GetCustomerOverride(ctx, tenantID, customerID); err == nil {
		if override.MaxRetryAttempts != nil {
			policy.MaxRetryAttempts = *override.MaxRetryAttempts
		}
		if override.GracePeriodDays != nil {
			policy.GracePeriodDays = *override.GracePeriodDays
		}
		if override.FinalAction != "" {
			policy.FinalAction = domain.DunningFinalAction(override.FinalAction)
		}
	}

	// Grace period determines when the first retry happens.
	// retry_schedule determines the intervals between subsequent retries.
	firstRetryDelay := time.Duration(policy.GracePeriodDays) * 24 * time.Hour
	if firstRetryDelay <= 0 {
		firstRetryDelay = 24 * time.Hour // minimum 1 day before first retry
	}

	t := s.clock.Now().Add(firstRetryDelay)
	nextActionAt := &t

	run, err := s.store.CreateRun(ctx, tenantID, domain.InvoiceDunningRun{
		InvoiceID:    invoiceID,
		CustomerID:   customerID,
		PolicyID:     policy.ID,
		State:        domain.DunningActive,
		Reason:       "payment_failed",
		AttemptCount: 0,
		NextActionAt: nextActionAt,
	})
	if err != nil {
		return domain.InvoiceDunningRun{}, fmt.Errorf("create dunning run: %w", err)
	}

	// Record start event
	_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:     run.ID,
		InvoiceID: invoiceID,
		EventType: domain.DunningEventStarted,
		State:     domain.DunningActive,
		Reason:    "payment_failed",
	})

	slog.Info("dunning started", "run_id", run.ID, "invoice_id", invoiceID)

	s.fireEvent(ctx, tenantID, domain.EventDunningStarted, map[string]any{
		"run_id":      run.ID,
		"invoice_id":  invoiceID,
		"customer_id": customerID,
	})

	return run, nil
}

// ProcessDueRuns finds runs due for action and executes retries.
func (s *Service) ProcessDueRuns(ctx context.Context, tenantID string, limit int) (int, []error) {
	if limit <= 0 {
		limit = 20
	}

	dueRuns, err := s.store.ListDueRuns(ctx, tenantID, s.clock.Now(), limit)
	if err != nil {
		return 0, []error{fmt.Errorf("list due runs: %w", err)}
	}

	processed := 0
	var errs []error

	for _, run := range dueRuns {
		if err := s.processRun(ctx, tenantID, run); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", run.ID, err))
			continue
		}
		processed++
	}

	return processed, errs
}

func (s *Service) processRun(ctx context.Context, tenantID string, run domain.InvoiceDunningRun) error {
	policy, err := s.store.GetPolicy(ctx, tenantID)
	if err != nil {
		return err
	}

	// Check for customer-specific dunning override
	if run.CustomerID != "" {
		if override, err := s.store.GetCustomerOverride(ctx, tenantID, run.CustomerID); err == nil {
			if override.MaxRetryAttempts != nil {
				policy.MaxRetryAttempts = *override.MaxRetryAttempts
			}
			if override.GracePeriodDays != nil {
				policy.GracePeriodDays = *override.GracePeriodDays
			}
			if override.FinalAction != "" {
				policy.FinalAction = domain.DunningFinalAction(override.FinalAction)
			}
		}
	}

	if run.Paused {
		return nil // Skip paused runs
	}

	// Check if max retries exhausted
	if run.AttemptCount >= policy.MaxRetryAttempts {
		return s.exhaustRun(ctx, tenantID, run, policy)
	}

	// Attempt retry
	run.AttemptCount++
	now := s.clock.Now()
	run.LastAttemptAt = &now

	// Actually retry the payment
	retryErr := fmt.Errorf("payment retrier not configured")
	if s.retrier != nil {
		retryErr = s.retrier.RetryPayment(ctx, tenantID, run.InvoiceID, run.CustomerID)
	}

	if retryErr != nil {
		run.State = domain.DunningActive // Will retry again later
		slog.Warn("dunning retry failed",
			"run_id", run.ID,
			"invoice_id", run.InvoiceID,
			"attempt", run.AttemptCount,
			"error", retryErr,
		)

		// Record failed retry event
		_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
			RunID:        run.ID,
			InvoiceID:    run.InvoiceID,
			EventType:    domain.DunningEventRetryAttempted,
			State:        domain.DunningActive,
			AttemptCount: run.AttemptCount,
			Reason:       retryErr.Error(),
		})

		// Send dunning warning email asynchronously
		if s.emailNotifier != nil && s.customerEmail != nil {
			go func() {
				email, name, err := s.customerEmail.GetCustomerEmail(ctx, tenantID, run.CustomerID)
				if err != nil || email == "" {
					slog.Warn("skip dunning warning email — cannot resolve customer email",
						"run_id", run.ID, "customer_id", run.CustomerID, "error", err)
					return
				}
				invoiceNumber := run.InvoiceID
				if s.invoiceGet != nil {
					if inv, err := s.invoiceGet.Get(ctx, tenantID, run.InvoiceID); err == nil {
						invoiceNumber = inv.InvoiceNumber
					}
				}
				nextRetry := "TBD"
				if run.NextActionAt != nil {
					nextRetry = run.NextActionAt.Format("January 2, 2006")
				}
				if err := s.emailNotifier.SendDunningWarning(email, name, invoiceNumber, run.AttemptCount, policy.MaxRetryAttempts, nextRetry); err != nil {
					slog.Error("failed to send dunning warning email",
						"run_id", run.ID, "email", email, "error", err)
				}
			}()
		}
	} else {
		run.State = domain.DunningResolved
		run.Resolution = domain.ResolutionPaymentRecovered
		run.ResolvedAt = &now
		run.NextActionAt = nil

		_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
			RunID:        run.ID,
			InvoiceID:    run.InvoiceID,
			EventType:    domain.DunningEventResolved,
			State:        domain.DunningResolved,
			AttemptCount: run.AttemptCount,
			Reason:       "payment_recovered",
		})

		slog.Info("dunning resolved — payment succeeded",
			"run_id", run.ID,
			"invoice_id", run.InvoiceID,
			"attempt", run.AttemptCount,
		)

		if _, err := s.store.UpdateRun(ctx, tenantID, run); err != nil {
			return err
		}
		return nil
	}

	// Schedule next retry.
	// retry_schedule contains the intervals between retries:
	//   retry_schedule[0] = gap between retry 1 and retry 2
	//   retry_schedule[1] = gap between retry 2 and retry 3
	// Grace period (used in StartDunning) determines when retry 1 happens.
	if run.AttemptCount < policy.MaxRetryAttempts {
		// Default intervals between retries: 3 days, 5 days, 7 days
		defaultIntervals := []time.Duration{72 * time.Hour, 120 * time.Hour, 168 * time.Hour}
		retryIntervals := defaultIntervals
		if len(policy.RetrySchedule) > 0 {
			retryIntervals = nil
			for _, s := range policy.RetrySchedule {
				if d, err := time.ParseDuration(s); err == nil {
					retryIntervals = append(retryIntervals, d)
				}
			}
			if len(retryIntervals) == 0 {
				retryIntervals = defaultIntervals
			}
		}
		// AttemptCount is 1-based; schedule[0] is the gap after retry 1
		idx := run.AttemptCount - 1
		if idx >= len(retryIntervals) {
			idx = len(retryIntervals) - 1 // reuse last interval
		}
		if idx >= 0 {
			t := now.Add(retryIntervals[idx])
			run.NextActionAt = &t
		}
	} else {
		run.NextActionAt = nil
	}

	if _, err := s.store.UpdateRun(ctx, tenantID, run); err != nil {
		return err
	}

	// Check if exhausted after this attempt
	if run.AttemptCount >= policy.MaxRetryAttempts {
		return s.exhaustRun(ctx, tenantID, run, policy)
	}

	return nil
}

func (s *Service) exhaustRun(ctx context.Context, tenantID string, run domain.InvoiceDunningRun, policy domain.DunningPolicy) error {
	now := s.clock.Now()
	run.State = domain.DunningEscalated
	run.Resolution = domain.ResolutionRetriesExhausted
	run.ResolvedAt = &now
	run.NextActionAt = nil

	switch policy.FinalAction {
	case domain.DunningActionManualReview:
		// resolution already set
	case domain.DunningActionPause:
		// Actually pause the subscription
		if s.subPauser != nil && s.invoiceGet != nil {
			if inv, err := s.invoiceGet.Get(ctx, tenantID, run.InvoiceID); err == nil && inv.SubscriptionID != "" {
				if err := s.subPauser.Pause(ctx, tenantID, inv.SubscriptionID); err != nil {
					slog.Warn("failed to pause subscription after dunning exhausted",
						"invoice_id", run.InvoiceID, "subscription_id", inv.SubscriptionID, "error", err)
				} else {
					slog.Info("subscription paused by dunning",
						"invoice_id", run.InvoiceID, "subscription_id", inv.SubscriptionID)
				}
			}
		}
	default:
		// write_off_later or unknown — resolution stays retries_exhausted
	}

	_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:        run.ID,
		InvoiceID:    run.InvoiceID,
		EventType:    domain.DunningEventEscalated,
		State:        run.State,
		AttemptCount: run.AttemptCount,
		Reason:       string(policy.FinalAction),
	})

	if _, err := s.store.UpdateRun(ctx, tenantID, run); err != nil {
		return err
	}

	slog.Info("dunning exhausted",
		"run_id", run.ID,
		"invoice_id", run.InvoiceID,
		"final_action", policy.FinalAction,
	)

	// Send dunning escalation email asynchronously
	if s.emailNotifier != nil && s.customerEmail != nil {
		go func() {
			email, name, err := s.customerEmail.GetCustomerEmail(ctx, tenantID, run.CustomerID)
			if err != nil || email == "" {
				slog.Warn("skip dunning escalation email — cannot resolve customer email",
					"run_id", run.ID, "customer_id", run.CustomerID, "error", err)
				return
			}
			invoiceNumber := run.InvoiceID
			if s.invoiceGet != nil {
				if inv, err := s.invoiceGet.Get(ctx, tenantID, run.InvoiceID); err == nil {
					invoiceNumber = inv.InvoiceNumber
				}
			}
			if err := s.emailNotifier.SendDunningEscalation(email, name, invoiceNumber, string(policy.FinalAction)); err != nil {
				slog.Error("failed to send dunning escalation email",
					"run_id", run.ID, "email", email, "error", err)
			}
		}()
	}

	s.fireEvent(ctx, tenantID, domain.EventDunningEscalated, map[string]any{
		"run_id":       run.ID,
		"invoice_id":   run.InvoiceID,
		"customer_id":  run.CustomerID,
		"final_action": string(policy.FinalAction),
		"resolution":   string(run.Resolution),
		"attempts":     run.AttemptCount,
	})

	return nil
}

// ResolveRun marks a dunning run as resolved (e.g., after manual payment).
func (s *Service) ResolveRun(ctx context.Context, tenantID, runID string, resolution domain.DunningResolution) (domain.InvoiceDunningRun, error) {
	run, err := s.store.GetRun(ctx, tenantID, runID)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}

	now := s.clock.Now()
	run.State = domain.DunningResolved
	run.Resolution = resolution
	run.ResolvedAt = &now
	run.NextActionAt = nil

	_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:     run.ID,
		InvoiceID: run.InvoiceID,
		EventType: domain.DunningEventResolved,
		State:     domain.DunningResolved,
		Reason:    string(resolution),
	})

	return s.store.UpdateRun(ctx, tenantID, run)
}

// ResolveByInvoice resolves any active dunning run for the given invoice.
// Called when an invoice is voided or paid outside of dunning.
func (s *Service) ResolveByInvoice(ctx context.Context, tenantID, invoiceID string, resolution domain.DunningResolution) error {
	run, err := s.store.GetActiveRunByInvoice(ctx, tenantID, invoiceID)
	if err != nil {
		return nil // No active run — nothing to resolve
	}

	now := s.clock.Now()
	run.State = domain.DunningResolved
	run.Resolution = resolution
	run.ResolvedAt = &now
	run.NextActionAt = nil

	_, _ = s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:     run.ID,
		InvoiceID: run.InvoiceID,
		EventType: domain.DunningEventResolved,
		State:     domain.DunningResolved,
		Reason:    fmt.Sprintf("invoice %s", string(resolution)),
	})

	_, err = s.store.UpdateRun(ctx, tenantID, run)
	return err
}

// GetPolicy returns the dunning policy for a tenant.
func (s *Service) GetPolicy(ctx context.Context, tenantID string) (domain.DunningPolicy, error) {
	return s.store.GetPolicy(ctx, tenantID)
}

// UpsertPolicy creates or updates the dunning policy for a tenant.
func (s *Service) UpsertPolicy(ctx context.Context, tenantID string, policy domain.DunningPolicy) (domain.DunningPolicy, error) {
	if policy.MaxRetryAttempts <= 0 {
		policy.MaxRetryAttempts = 3
	}
	if policy.MaxRetryAttempts > 15 {
		return domain.DunningPolicy{}, fmt.Errorf("max_retry_attempts cannot exceed 15")
	}
	if policy.GracePeriodDays <= 0 {
		policy.GracePeriodDays = 3
	}
	if policy.GracePeriodDays > 30 {
		return domain.DunningPolicy{}, fmt.Errorf("grace_period_days cannot exceed 30")
	}
	switch policy.FinalAction {
	case domain.DunningActionManualReview, domain.DunningActionPause, domain.DunningActionWriteOff:
		// valid
	case "":
		policy.FinalAction = domain.DunningActionManualReview
	default:
		return domain.DunningPolicy{}, fmt.Errorf("final_action must be one of: manual_review, pause, write_off_later")
	}
	if err := domain.MaxLen("name", policy.Name, 255); err != nil {
		return domain.DunningPolicy{}, err
	}
	return s.store.UpsertPolicy(ctx, tenantID, policy)
}

// GetCustomerOverride returns the dunning override for a specific customer.
func (s *Service) GetCustomerOverride(ctx context.Context, tenantID, customerID string) (domain.CustomerDunningOverride, error) {
	return s.store.GetCustomerOverride(ctx, tenantID, customerID)
}

// UpsertCustomerOverride creates or updates a customer-level dunning override.
func (s *Service) UpsertCustomerOverride(ctx context.Context, tenantID string, override domain.CustomerDunningOverride) (domain.CustomerDunningOverride, error) {
	if override.CustomerID == "" {
		return domain.CustomerDunningOverride{}, fmt.Errorf("customer_id is required")
	}
	if override.MaxRetryAttempts != nil && *override.MaxRetryAttempts > 15 {
		return domain.CustomerDunningOverride{}, fmt.Errorf("max_retry_attempts cannot exceed 15")
	}
	if override.GracePeriodDays != nil && *override.GracePeriodDays > 30 {
		return domain.CustomerDunningOverride{}, fmt.Errorf("grace_period_days cannot exceed 30")
	}
	if override.FinalAction != "" {
		switch domain.DunningFinalAction(override.FinalAction) {
		case domain.DunningActionManualReview, domain.DunningActionPause, domain.DunningActionWriteOff:
			// valid
		default:
			return domain.CustomerDunningOverride{}, fmt.Errorf("final_action must be one of: manual_review, pause, write_off_later")
		}
	}
	return s.store.UpsertCustomerOverride(ctx, tenantID, override)
}

// DeleteCustomerOverride removes a customer-level dunning override.
func (s *Service) DeleteCustomerOverride(ctx context.Context, tenantID, customerID string) error {
	return s.store.DeleteCustomerOverride(ctx, tenantID, customerID)
}

// ListRuns returns dunning runs matching the filter.
func (s *Service) ListRuns(ctx context.Context, filter RunListFilter) ([]domain.InvoiceDunningRun, int, error) {
	return s.store.ListRuns(ctx, filter)
}
