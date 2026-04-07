package dunning

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// PaymentRetrier retries a payment for an invoice.
type PaymentRetrier interface {
	RetryPayment(ctx context.Context, tenantID, invoiceID, stripeCustomerID string) error
}

type Service struct {
	store   Store
	retrier PaymentRetrier
}

func NewService(store Store, retrier PaymentRetrier) *Service {
	return &Service{store: store, retrier: retrier}
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

	// Schedule first retry based on policy
	var nextActionAt *time.Time
	if len(policy.RetrySchedule) > 0 {
		d, err := time.ParseDuration(policy.RetrySchedule[0])
		if err == nil {
			t := time.Now().UTC().Add(d)
			nextActionAt = &t
		}
	}

	run, err := s.store.CreateRun(ctx, tenantID, domain.InvoiceDunningRun{
		InvoiceID:    invoiceID,
		CustomerID:   customerID,
		PolicyID:     policy.ID,
		State:        domain.DunningScheduled,
		Reason:       "payment_failed",
		AttemptCount: 0,
		NextActionAt: nextActionAt,
	})
	if err != nil {
		return domain.InvoiceDunningRun{}, fmt.Errorf("create dunning run: %w", err)
	}

	// Record start event
	s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:     run.ID,
		InvoiceID: invoiceID,
		EventType: domain.DunningEventStarted,
		State:     domain.DunningScheduled,
		Reason:    "payment_failed",
	})

	slog.Info("dunning started", "run_id", run.ID, "invoice_id", invoiceID)
	return run, nil
}

// ProcessDueRuns finds runs due for action and executes retries.
func (s *Service) ProcessDueRuns(ctx context.Context, tenantID string, limit int) (int, []error) {
	if limit <= 0 {
		limit = 20
	}

	dueRuns, err := s.store.ListDueRuns(ctx, tenantID, time.Now().UTC(), limit)
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

	if run.Paused {
		return nil // Skip paused runs
	}

	// Check if max retries exhausted
	if run.AttemptCount >= policy.MaxRetryAttempts {
		return s.exhaustRun(ctx, tenantID, run, policy)
	}

	// Attempt retry
	run.AttemptCount++
	now := time.Now().UTC()
	run.LastAttemptAt = &now
	run.State = domain.DunningAwaitingResult

	// Record retry attempt event
	s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:        run.ID,
		InvoiceID:    run.InvoiceID,
		EventType:    domain.DunningEventRetryAttempted,
		State:        domain.DunningAwaitingResult,
		AttemptCount: run.AttemptCount,
	})

	// Schedule next action
	if run.AttemptCount < policy.MaxRetryAttempts && run.AttemptCount < len(policy.RetrySchedule) {
		d, err := time.ParseDuration(policy.RetrySchedule[run.AttemptCount])
		if err == nil {
			t := now.Add(d)
			run.NextActionAt = &t
		}
	} else {
		run.NextActionAt = nil
	}

	if _, err := s.store.UpdateRun(ctx, tenantID, run); err != nil {
		return err
	}

	slog.Info("dunning retry attempted",
		"run_id", run.ID,
		"invoice_id", run.InvoiceID,
		"attempt", run.AttemptCount,
	)

	return nil
}

func (s *Service) exhaustRun(ctx context.Context, tenantID string, run domain.InvoiceDunningRun, policy domain.DunningPolicy) error {
	now := time.Now().UTC()
	run.State = domain.DunningExhausted
	run.ResolvedAt = &now
	run.NextActionAt = nil

	switch policy.FinalAction {
	case domain.DunningActionManualReview:
		run.Resolution = domain.ResolutionEscalated
		run.State = domain.DunningEscalated
	case domain.DunningActionPause:
		run.Resolution = domain.ResolutionNotCollectible
	default:
		run.Resolution = domain.ResolutionNotCollectible
	}

	s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:        run.ID,
		InvoiceID:    run.InvoiceID,
		EventType:    domain.DunningEventEscalated,
		State:        run.State,
		AttemptCount: run.AttemptCount,
		Reason:       "max_retries_exhausted",
	})

	if _, err := s.store.UpdateRun(ctx, tenantID, run); err != nil {
		return err
	}

	slog.Info("dunning exhausted",
		"run_id", run.ID,
		"invoice_id", run.InvoiceID,
		"final_action", policy.FinalAction,
	)

	return nil
}

// ResolveRun marks a dunning run as resolved (e.g., after manual payment).
func (s *Service) ResolveRun(ctx context.Context, tenantID, runID string, resolution domain.DunningResolution) (domain.InvoiceDunningRun, error) {
	run, err := s.store.GetRun(ctx, tenantID, runID)
	if err != nil {
		return domain.InvoiceDunningRun{}, err
	}

	now := time.Now().UTC()
	run.State = domain.DunningResolved
	run.Resolution = resolution
	run.ResolvedAt = &now
	run.NextActionAt = nil

	s.store.CreateEvent(ctx, tenantID, domain.InvoiceDunningEvent{
		RunID:     run.ID,
		InvoiceID: run.InvoiceID,
		EventType: domain.DunningEventResolved,
		State:     domain.DunningResolved,
		Reason:    string(resolution),
	})

	return s.store.UpdateRun(ctx, tenantID, run)
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
	if policy.GracePeriodDays <= 0 {
		policy.GracePeriodDays = 3
	}
	if policy.FinalAction == "" {
		policy.FinalAction = domain.DunningActionManualReview
	}
	return s.store.UpsertPolicy(ctx, tenantID, policy)
}

// ListRuns returns dunning runs matching the filter.
func (s *Service) ListRuns(ctx context.Context, filter RunListFilter) ([]domain.InvoiceDunningRun, error) {
	return s.store.ListRuns(ctx, filter)
}
