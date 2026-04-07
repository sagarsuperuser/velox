package subscription

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

type CreateInput struct {
	Code        string                      `json:"code"`
	DisplayName string                      `json:"display_name"`
	CustomerID  string                      `json:"customer_id"`
	PlanID      string                      `json:"plan_id"`
	BillingTime domain.SubscriptionBillingTime `json:"billing_time"`
	TrialDays   int                         `json:"trial_days,omitempty"`
	StartNow    bool                        `json:"start_now,omitempty"`
}

func (s *Service) Create(ctx context.Context, tenantID string, input CreateInput) (domain.Subscription, error) {
	code := strings.TrimSpace(input.Code)
	displayName := strings.TrimSpace(input.DisplayName)

	if code == "" {
		return domain.Subscription{}, fmt.Errorf("code is required")
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
	now := time.Now().UTC()

	var trialStart, trialEnd *time.Time
	var startedAt *time.Time

	if input.TrialDays > 0 {
		ts := now
		te := now.AddDate(0, 0, input.TrialDays)
		trialStart = &ts
		trialEnd = &te
		status = domain.SubscriptionActive
		startedAt = &now
	} else if input.StartNow {
		status = domain.SubscriptionActive
		startedAt = &now
	}

	return s.store.Create(ctx, tenantID, domain.Subscription{
		Code:         code,
		DisplayName:  displayName,
		CustomerID:   input.CustomerID,
		PlanID:       input.PlanID,
		Status:       status,
		BillingTime:  billingTime,
		TrialStartAt: trialStart,
		TrialEndAt:   trialEnd,
		StartedAt:    startedAt,
	})
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]domain.Subscription, error) {
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

	now := time.Now().UTC()
	sub.Status = domain.SubscriptionActive
	sub.ActivatedAt = &now
	sub.StartedAt = &now
	return s.store.Update(ctx, tenantID, sub)
}

func (s *Service) Cancel(ctx context.Context, tenantID, id string) (domain.Subscription, error) {
	sub, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return domain.Subscription{}, err
	}
	if sub.Status != domain.SubscriptionActive && sub.Status != domain.SubscriptionPaused {
		return domain.Subscription{}, fmt.Errorf("can only cancel active or paused subscriptions, current status: %s", sub.Status)
	}

	now := time.Now().UTC()
	sub.Status = domain.SubscriptionCanceled
	sub.CanceledAt = &now
	return s.store.Update(ctx, tenantID, sub)
}
