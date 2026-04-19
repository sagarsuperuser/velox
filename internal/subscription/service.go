package subscription

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

type Service struct {
	store Store
	clock clock.Clock
}

func NewService(store Store, clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.Real()
	}
	return &Service{store: store, clock: clk}
}

type CreateInput struct {
	Code          string                         `json:"code"`
	DisplayName   string                         `json:"display_name"`
	CustomerID    string                         `json:"customer_id"`
	PlanID        string                         `json:"plan_id"`
	BillingTime   domain.SubscriptionBillingTime `json:"billing_time"`
	TrialDays     int                            `json:"trial_days,omitempty"`
	StartNow      bool                           `json:"start_now,omitempty"`
	UsageCapUnits *int64                         `json:"usage_cap_units,omitempty"`
	OverageAction string                         `json:"overage_action,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Subscription, error) {
	code := strings.TrimSpace(input.Code)
	displayName := strings.TrimSpace(input.DisplayName)

	if code == "" {
		return domain.Subscription{}, fmt.Errorf("code is required")
	}
	if !slugPattern.MatchString(code) {
		return domain.Subscription{}, fmt.Errorf("code must contain only alphanumeric characters, hyphens, and underscores")
	}
	if displayName == "" {
		return domain.Subscription{}, fmt.Errorf("display_name is required")
	}
	if input.CustomerID == "" {
		return domain.Subscription{}, fmt.Errorf("customer_id is required")
	}
	if input.PlanID == "" {
		return domain.Subscription{}, fmt.Errorf("plan_id is required")
	}

	billingTime := input.BillingTime
	if billingTime == "" {
		billingTime = domain.BillingTimeCalendar
	}
	if billingTime != domain.BillingTimeCalendar && billingTime != domain.BillingTimeAnniversary {
		return domain.Subscription{}, fmt.Errorf("billing_time must be calendar or anniversary")
	}

	status := domain.SubscriptionDraft
	now := s.clock.Now()

	var trialStart, trialEnd *time.Time
	var startedAt *time.Time

	var periodStart, periodEnd, nextBilling *time.Time

	if input.TrialDays > 0 {
		ts := now
		te := now.AddDate(0, 0, input.TrialDays)
		trialStart = &ts
		trialEnd = &te
		status = domain.SubscriptionActive
		startedAt = &now
		// After trial: align to calendar boundary if calendar billing
		if billingTime == domain.BillingTimeCalendar {
			ps := beginningOfMonth(te.AddDate(0, 1, 0)) // 1st of next month after trial
			pe := ps.AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		} else {
			ps := te
			pe := te.AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		}
	} else if input.StartNow {
		status = domain.SubscriptionActive
		startedAt = &now
		if billingTime == domain.BillingTimeCalendar {
			// First partial period: today → 1st of next month (arrears billing)
			ps := now
			pe := beginningOfMonth(now).AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe // Bill when period closes
		} else {
			// Anniversary: today → today + 1 month
			ps := now
			pe := now.AddDate(0, 1, 0)
			periodStart = &ps
			periodEnd = &pe
			nextBilling = &pe
		}
	}

	overageAction := input.OverageAction
	if overageAction == "" {
		overageAction = "charge"
	}

	return s.store.Create(ctx, tenantID, domain.Subscription{
		Code:                      code,
		DisplayName:               displayName,
		CustomerID:                input.CustomerID,
		PlanID:                    input.PlanID,
		Status:                    status,
		BillingTime:               billingTime,
		TrialStartAt:              trialStart,
		TrialEndAt:                trialEnd,
		StartedAt:                 startedAt,
		CurrentBillingPeriodStart: periodStart,
		CurrentBillingPeriodEnd:   periodEnd,
		NextBillingAt:             nextBilling,
		UsageCapUnits:             input.UsageCapUnits,
		OverageAction:             overageAction,
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Subscription, int, error) {
	return s.store.List(ctx, filter)
}

func (s *Service) Activate(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	sub, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Subscription{}, err
	}
	if sub.Status != domain.SubscriptionDraft {
		return domain.Subscription{}, fmt.Errorf("can only activate draft subscriptions, current status: %s", sub.Status)
	}

	now := s.clock.Now()
	sub.Status = domain.SubscriptionActive
	sub.ActivatedAt = &now
	sub.StartedAt = &now

	// Set billing period if not already set
	if sub.CurrentBillingPeriodStart == nil {
		ps := beginningOfMonth(now)
		pe := ps.AddDate(0, 1, 0)
		sub.CurrentBillingPeriodStart = &ps
		sub.CurrentBillingPeriodEnd = &pe
		sub.NextBillingAt = &pe
	}

	return s.store.Update(ctx, tenantID, sub)
}

type ChangePlanInput struct {
	NewPlanID string `json:"new_plan_id"`
	Immediate bool   `json:"immediate"` // true = change now with proration, false = change at period end
}

type ProrationDetail struct {
	OldPlanID       string  `json:"old_plan_id"`
	NewPlanID       string  `json:"new_plan_id"`
	ProrationFactor float64 `json:"proration_factor"`
	AmountCents     int64   `json:"amount_cents"`
	Type            string  `json:"type"` // "invoice" or "credit"
	InvoiceID       string  `json:"invoice_id,omitempty"`
}

type ChangePlanResult struct {
	Subscription    domain.Subscription `json:"subscription"`
	ProrationFactor float64             `json:"proration_factor,omitempty"`
	EffectiveAt     time.Time           `json:"effective_at"`
	Proration       *ProrationDetail    `json:"proration,omitempty"`
}

// ChangePlan upgrades or downgrades a subscription's plan.
// If immediate=true, calculates proration based on remaining days in the billing period.
// If immediate=false, the plan change takes effect at the next billing cycle.
func (s *Service) ChangePlan(ctx context.Context, tenantID, id string, input ChangePlanInput) (ChangePlanResult, error) {
	sub, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return ChangePlanResult{}, err
	}
	if sub.Status != domain.SubscriptionActive {
		return ChangePlanResult{}, fmt.Errorf("can only change plan for active subscriptions, current status: %s", sub.Status)
	}
	if input.NewPlanID == "" {
		return ChangePlanResult{}, fmt.Errorf("new_plan_id is required")
	}
	if input.NewPlanID == sub.PlanID {
		return ChangePlanResult{}, fmt.Errorf("new plan is the same as current plan")
	}

	now := s.clock.Now()
	result := ChangePlanResult{}

	if input.Immediate {
		// Calculate proration factor: remaining days / total days in period
		if sub.CurrentBillingPeriodStart != nil && sub.CurrentBillingPeriodEnd != nil {
			totalDays := sub.CurrentBillingPeriodEnd.Sub(*sub.CurrentBillingPeriodStart).Hours() / 24
			remainingDays := sub.CurrentBillingPeriodEnd.Sub(now).Hours() / 24
			if totalDays > 0 && remainingDays > 0 {
				result.ProrationFactor = remainingDays / totalDays
			}
		}
		result.EffectiveAt = now
	} else {
		// Change at next billing cycle
		if sub.CurrentBillingPeriodEnd != nil {
			result.EffectiveAt = *sub.CurrentBillingPeriodEnd
		} else {
			result.EffectiveAt = now
		}
	}

	sub.PreviousPlanID = sub.PlanID
	sub.PlanID = input.NewPlanID
	planChangedAt := now
	sub.PlanChangedAt = &planChangedAt

	updated, err := s.store.Update(ctx, tenantID, sub)
	if err != nil {
		return ChangePlanResult{}, err
	}
	result.Subscription = updated
	return result, nil
}

// Pause pauses an active subscription. Can be resumed later.
func (s *Service) Pause(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.store.PauseAtomic(ctx, tenantID, id)
}

// Resume resumes a paused subscription.
func (s *Service) Resume(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.store.ResumeAtomic(ctx, tenantID, id)
}

func (s *Service) Cancel(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.store.CancelAtomic(ctx, tenantID, id)
}

func beginningOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}
